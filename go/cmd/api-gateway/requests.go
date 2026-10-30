// This file contains the handlers for the requests that the API Gateway receives from the client
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Jorrit05/DYNAMOS/pkg/api"
	"github.com/Jorrit05/DYNAMOS/pkg/lib"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.opencensus.io/trace"
)

const (
	StatusPending = "pending"
	StatusDone    = "done"
	StatusFailed  = "failed"
)

var (
	activeJobID        string
	activeJobLock      sync.Mutex   // to allow only 1 active job at any time
	trainingRequests   = sync.Map{} // map[string]TrainingRequestData
	formattedEndpoint  = "http://%s:8080/agent/v1/vflTrainRequest/%s"
	clientsMutex       = &sync.Mutex{}
	trainingStateMutex sync.Mutex
)

// #region TrainingRequestData helpers

type TrainingRequestData struct {
	Status   string
	Results  []map[string]any
	Metadata map[string]any
	// add more fields as needed
}

func getTrainingRequest(jobId string) (TrainingRequestData, bool) {
	v, ok := trainingRequests.Load(jobId)
	if !ok {
		logger.Sugar().Debug("Not found training request: ", jobId)
		return TrainingRequestData{}, false
	}

	return v.(TrainingRequestData), true
}

func addAndUpdateTrainingRequest(jobId string, cycle int64, clientsNumber int, timing map[string]any) {
	trainingStateMutex.Lock()
	defer trainingStateMutex.Unlock()
	result := map[string]any{
		"timestamp":   time.Now().Format(time.RFC3339),
		"train_round": cycle,
		"clients":     clientsNumber,
		"timing":      timing,
	}

	reqData, _ := getTrainingRequest(jobId)

	if reqData.Results == nil {
		reqData.Results = make([]map[string]any, 0)
	}

	reqData.Results = append(reqData.Results, result)
	trainingRequests.Store(jobId, reqData)

}

func updateTrainingRequest(jobId string, accuracies map[string]float64) error {
	trainingStateMutex.Lock()
	defer trainingStateMutex.Unlock()
	reqData, _ := getTrainingRequest(jobId)
	results := reqData.Results

	for _, res := range results {
		trainCycle := fmt.Sprintf("%v", res["train_round"])
		_, ok := accuracies[trainCycle]
		if !ok {
			return fmt.Errorf("final accuracy missing for cycle %s", trainCycle)
		}
	}
	for _, res := range results {
		res["accuracy"] = accuracies[fmt.Sprintf("%v", res["train_round"])]
	}

	reqData.Results = results
	trainingRequests.Store(jobId, reqData)
	return nil
}

// #endregion

func getTrainingStatusHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger.Sugar().Info("Starting getTrainingStatusHandler")
		requestID := r.URL.Query().Get("id")
		trainingStateMutex.Lock()
		defer trainingStateMutex.Unlock()
		reqData, ok := getTrainingRequest(requestID)
		if !ok {
			http.Error(w, "Request ID not found", http.StatusNotFound)
			return
		}

		logger.Sugar().Debug("Found training request: ", requestID)
		resp := map[string]any{
			"request_id": requestID,
			"status":     reqData.Status,
			"metadata":   reqData.Metadata,
			"results":    reqData.Results,
		}
		respBytes, _ := json.MarshalIndent(resp, "", "    ")
		w.WriteHeader(http.StatusOK)
		w.Write(respBytes)
	}
}

func requestHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		activeJobLock.Lock()
		defer activeJobLock.Unlock()

		// Check for existing active job
		if activeJobID != "" {
			v, _ := trainingRequests.Load(activeJobID)
			reqData := v.(TrainingRequestData)
			resp := map[string]any{
				"error":             "A training job is already in progress.",
				"active_request_id": activeJobID,
				"active_status":     reqData.Status,
			}
			respBytes, _ := json.Marshal(resp)
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write(respBytes)
			return
		}

		// Accept new job
		requestID := uuid.New().String()
		activeJobID = requestID
		reqData := TrainingRequestData{
			Status: StatusPending,
			Metadata: map[string]any{
				"created_at": time.Now().Format(time.RFC3339),
			},
			Results: []map[string]any{},
		}
		trainingRequests.Store(requestID, reqData)

		resp := map[string]any{
			"request_id": requestID,
			"status":     StatusPending,
		}

		logger.Sugar().Info("Accepted new job with id: ", activeJobID)

		// Parse the request body
		body, err := api.GetRequestBody(w, r, serviceName)
		if err != nil {
			return
		}

		var apiReqApproval api.RequestApproval
		if err := json.Unmarshal(body, &apiReqApproval); err != nil {
			logger.Sugar().Errorf("Error unmMarshalling get apiReqApproval: %v", err)
			return
		}

		userPb := &pb.User{
			Id:       apiReqApproval.User.Id,
			UserName: apiReqApproval.User.UserName,
		}

		var dataRequestInterface map[string]any
		if err := json.Unmarshal(apiReqApproval.DataRequest, &dataRequestInterface); err != nil {
			logger.Sugar().Errorf("Error unmarhsalling get request: %v", err)
			return
		}

		dataRequestOptions := &api.DataRequestOptions{}
		dataRequestOptions.Options = make(map[string]bool)
		if err := json.Unmarshal(apiReqApproval.DataRequest, &dataRequestOptions); err != nil {
			logger.Sugar().Errorf("Error unmMarshalling get apiReqApproval: %v", err)
			return
		}

		dataRequestInterface["user"] = userPb

		// Create protobuf struct for the req approval flow
		protoRequest := &pb.RequestApproval{
			Type:             apiReqApproval.Type,
			User:             userPb,
			DataProviders:    apiReqApproval.DataProviders,
			DestinationQueue: "policyEnforcer-in",
			Options:          dataRequestOptions.Options,
		}

		respBytes, _ := json.Marshal(resp)
		w.WriteHeader(http.StatusAccepted)
		w.Write(respBytes)

		// ---- TRIGGER TRAINING IN BACKGROUND ----
		go func() {
			startTraining(protoRequest, dataRequestInterface, apiReqApproval, r, requestID)
		}()

	}
}

func startTraining(protoRequest *pb.RequestApproval, dataRequestInterface map[string]any, apiReqApproval api.RequestApproval, r *http.Request, requestID string) {
	approvalStart := time.Now()
	logger.Debug("Starting training process...")
	// Requests may take up to 10 minutes now
	ctxWithTimeout, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Start a new span with the context that has a timeout
	ctx, span := trace.StartSpan(ctxWithTimeout, "requestApprovalHandler")
	defer span.End()

	// Create a channel to receive the response
	responseChan := make(chan validation)

	requestApprovalMutex.Lock()
	requestApprovalMap[protoRequest.User.Id] = responseChan
	requestApprovalMutex.Unlock()

	_, err := c.SendRequestApproval(ctx, protoRequest)
	if err != nil {
		logger.Sugar().Errorf("error in sending requestapproval: %v", err)
	}

	select {
	case validationStruct := <-responseChan:
		msg := validationStruct.response

		logger.Sugar().Infof("Received response, %s", msg.Type)
		if msg.Type != "requestApprovalResponse" {
			logger.Sugar().Errorf("Unexpected message received, type: %s", msg.Type)
			// http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		requestMetadata := &pb.RequestMetadata{
			JobId: msg.JobId,
		}
		dataRequestInterface["requestMetadata"] = requestMetadata

		logger.Sugar().Infow("Request approved", "request_id", requestID, "job_id", msg.JobId, "providers", len(msg.AuthorizedProviders))

		var response []byte

		if apiReqApproval.Type == "vflTrainModelRequest" {
			ctxWithoutCancel := context.WithoutCancel(r.Context())
			response = runVFLTraining(dataRequestInterface, msg.AuthorizedProviders, msg.JobId, ctxWithoutCancel, requestID, time.Since(approvalStart))

		} else {
			// Marshal the combined data back into JSON for forwarding
			dataRequestJson, err := json.Marshal(dataRequestInterface)
			if err != nil {
				logger.Sugar().Errorf("Error marshalling combined data: %v", err)
				return
			}

			response = sendDataToAuthProviders(dataRequestJson, msg.AuthorizedProviders, apiReqApproval.Type, msg.JobId)
		}

		// w.WriteHeader(http.StatusOK)
		// w.Write(response)
		logger.Sugar().Info("Training process completed for request id: ", requestID)
		logger.Sugar().Infow("Training response ready", "request_id", requestID, "response_bytes", len(response))
		return

	case <-ctx.Done():
		// http.Error(w, "Request timed out", http.StatusRequestTimeout)
		return
	}

}

type TrainingRoundData struct {
	cycle         int64
	sampleIndexes string
	embeddings    []string
	gradients     []string
	activeClients []ClientData
	start         time.Time
	phases        []*phaseRecorder
}

type vflPipeline struct {
	sampleChan     chan *TrainingRoundData
	embeddingsChan chan *TrainingRoundData
	gradientsChan  chan *TrainingRoundData
	done           chan struct{}
	mu             sync.Mutex
	phases         []*phaseRecorder
	errors         []string
	firstReply     time.Time
}

func makeVFLChannels() *vflPipeline {
	return &vflPipeline{
		sampleChan:     make(chan *TrainingRoundData, 1),
		embeddingsChan: make(chan *TrainingRoundData, 1),
		gradientsChan:  make(chan *TrainingRoundData, 1),
		done:           make(chan struct{}),
	}
}

func (p *vflPipeline) phase(data *TrainingRoundData, name string) *phaseRecorder {
	phase := startPhase(name)
	cycle := data.cycle
	phase.cycle = &cycle
	data.phases = append(data.phases, phase)
	p.mu.Lock()
	p.phases = append(p.phases, phase)
	p.mu.Unlock()
	return phase
}

func (p *vflPipeline) fail(cycle int64, stage string, err error) {
	logger.Sugar().Errorw("Pipeline stage failed", "cycle", cycle, "stage", stage, "error", err)
	p.mu.Lock()
	p.errors = append(p.errors, fmt.Sprintf("cycle %d %s: %v", cycle, stage, err))
	p.mu.Unlock()
}

func cloneDataRequest(dataRequest map[string]any, requestType string, data map[string]any) map[string]any {
	request := maps.Clone(dataRequest)
	request["type"] = requestType
	request["data"] = data
	return request
}

func getSafeClients(clients *[]ClientData) []ClientData {
	clientsMutex.Lock()
	currentClients := make([]ClientData, len(*clients))
	copy(currentClients, *clients)
	clientsMutex.Unlock()
	return currentClients
}

func startVFLPipeline(p *vflPipeline, dataRequest map[string]any, clients *[]ClientData, serverAuth string, learningRate float64, serverUrl string, trainingBacktrack int64, communicationFrequency int64, requestID string) {
	serverTarget := strings.ToLower(serverAuth)
	serverEndpoint := fmt.Sprintf(formattedEndpoint, serverUrl, serverTarget)

	// Gets the embeddings
	go func() {
		var wg sync.WaitGroup
		var mu sync.Mutex

		defer close(p.embeddingsChan)
		for data := range p.sampleChan {
			responses := map[string]string{}
			var existingError error
			phase := p.phase(data, "client_forward")
			logger.Sugar().Debug("Embeddings routine. Samples received! Cycle: ", data.cycle)

			currentClients := getSafeClients(clients)

			for _, client := range currentClients {
				auth := client.Auth
				url := client.Url

				logger.Sugar().Info("[", auth, "] [vflTrainRequest] [Cycle: ", data.cycle, "] Requesting intermediate embeddings at url: ", url)

				wg.Add(1)
				target := strings.ToLower(auth)

				ips, err := net.LookupIP(url)
				if err == nil && len(ips) != 0 {
					url = ips[0].String()
				}

				endpoint := fmt.Sprintf(formattedEndpoint, url, target)

				go func() {
					request := cloneDataRequest(
						dataRequest,
						"vflTrainRequest",
						map[string]any{
							"cycle":                data.cycle,
							"sample_batch_indexes": data.sampleIndexes,
						},
					)

					responseData, err := sendRequest(phase, target, endpoint, request)

					mu.Lock()
					if err != nil {
						existingError = err
						logger.Sugar().Errorf("Error sending data, %v", err)
					} else {
						dataJson := responseData.Data.AsMap()
						embeddings, ok := dataJson["embeddings"].(string)

						if !ok {
							logger.Sugar().Error("No embeddings found in the return data.")
							existingError = fmt.Errorf("missing embeddings from %s", target)
						}

						responses[target] = embeddings
					}
					mu.Unlock()

					wg.Done()
					logger.Sugar().Debugw("Embedding reply processed", "participant", target, "cycle", data.cycle)
				}()
			}

			wg.Wait()
			phase.finish()

			if existingError != nil {
				p.fail(data.cycle, phase.name, existingError)
				continue
			}
			embeddingList := []string{}
			for _, client := range currentClients {
				if emb, ok := responses[strings.ToLower(client.Auth)]; ok {
					embeddingList = append(embeddingList, emb)
				}
			}

			data.embeddings = embeddingList
			data.activeClients = currentClients
			logger.Sugar().Info("[CLIENTS] [vflTrainRequest] [Cycle: ", data.cycle, "] Pushing embeddings into embeddingsChan")
			p.embeddingsChan <- data
		}
	}()

	// Gets the gradients
	go func() {
		defer close(p.gradientsChan)
		for data := range p.embeddingsChan {
			phase := p.phase(data, "server_aggregate")
			logger.Sugar().Debug("Gradients routine. Embeddings received! Cycle: ", data.cycle)

			logger.Sugar().Info("[SERVER] [vflAggregateRequest] [Cycle: ", data.cycle, "] Requesting gradients.")

			request := cloneDataRequest(
				dataRequest,
				"vflAggregateRequest",
				map[string]any{
					"cycle":                   data.cycle,
					"embeddings":              data.embeddings,
					"trainingBacktrack":       trainingBacktrack,
					"sample_batch_indexes":    data.sampleIndexes,
					"communication_frequency": communicationFrequency,
				},
			)

			serverResponse, err := sendRequest(phase, serverTarget, serverEndpoint, request)
			phase.finish()
			if err != nil {
				p.fail(data.cycle, phase.name, err)
				continue
			}

			accuraciesStr := serverResponse.Data.GetFields()["accuracies"].GetStringValue()
			gradientList := serverResponse.Data.GetFields()["gradients"].GetListValue().GetValues()

			var accuracies map[string]float64
			err = json.Unmarshal([]byte(accuraciesStr), &accuracies)
			if err != nil {
				p.fail(data.cycle, phase.name, fmt.Errorf("decode accuracies: %w", err))
				continue
			}

			gradients := []string{}
			for _, val := range gradientList {
				gradients = append(gradients, val.GetStringValue())
			}
			if len(gradients) != len(data.activeClients) {
				p.fail(data.cycle, phase.name, fmt.Errorf("received %d gradients for %d clients", len(gradients), len(data.activeClients)))
				continue
			}
			invalidGradient := false
			for index, gradient := range gradients {
				if gradient == "" {
					p.fail(data.cycle, phase.name, fmt.Errorf("missing gradient for %s", data.activeClients[index].Auth))
					invalidGradient = true
				}
			}
			if invalidGradient {
				continue
			}

			logger.Sugar().Info("[SERVER] [vflAggregateRequest] [Cycle: ", data.cycle, "] Response OK.")
			logger.Sugar().Info("[SERVER] [vflAggregateRequest] Pushing gradients into gradientsChan")

			data.gradients = gradients
			p.gradientsChan <- data
		}

	}()

	// Performs gradient descent
	go func() {
		defer close(p.done)
		for data := range p.gradientsChan {
			phase := p.phase(data, "client_update_dispatch")
			var errorMutex sync.Mutex
			var updateError error
			logger.Sugar().Debug("Gradient descent routine. Gradients received! Cycle: ", data.cycle)

			var wg sync.WaitGroup

			for index, client := range data.activeClients {

				target := strings.ToLower(client.Auth)
				endpoint := fmt.Sprintf(formattedEndpoint, client.Url, target)

				logger.Sugar().Info("[", target, "] [vflGradientDescentRequest] [Cycle: ", data.cycle, "] Perform gradient descent for ", communicationFrequency, " times")

				wg.Add(1)
				go func() {
					defer wg.Done()
					request := cloneDataRequest(
						dataRequest,
						"vflGradientDescentRequest",
						map[string]any{
							"gradients":               data.gradients[index],
							"cycle":                   data.cycle,
							"learning_rate":           learningRate,
							"communication_frequency": communicationFrequency,
						},
					)

					_, err := sendRequest(phase, target, endpoint, request)
					if err != nil {
						errorMutex.Lock()
						updateError = err
						errorMutex.Unlock()
					} else {
						logger.Sugar().Info("[", target, "] [vflGradientDescentRequest] [Cycle: ", data.cycle, "] Response OK.")
					}
				}()
			}
			wg.Wait()
			phase.finish()
			if updateError != nil {
				p.fail(data.cycle, phase.name, updateError)
				continue
			}
			timing := pipelineTiming(data.start, time.Now(), data.phases)
			timing["cycle_latency_ms"] = timing["duration_ms"]
			p.mu.Lock()
			if p.firstReply.IsZero() {
				p.firstReply = time.Now()
			}
			p.mu.Unlock()
			addAndUpdateTrainingRequest(requestID, data.cycle, len(data.activeClients), timing)
		}

		logger.Sugar().Info("Gradients descent has finished!")
	}()
}

func runVFLTrainingRound(p *vflPipeline, dataRequest map[string]any, serverAuth string, serverUrl string, sampleBatchSize int64, cycle int64) error {
	data := &TrainingRoundData{cycle: cycle, start: time.Now()}
	phase := p.phase(data, "server_sample")
	serverTarget := strings.ToLower(serverAuth)
	serverEndpoint := fmt.Sprintf(formattedEndpoint, serverUrl, serverTarget)

	logger.Sugar().Info("[SERVER] [vflSampleBatchRequest] [Cycle: ", cycle, "] Requesting mini-batch samples.")
	request := cloneDataRequest(
		dataRequest,
		"vflSampleBatchRequest",
		map[string]any{
			"sample_batch_size": sampleBatchSize,
		},
	)

	responseData, err := sendRequest(phase, serverTarget, serverEndpoint, request)
	phase.finish()

	if err != nil {
		return err
	} else {
		sampleBatchIndexes, ok := responseData.GetData().AsMap()["sample_batch_indexes"].(string)
		if !ok || sampleBatchIndexes == "" {
			return fmt.Errorf("server response missing sample_batch_indexes")
		}

		logger.Sugar().Info("[SERVER] [vflSampleBatchRequest] [Cycle: ", cycle, "] Response OK.")
		logger.Sugar().Info("[SERVER] [vflSampleBatchRequest] [Cycle: ", cycle, "] Pushing samples into sampleChan")
		data.sampleIndexes = sampleBatchIndexes
		p.sampleChan <- data
	}

	return nil
}

// #region VFL requests

func getVFLAccuracies(phase *phaseRecorder, dataRequest map[string]any, serverAuth string, serverUrl string) (map[string]float64, error) {
	serverTarget := strings.ToLower(serverAuth)
	serverEndpoint := fmt.Sprintf(formattedEndpoint, serverUrl, serverTarget)
	var accuracies map[string]float64

	request := cloneDataRequest(
		dataRequest,
		"vflGetAccuraciesRequest",
		nil,
	)

	serverResponse, err := sendRequest(phase, serverTarget, serverEndpoint, request)
	if err != nil {
		return nil, err
	} else {
		accuraciesStr := serverResponse.Data.GetFields()["accuracies"].GetStringValue()

		err = json.Unmarshal([]byte(accuraciesStr), &accuracies)
		if err != nil {
			return nil, fmt.Errorf("decode final accuracies: %w", err)
		}
	}

	return accuracies, nil
}

// #endregion

func extractValueOrDefault[T ~int64 | ~float64](data map[string]any, propertyName string, defaultValue T) T {
	value, ok := data[propertyName].(float64)
	if ok {
		return T(value)
	}

	logger.Sugar().Debug("Property [", propertyName, "] wasn't found on the request parameters. Default value [", defaultValue, "] is going to be used instead.")
	return defaultValue
}

func checkPolicyUpdate(clients *[]ClientData, user *pb.User) {
	policyUpdateChan := make(chan PolicyUpdateResponse)

	// There is a misalignment because there are static User.Ids in different parts of the code.
	// This has to be changed in the future.
	logger.Sugar().Debug("Updating policyUpdateMap with key: ", user.Id)
	policyUpdateMutex.Lock()
	policyUpdateMap[user.Id] = policyUpdateChan
	policyUpdateMutex.Unlock()

	go func() {

		for policyUpdateResponse := range policyUpdateChan {
			availableProviders, err := getAvailableProviders()

			if err != nil {
				logger.Sugar().Errorf("A problem occurred while fetching providers GetAvailableProviders: ", err)
			}

			validDataproviders := policyUpdateResponse.GetValidDataproviders()

			// We exclude server from valid data providers
			if len(getSafeClients(clients)) != len(validDataproviders)-1 {
				logger.Sugar().Debug("Clients before policy update: ", getSafeClients(clients))

				var activeClients []ClientData
				for auth, agentDetail := range availableProviders {
					if strings.ToLower(auth) == "server" {
						continue
					}

					_, isValid := validDataproviders[auth]

					if !isValid {
						continue
					}

					activeClients = append(activeClients, ClientData{
						Auth: auth,
						Url:  agentDetail.Dns,
					})
				}

				clientsMutex.Lock()
				*clients = activeClients
				clientsMutex.Unlock()
				logger.Sugar().Debug("Clients after policy update: ", getSafeClients(clients))
			}
		}

		policyUpdateMutex.Lock()
		delete(policyUpdateMap, user.Id)
		policyUpdateMutex.Unlock()
	}()
}

type ClientData struct {
	Auth string
	Url  string
}

func runVFLTraining(dataRequest map[string]any, authorizedProviders map[string]string, jobId string, ctx context.Context, requestID string, approvalDuration time.Duration) []byte {
	trainingStart := time.Now()
	clients := &[]ClientData{}
	var serverUrl string
	var serverAuth string
	var finalAccuracy float64
	var wg sync.WaitGroup

	// Default parameters values
	var sample_batch_size int64 = 64
	var communication_frequency int64 = 10
	var cycles int64 = 10
	var learning_rate float64 = 0.05
	var policy_removal int64 = -1
	var policy_reintroduction int64 = -1
	var dataProviders []string = []string{}

	var trainingBacktrack int64 = 0 // Default value

	data, ok := dataRequest["data"].(map[string]any)
	logger.Sugar().Info("Data from req: ", data)

	if ok {
		// Parameters extraction
		sample_batch_size = extractValueOrDefault(data, "sample_batch_size", sample_batch_size)
		communication_frequency = extractValueOrDefault(data, "communication_frequency", communication_frequency)
		cycles = extractValueOrDefault(data, "cycles", cycles)
		learning_rate = extractValueOrDefault(data, "learning_rate", learning_rate)
		trainingBacktrack = extractValueOrDefault(data, "training_backtrack", trainingBacktrack)
		policy_removal = extractValueOrDefault(data, "policy_removal", policy_removal)
		policy_reintroduction = extractValueOrDefault(data, "policy_reintroduction", policy_reintroduction)
	}

	metadata := map[string]any{
		"total_rounds":            cycles,
		"policy_removal":          policy_removal,
		"training_backtrack":      trainingBacktrack,
		"policy_reintroduction":   policy_reintroduction,
		"strategy":                "semi-overlap-fed-bcd",
		"communication_frequency": communication_frequency,
		"sample_batch_size":       sample_batch_size,
		"policy_mode":             "event-driven",
		"completion_semantics":    "gateway pipeline replies drained; background training completion is not confirmed by dispatch acknowledgments",
	}

	// logger.Sugar().Debug("metadata: ", metadata)

	trainingFailed := false

	for auth, url := range authorizedProviders {
		if strings.ToLower(auth) == "server" {
			serverUrl = url
			serverAuth = auth
		} else if url != "" {
			*clients = append(*clients, ClientData{Auth: auth, Url: url})
		}

		dataProviders = append(dataProviders, auth)
	}

	logger.Sugar().Info("Sending ping to start pods...")
	dataRequest["type"] = "vflPingRequest"

	dataRequestJson, err := json.Marshal(dataRequest)
	if err != nil {
		logger.Sugar().Errorf("Error marshalling combined data: %v", err)
		return []byte{}
	}

	user, ok := dataRequest["user"].(*pb.User)

	if !ok {
		logger.Sugar().Info("Did not retrieve User from dataRequest, cannot dynamically verify each training round.")
		user = &pb.User{}
	}

	var noPing bool = false
	var pingMutex sync.Mutex
	warmup := startPhase("warmup")

	for auth, url := range authorizedProviders {
		wg.Add(1)
		target := strings.ToLower(auth)
		endpoint := fmt.Sprintf("http://%s:8080/agent/v1/vflTrainRequest/%s", url, target)

		go func() {
			// TODO: Repeat ping until no error, after 5 tries, cancel request
			for i := range 5 {
				logger.Sugar().Info("Sending ping to: ", target)
				_, err := warmup.send(target, endpoint, dataRequestJson)

				if err == nil {
					break
				}

				logger.Sugar().Info("No response from: ", target)

				if i == 4 {
					pingMutex.Lock()
					noPing = true
					pingMutex.Unlock()
				}
			}

			wg.Done()
		}()
	}

	wg.Wait()
	warmup.finish()
	if noPing {
		logger.Sugar().Error("No ping from a client or the server. Something is wrong.")
		trainingFailed = true
	}

	var iterations int64
	if communication_frequency <= 0 || cycles <= 0 || sample_batch_size <= 0 {
		logger.Sugar().Error("Invalid training parameters: cycles, communication_frequency and sample_batch_size must be positive.")
		trainingFailed = true
	} else {
		iterations = cycles / communication_frequency
		if iterations == 0 {
			logger.Sugar().Error("No synchronization cycles scheduled.")
			trainingFailed = true
		}
	}
	metadata["synchronization_rounds"] = iterations
	metadata["scheduled_local_steps"] = iterations * communication_frequency

	// Creates VFL channels
	pipeline := makeVFLChannels()
	pipelineStart := time.Now()

	// Initializes VFL pipeline
	startVFLPipeline(pipeline, dataRequest, clients, serverAuth, learning_rate, serverUrl, trainingBacktrack, communication_frequency, requestID)

	// Checks if policy changes (from incoming messages)
	checkPolicyUpdate(clients, user)

	var policyRemovalRound int64 = -1
	if policy_removal != -1 && communication_frequency > 0 {
		policyRemovalRound = policy_removal / communication_frequency
	}

	var policyReintroductionRound int64 = -1
	if policy_reintroduction != -1 && communication_frequency > 0 {
		policyReintroductionRound = policy_reintroduction / communication_frequency
	}

	logger.Sugar().Info("Running VFL for ", cycles, " rounds")
	var lastCycle int64 = -1
	for cycle := range iterations {
		if trainingFailed {
			break
		}
		logger.Sugar().Info("Running VFL training round ", cycle)

		// TODO: Implement policy change request
		if policyRemovalRound == cycle {
			logger.Sugar().Info("TODO: Policy change request not yet implemented.")
			api.DeleteRequest(
				"http://orchestrator.orchestrator.svc.cluster.local:8080/api/v1/policyEnforcer/agreements/clientthree",
				"",
				nil)
		}

		// TODO: Implement policy change request
		if policyReintroductionRound == cycle {
			logger.Sugar().Info("Sending in the policy change request, reintroducing client 3 to the agreement.")
			logger.Sugar().Info("TODO: Policy change request not yet implemented. (values are hardcoded)")

			rawPayload := `{
						"name": "clientthree"
						,
						"relations": {
							"requestor": {
								"ID": "GUID",
								"requestTypes": [
									"vflTrainRequest"
								],
								"dataSets": null,
								"allowedArchetypes": [
									"computeToData"
								],
								"allowedComputeProviders": [
									"clientthree"
								]
							}
						},
						"computeProviders": [
							"clientthree"
						],
						"archetypes": [
							"computeToData"
						]
				}`

			api.PutRequest(
				"http://orchestrator.orchestrator.svc.cluster.local:8080/api/v1/policyEnforcer",
				rawPayload,
				nil)
		}

		logger.Sugar().Info("- Sending training request")

		err := runVFLTrainingRound(pipeline, dataRequest, serverAuth, serverUrl, sample_batch_size, cycle)

		if err != nil {
			logger.Sugar().Error("Training round returned an error.")
			trainingFailed = true
			pipeline.fail(cycle, "server_sample", err)
			break
		}
		// }

		lastCycle = cycle

		if trainingFailed {
			break
		}

	}

	close(pipeline.sampleChan)
	enqueueEnd := time.Now()
	<-pipeline.done
	pipelineEnd := time.Now()
	logger.Sugar().Info("VFL gateway reply pipeline drained; background training completion is not confirmed.")
	if len(pipeline.errors) > 0 {
		trainingFailed = true
		metadata["pipeline_errors"] = pipeline.errors
	}

	accuracyPhase := startPhase("final_accuracy")
	accuracies, accuracyErr := getVFLAccuracies(accuracyPhase, dataRequest, serverAuth, serverUrl)
	accuracyPhase.finish()
	if accuracyErr == nil {
		accuracyErr = updateTrainingRequest(requestID, accuracies)
	}
	if accuracyErr != nil {
		logger.Sugar().Errorw("Final accuracy retrieval failed", "error", accuracyErr)
		trainingFailed = true
		metadata["accuracy_error"] = accuracyErr.Error()
	} else if accuracy, ok := accuracies[fmt.Sprint(lastCycle)]; ok {
		finalAccuracy = accuracy
	} else {
		logger.Sugar().Error("Final cycle accuracy missing.")
		trainingFailed = true
	}

	logger.Sugar().Info("-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-")
	logger.Sugar().Info("Final accuracy achieved: ", finalAccuracy)
	logger.Sugar().Info("-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-")

	shutdownRequest := cloneDataRequest(dataRequest, "vflShutdownRequest", nil)

	dataRequestJson, err = json.Marshal(shutdownRequest)
	if err != nil {
		logger.Sugar().Errorf("Error marshalling combined data: %v", err)
		return []byte{}
	}

	shutdown := startPhase("shutdown")
	for auth, url := range authorizedProviders {
		wg.Add(1)
		target := strings.ToLower(auth)
		endpoint := fmt.Sprintf("http://%s:8080/agent/v1/vflTrainRequest/%s", url, target)

		go func() {
			shutdown.send(target, endpoint, dataRequestJson)
			wg.Done()
		}()
	}

	wg.Wait()
	shutdown.finish()
	runTiming := pipelineTiming(pipelineStart, pipelineEnd, pipeline.phases)
	runTiming["approval_ms"] = toMs(approvalDuration)
	runTiming["enqueue_ms"] = toMs(enqueueEnd.Sub(pipelineStart))
	runTiming["drain_ms"] = toMs(pipelineEnd.Sub(enqueueEnd))
	if !pipeline.firstReply.IsZero() {
		runTiming["fill_ms"] = toMs(pipeline.firstReply.Sub(pipelineStart))
	}
	runTiming["warmup"] = warmup.summary()
	runTiming["final_accuracy"] = accuracyPhase.summary()
	runTiming["shutdown"] = shutdown.summary()
	runTiming["total_ms"] = toMs(approvalDuration + time.Since(trainingStart))
	metadata["timing"] = runTiming

	response := map[string]any{
		"jobId":    jobId,
		"accuracy": finalAccuracy,
	}

	logger.Sugar().Info("-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-")
	logger.Sugar().Info("Final accuracy achieved: ", finalAccuracy)
	logger.Sugar().Info("-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-")

	results, _ := getTrainingRequest(requestID)
	metadata["acknowledged_cycles"] = len(results.Results)
	if int64(len(results.Results)) != iterations {
		logger.Sugar().Errorw("Pipeline result count mismatch", "expected", iterations, "received", len(results.Results))
		trainingFailed = true
	}

	new_response := map[string]any{
		"metadata": metadata,
		"results":  results.Results,
	}

	// --- SET STATUS and UNLOCK ---
	trainingStateMutex.Lock()
	v, ok := trainingRequests.Load(requestID)
	if ok {
		reqData := v.(TrainingRequestData)
		reqData.Metadata = metadata
		if trainingFailed {
			reqData.Status = StatusFailed
		} else {
			reqData.Status = StatusDone
		}
		trainingRequests.Store(requestID, reqData)
		logger.Sugar().Infow("Job completed", "requestID", requestID, "status", reqData.Status, "accuracy", finalAccuracy)
	} else {
		logger.Sugar().Error("Could not find the training request to update status.")
	}
	trainingStateMutex.Unlock()

	// Release the active job lock
	activeJobLock.Lock()
	activeJobID = ""
	activeJobLock.Unlock()

	// Marshal and return
	responseJson, err := json.MarshalIndent(new_response, "", "    ")
	if err != nil {
		logger.Sugar().Errorf("Error marshalling training results: %v", err)
		return []byte{}
	}

	logger.Sugar().Infow("Training results ready", "request_id", requestID,
		"failed", trainingFailed, "result_bytes", len(responseJson))
	return cleanupAndMarshalResponse(response) // note this is not the same as responseJson
}

// #region vflTrainModelRequest - Helpers

func sendRequest(phase *phaseRecorder, participant, endpoint string, dataRequest map[string]any) (*pb.MicroserviceCommunication, error) {
	dataRequestJson, err := json.Marshal(dataRequest)
	if err != nil {
		logger.Sugar().Errorf("Error marshalling combined data: %v", err)
		return nil, err
	}

	responseData, err := phase.send(participant, endpoint, dataRequestJson)
	if err != nil {
		return nil, err
	}

	responseObj := &pb.MicroserviceCommunication{}
	if err := json.Unmarshal([]byte(responseData), responseObj); err != nil {
		return nil, fmt.Errorf("Error unmarshalling response: %w", err)
	}

	return responseObj, nil
}

// #endregion

// Use the data request that was previously built and send it to the authorised providers
// acquired from the request approval
func sendDataToAuthProviders(dataRequest []byte, authorizedProviders map[string]string, msgType string, jobId string) []byte {
	// Setup the wait group for async data requests
	var wg sync.WaitGroup
	var responses []string

	// This will be replaced with AMQ in the future
	agentPort := "8080"
	// Iterate over each auth provider
	for auth, url := range authorizedProviders {
		wg.Add(1)
		target := strings.ToLower(auth)
		// Construct the end point
		endpoint := fmt.Sprintf("http://%s:%s/agent/v1/%s/%s", url, agentPort, msgType, target)

		logger.Sugar().Infow("Sending provider request", "target", target, "endpoint", endpoint, "request_bytes", len(dataRequest))

		// Async call send the data
		go func() {
			respData, err := sendData(endpoint, dataRequest)
			if err != nil {
				logger.Sugar().Errorf("Error sending data, %v", err)
			}
			responses = append(responses, respData)
			// Signal that the data request has been sent to all auth providers
			wg.Done()
		}()
	}

	// Wait until all the requests are complete
	wg.Wait()
	logger.Sugar().Debug("Returning responses")

	responseMap := map[string]any{
		"jobId":     jobId,
		"responses": responses,
	}

	// jsonResponse, _ := json.Marshal(responseMap)
	// return jsonResponse
	return cleanupAndMarshalResponse(responseMap)
}

// Now assumes input is map[string]interface{} and directly marshals it to prettified JSON.
func cleanupAndMarshalResponse(responseMap map[string]any) []byte {
	prettifiedJSON, err := json.MarshalIndent(responseMap, "", "    ")
	if err != nil {
		logger.Sugar().Errorf("Error marshalling cleaned response: %v", err)
	}
	return prettifiedJSON
}

func sendData(endpoint string, jsonData []byte) (string, error) {
	// FIXME: Change to an actual token in the future?
	headers := map[string]string{
		"Authorization": "bearer 1234",
	}
	body, err := api.PostRequest(endpoint, string(jsonData), headers)
	if err != nil {
		logger.Sugar().Errorw("Agent HTTP request failed", "endpoint", endpoint, "request_bytes", len(jsonData), "error", err)
		return "", err
	}

	return string(body), nil
}

func availableProvidersHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger.Debug("Starting requestApprovalHandler")
		var availableProviders = make(map[string]lib.AgentDetails)
		resp, err := getAvailableProviders()
		if err != nil {
			logger.Sugar().Errorf("Error getting available providers: %v", err)
			return
		}

		// Bind resp to availableProviders
		availableProviders = resp

		jsonResponse, err := json.Marshal(availableProviders)
		if err != nil {
			logger.Sugar().Errorf("Error marshalling result, %v", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write(jsonResponse)
	}
}

// Maybe this should be moved into the orchestrarot
func getAvailableProviders() (map[string]lib.AgentDetails, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Get the value from etcd.
	resp, err := etcdClient.Get(ctx, "/agents/online", clientv3.WithPrefix())
	if err != nil {
		logger.Sugar().Errorf("failed to get value from etcd: %v", err)
		return nil, err
	}

	// Initialize an empty map to store the unmarshaled structs.
	result := make(map[string]lib.AgentDetails)
	// Iterate through the key-value pairs and unmarshal the values into structs.
	for _, kv := range resp.Kvs {
		var target lib.AgentDetails
		err = json.Unmarshal(kv.Value, &target)
		if err != nil {
			// return nil, fmt.Errorf("failed to unmarshal JSON for key %s: %v", key, err)
		}
		result[string(target.Name)] = target
	}

	return result, nil

}
