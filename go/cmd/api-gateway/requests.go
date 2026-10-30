// This file contains the handlers for the requests that the API Gateway receives from the client
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	"slices"

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

// Provider names
const (
	Authority  = "authority"
	Aggregator = "aggregator"
	Server     = "server"
)

var (
	activeJobID       string
	activeJobLock     sync.Mutex   // to allow only 1 active job at any time
	trainingRequests  = sync.Map{} // map[string]TrainingRequestData
	formattedEndpoint = "http://%s:8080/agent/v1/vflTrainRequest/%s"
	cyclesCompleted   int64
	clientsMutex      = &sync.Mutex{}
	trainingDataMutex sync.Mutex
	excludedClients   = []string{Authority, Aggregator}
	policyUpdateDone  = map[string]<-chan struct{}{}
)

// #region TrainingRequestData helpers

type TrainingRequestData struct {
	Status   string
	Results  []map[string]any
	Metadata map[string]any
	// add more fields as needed
}

func getTrainingRequest(jobId string) (TrainingRequestData, bool) {
	trainingDataMutex.Lock()
	defer trainingDataMutex.Unlock()
	v, ok := trainingRequests.Load(jobId)
	if !ok {
		logger.Sugar().Debug("Not found training request: ", jobId)
		return TrainingRequestData{}, false
	}

	data := v.(TrainingRequestData)
	data.Results = append([]map[string]any{}, data.Results...)
	return data, true
}

func addAndUpdateTrainingRequest(jobId string, cycle int64, clientsNumber int, accuracy float64, timing map[string]any, clients []ClientData) {
	result := map[string]any{
		"timestamp":    time.Now().Format(time.RFC3339),
		"train_round":  cycle,
		"accuracy":     accuracy,
		"clients":      clientsNumber,
		"timing":       timing,
		"participants": clients,
	}

	reqData, _ := getTrainingRequest(jobId)

	if reqData.Results == nil {
		reqData.Results = make([]map[string]any, 0)
	}

	reqData.Results = append(reqData.Results, result)
	trainingDataMutex.Lock()
	trainingRequests.Store(jobId, reqData)
	trainingDataMutex.Unlock()
}

// #endregion

func getTrainingStatusHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger.Sugar().Info("Starting getTrainingStatusHandler")
		requestID := r.URL.Query().Get("id")
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
		respBytes, err := json.MarshalIndent(resp, "", "    ")
		if err != nil {
			logger.Sugar().Errorw("Could not encode training status", "request_id", requestID, "error", err)
			http.Error(w, "Could not encode training status", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
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
		started := false
		defer func() {
			if !started {
				activeJobID = ""
				failTrainingApproval(requestID, fmt.Errorf("invalid training request"), 0)
			}
		}()

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
			http.Error(w, "Invalid request approval", http.StatusBadRequest)
			return
		}

		userPb := &pb.User{
			Id:       apiReqApproval.User.Id,
			UserName: apiReqApproval.User.UserName,
		}

		var dataRequestInterface map[string]any
		if err := json.Unmarshal(apiReqApproval.DataRequest, &dataRequestInterface); err != nil {
			logger.Sugar().Errorf("Error unmarhsalling get request: %v", err)
			http.Error(w, "Invalid data request", http.StatusBadRequest)
			return
		}
		if dataRequestInterface == nil {
			logger.Sugar().Warn("Data request must be a JSON object")
			http.Error(w, "Data request must be a JSON object", http.StatusBadRequest)
			return
		}

		dataRequestOptions := &api.DataRequestOptions{}
		dataRequestOptions.Options = make(map[string]bool)
		if err := json.Unmarshal(apiReqApproval.DataRequest, &dataRequestOptions); err != nil {
			logger.Sugar().Errorf("Error unmMarshalling get apiReqApproval: %v", err)
			http.Error(w, "Invalid request options", http.StatusBadRequest)
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
		started = true
		go func() {
			startTraining(protoRequest, dataRequestInterface, apiReqApproval, r, requestID)
		}()

	}
}

func startTraining(protoRequest *pb.RequestApproval, dataRequestInterface map[string]any, apiReqApproval api.RequestApproval, r *http.Request, requestID string) {
	approvalStart := time.Now()
	defer func() {
		activeJobLock.Lock()
		if activeJobID == requestID {
			activeJobID = ""
		}
		activeJobLock.Unlock()
	}()
	logger.Debug("Starting training process...")
	// Requests may take up to 10 minutes now
	ctxWithTimeout, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Start a new span with the context that has a timeout
	ctx, span := trace.StartSpan(ctxWithTimeout, "requestApprovalHandler")
	defer span.End()

	// Create a channel to receive the response
	responseChan := make(chan validation, 1)

	requestApprovalMutex.Lock()
	requestApprovalMap[protoRequest.User.Id] = responseChan
	requestApprovalMutex.Unlock()
	defer func() {
		requestApprovalMutex.Lock()
		delete(requestApprovalMap, protoRequest.User.Id)
		requestApprovalMutex.Unlock()
	}()

	_, err := c.SendRequestApproval(ctx, protoRequest)
	if err != nil {
		logger.Sugar().Errorf("error in sending requestapproval: %v", err)
		failTrainingApproval(requestID, err, time.Since(approvalStart))
		return
	}

	select {
	case validationStruct := <-responseChan:
		msg := validationStruct.response

		logger.Sugar().Infof("Received response, %s", msg.Type)
		if msg.Type != "requestApprovalResponse" {
			logger.Sugar().Errorf("Unexpected message received, type: %s", msg.Type)
			failTrainingApproval(requestID, fmt.Errorf("unexpected approval response: %s", msg.Type), time.Since(approvalStart))
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
		failTrainingApproval(requestID, ctx.Err(), time.Since(approvalStart))
		return
	}

}

func cloneDataRequest(dataRequest map[string]any, requestType string, data map[string]any) map[string]any {
	request := maps.Clone(dataRequest)
	request["type"] = requestType
	request["data"] = data
	return request
}

func cloneAndSendDataRequest(dataRequest map[string]any, authorizedProviders map[string]string, auth string, requestType string, data map[string]any, timing *executionTiming) (*pb.MicroserviceCommunication, error) {
	_, endpoint := findAuthorizedProvider(authorizedProviders, auth)

	logger.Sugar().Debug("[", auth, "] [", requestType, "] Sending request...")
	request := cloneDataRequest(dataRequest, requestType, data)

	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	stage := requestType
	if requestType == "vflInitializeRequest" {
		stage = "parties_initialize"
		if strings.EqualFold(auth, Authority) || strings.EqualFold(auth, Aggregator) {
			stage = strings.ToLower(auth) + "_initialize"
		}
	}
	if endpoint == "" {
		return nil, fmt.Errorf("no authorized endpoint for %s", auth)
	}
	response, err := timing.send(stage, auth, endpoint, body)
	if err != nil {
		return nil, err
	}
	responseData := &pb.MicroserviceCommunication{}
	if err := json.Unmarshal([]byte(response), responseData); err != nil {
		timing.failReply(stage, auth, err)
		return nil, fmt.Errorf("decode %s response from %s: %w", requestType, auth, err)
	}
	if responseData.Data == nil {
		err := fmt.Errorf("missing data in %s response from %s", requestType, auth)
		timing.failReply(stage, auth, err)
		return nil, err
	}
	if err := validateTrainingReply(requestType, auth, responseData); err != nil {
		timing.failReply(stage, auth, err)
		logger.Sugar().Errorw("Invalid training reply", "participant", auth, "stage", stage, "error", err)
		return nil, err
	}

	if err != nil {
		logger.Sugar().Errorf("Error sending data, %v", err)
	} else {
		keys := make(map[string]string)

		for k, v := range responseData.Data.GetFields() {
			keys[k] = fmt.Sprintf("%T", v.Kind)
		}

		logger.Sugar().Debug("[", auth, "] [", requestType, "] Response: ", keys)
	}

	return responseData, err
}

func getSafeClients(clients *[]ClientData) []ClientData {
	clientsMutex.Lock()
	currentClients := make([]ClientData, len(*clients))
	copy(currentClients, *clients)
	clientsMutex.Unlock()

	return currentClients
}

func findAuthorizedProvider(authorizedProviders map[string]string, targetedAuth string) (string, string) {
	loweredTargetedAuth := strings.ToLower(targetedAuth)

	for auth, url := range authorizedProviders {
		target := strings.ToLower(auth)

		if target == loweredTargetedAuth {
			return auth, fmt.Sprintf(formattedEndpoint, url, target)
		}
	}

	return "", ""
}

type ClientFeature struct {
	PartyId      string `json:"party_id"`
	FeaturesSize any    `json:"features_size"`
}

func initializeVFLServices(dataRequest map[string]any, clients []ClientData, weights *[]any, authorizedProviders map[string]string, sampleBatchSize int64, timing *executionTiming) error {

	// Initialize authority
	responseData, err := cloneAndSendDataRequest(dataRequest, authorizedProviders,
		Authority,
		"vflInitializeRequest",
		map[string]any{
			"parties_size": len(clients),
			"batch_size":   sampleBatchSize,
		},
		timing,
	)

	if err != nil {
		return err
	}

	encryptionKeys := responseData.Data.GetFields()["mife_encryption_keys"].GetListValue().GetValues()
	sifePublicKey := responseData.Data.GetFields()["sife_public_key"].GetStringValue()
	mifePublicKey := responseData.Data.GetFields()["mife_public_key"].GetStringValue()

	// Initialize parties
	var wg sync.WaitGroup
	var mtx sync.Mutex
	featuresSize := map[string]any{}
	errors := make(chan error, len(clients))
	if len(encryptionKeys) != len(clients) {
		err := fmt.Errorf("authority returned %d encryption keys for %d parties", len(encryptionKeys), len(clients))
		timing.failReply("authority_initialize", Authority, err)
		return err
	}

	for index, client := range clients {
		wg.Add(1)

		go func() {
			defer wg.Done()

			responseData, err := cloneAndSendDataRequest(dataRequest, authorizedProviders,
				client.Auth,
				"vflInitializeRequest",
				map[string]any{
					"mife_encryption_key": encryptionKeys[index],
					"sife_public_key":     sifePublicKey,
				},
				timing,
			)

			if err != nil {
				errors <- err
				return
			}
			v := responseData.Data.GetFields()["features_size"].GetNumberValue()
			if v != 0 {
				mtx.Lock()
				featuresSize[client.Auth] = v
				mtx.Unlock()
			}
		}()
	}

	wg.Wait()
	close(errors)
	for err := range errors {
		return err
	}

	orderedClientFeatures := []ClientFeature{}

	logger.Sugar().Debug("Clients: ", clients)

	for _, client := range clients {
		if size, exists := featuresSize[client.Auth]; exists {
			orderedClientFeatures = append(orderedClientFeatures, ClientFeature{
				PartyId:      client.Auth,
				FeaturesSize: size,
			})
		}
	}

	// Initialize aggregator
	responseData, err = cloneAndSendDataRequest(dataRequest, authorizedProviders,
		Aggregator,
		"vflInitializeRequest",
		map[string]any{
			"parties_size":     len(clients),
			"batch_size":       sampleBatchSize,
			"mife_public_key":  mifePublicKey,
			"sife_public_key":  sifePublicKey,
			"parties_features": orderedClientFeatures,
		},
		timing,
	)

	if err != nil {
		return err
	}

	*weights = ToAnyList(responseData.Data.GetFields()["weights"].GetListValue().GetValues())
	if len(*weights) != len(orderedClientFeatures) {
		err := fmt.Errorf("aggregator returned %d weights for %d feature parties", len(*weights), len(orderedClientFeatures))
		timing.failReply("aggregator_initialize", Aggregator, err)
		return err
	}

	return nil
}

func ToAnyList[T any](input []T) []any {
	result := make([]any, len(input))
	for i, v := range input {
		result[i] = v
	}
	return result
}

func runVFLTrainingRound(dataRequest map[string]any, clients []ClientData, weights *[]any, authorizedProviders map[string]string, sampleBatchSize int64, timing *executionTiming) (float64, error) {

	responseData, err := cloneAndSendDataRequest(dataRequest, authorizedProviders,
		Server,
		"vflSampleBatchRequest",
		map[string]any{
			"sample_batch_size": sampleBatchSize,
		},
		timing,
	)

	if err != nil {
		return 0., err
	}

	sampleBatchIndexes := responseData.Data.GetFields()["sample_batch_indexes"].GetStringValue()

	var wg sync.WaitGroup

	featureDimension := map[string]any{}
	sampleDimension := map[string]any{}
	var dimensionsMutex sync.Mutex
	errors := make(chan error, len(clients))

	index := 0

	for _, client := range clients {
		wg.Add(1)

		weightIndex := -1
		// Server holds only labels. No weights.
		if strings.ToLower(client.Auth) != Server {
			weightIndex = index
			index++
		}
		if weightIndex >= len(*weights) {
			// Join already-dispatched requests before returning a failed round.
			wg.Done()
			wg.Wait()
			return 0, fmt.Errorf("missing weight %d for party %s", weightIndex, client.Auth)
		}

		go func(idx int) {
			defer wg.Done()

			data := map[string]any{
				"sample_batch_indexes": sampleBatchIndexes,
			}

			if weightIndex != -1 {
				data["weights"] = (*weights)[idx]
			}

			responseData, err := cloneAndSendDataRequest(dataRequest, authorizedProviders,
				client.Auth,
				"vflExtractCiphertextsRequest",
				data,
				timing,
			)

			if err != nil {
				errors <- err
				return
			}

			dimensionsMutex.Lock()
			defer dimensionsMutex.Unlock()
			featureDimension[client.Auth] = responseData.Data.GetFields()["feature_dimension"].GetStringValue()
			sampleDimensionField, exists := responseData.Data.GetFields()["sample_dimension"]

			if exists {
				sampleDimension[client.Auth] = sampleDimensionField.GetListValue().GetValues()
			}

		}(weightIndex)
	}

	wg.Wait()
	close(errors)
	for err := range errors {
		return 0, err
	}

	orderedFeatureDimension := []any{}
	orderedSampleDimension := []any{}
	orderedActiveClients := []any{}

	for _, client := range clients {
		orderedFeatureDimension = append(orderedFeatureDimension, featureDimension[client.Auth])

		if sd, exists := sampleDimension[client.Auth]; exists {
			orderedSampleDimension = append(orderedSampleDimension, sd)
		}

		orderedActiveClients = append(orderedActiveClients, map[bool]int{true: 1, false: 0}[client.Active])
	}

	responseData, err = cloneAndSendDataRequest(dataRequest, authorizedProviders,
		Authority,
		"vflMIFEDKGenerationRequest",
		map[string]any{
			"active_parties": orderedActiveClients,
		},
		timing,
	)

	if err != nil {
		return 0, err
	}
	dkFeaturesMife := responseData.Data.GetFields()["dks_features_mife"].GetListValue().GetValues()

	responseData, err = cloneAndSendDataRequest(dataRequest, authorizedProviders,
		Aggregator,
		"vflFeaturesDecryptionRequest",
		map[string]any{
			"dks_features_mife":            dkFeaturesMife,
			"encrypted_features_dimension": orderedFeatureDimension,
		},
		timing,
	)

	if err != nil {
		return 0., err
	}

	decryptedFeaturesDimension := responseData.Data.GetFields()["decrypted_features_dimension"].GetListValue().GetValues()

	responseData, err = cloneAndSendDataRequest(dataRequest, authorizedProviders,
		Server,
		"vflCalculateAccuracyRequest",
		map[string]any{
			"decrypted_features_dimension": decryptedFeaturesDimension,
		},
		timing,
	)

	if err != nil {
		return 0., err
	}

	accuracy := responseData.Data.GetFields()["batch_accuracy"].GetNumberValue()
	logisticError := responseData.Data.GetFields()["logistic_error"].GetListValue().GetValues()

	responseData, err = cloneAndSendDataRequest(dataRequest, authorizedProviders,
		Authority,
		"vflSIFEDKGenerationRequest",
		map[string]any{
			"logistic_error": logisticError,
		},
		timing,
	)

	if err != nil {
		return 0., err
	}

	dkSamplesDimension := responseData.Data.GetFields()["dk_samples_sife"].GetStringValue()

	responseData, err = cloneAndSendDataRequest(dataRequest, authorizedProviders,
		Aggregator,
		"vflSamplesDecryptionRequest",
		map[string]any{
			"dk_samples_sife":             dkSamplesDimension,
			"encrypted_samples_dimension": orderedSampleDimension,
		},
		timing,
	)

	if err != nil {
		return 0, err
	}
	nextWeights := ToAnyList(responseData.Data.GetFields()["weights"].GetListValue().GetValues())
	if len(nextWeights) != len(*weights) {
		err := fmt.Errorf("aggregator returned %d weights; expected %d", len(nextWeights), len(*weights))
		timing.failReply("vflSamplesDecryptionRequest", Aggregator, err)
		return 0, err
	}
	*weights = nextWeights

	return accuracy, nil
}

// #region VFL requests

// #endregion

func extractValueOrDefault[T ~int64 | ~float64](data map[string]any, propertyName string, defaultValue T) T {
	value, ok := data[propertyName].(float64)
	if ok {
		return T(value)
	}

	logger.Sugar().Debug("Property [", propertyName, "] wasn't found on the request parameters. Default value [", defaultValue, "] is going to be used instead.")
	return defaultValue
}

func checkPolicyUpdate(ctx context.Context, clients *[]ClientData, user *pb.User, policyChanged *bool) {
	policyUpdateChan := make(chan PolicyUpdateResponse)

	// There is a misalignment because there are static User.Ids in different parts of the code.
	// This has to be changed in the future.
	logger.Sugar().Debug("Updating policyUpdateMap with key: ", user.Id)
	policyUpdateMutex.Lock()
	policyUpdateMap[user.Id] = policyUpdateChan
	policyUpdateDone[user.Id] = ctx.Done()
	policyUpdateMutex.Unlock()

	go func() {

		defer func() {
			policyUpdateMutex.Lock()
			if policyUpdateMap[user.Id] == policyUpdateChan {
				delete(policyUpdateMap, user.Id)
				delete(policyUpdateDone, user.Id)
			}
			policyUpdateMutex.Unlock()
		}()
		for {
			var policyUpdateResponse PolicyUpdateResponse
			select {
			case <-ctx.Done():
				return
			case policyUpdateResponse = <-policyUpdateChan:
			}
			availableProviders, err := getAvailableProviders()

			if err != nil {
				logger.Sugar().Errorf("A problem occurred while fetching providers: %v", err)
				continue
			}

			validDataproviders := policyUpdateResponse.GetValidDataproviders()

			clientsMutex.Lock()
			existingClients := make(map[string]bool)

			for i, client := range *clients {
				existingClients[client.Auth] = true

				_, isValid := validDataproviders[client.Auth]

				if isValid {
					// Policy reintroduced
					if !client.Active {
						(*clients)[i].Active = true
						logger.Sugar().Debug("[", client.Auth, "] [>!<PolicyChange>!<] Policy reintroduced.")
					}
				} else {
					// Policy removed
					if client.Active {
						(*clients)[i].Active = false
						logger.Sugar().Debug("[", client.Auth, "] [>!<PolicyChange>!<] Policy removed.")
					}
				}
			}

			for auth, agentDetail := range availableProviders {
				if shouldExcludeClient(strings.ToLower(auth)) {
					continue
				}

				_, isValid := validDataproviders[auth]

				if isValid && !existingClients[auth] {
					*clients = append(*clients, ClientData{
						Auth:   auth,
						Url:    agentDetail.Dns,
						Active: true,
					})
					*policyChanged = true
					logger.Sugar().Debug("[", auth, "] [>!<PolicyChange>!<] Policy added.")
				}
			}

			clientsMutex.Unlock()
		}
	}()
}

type ClientData struct {
	Auth   string
	Url    string
	Active bool
}

func shouldExcludeClient(auth string) bool {
	return slices.Contains(excludedClients, auth)
}

func runVFLTraining(dataRequest map[string]any, authorizedProviders map[string]string, jobId string, ctx context.Context, requestID string, approval time.Duration) []byte {
	lifecycleStart := time.Now()
	runTimer := &executionTiming{start: lifecycleStart}
	ctx, stopPolicy := context.WithCancel(ctx)
	defer stopPolicy()
	warmup := newExecutionTiming()
	initialization := map[string]any{}
	roundTimings := []map[string]any{}
	policyActions := []map[string]any{}
	runErrors := []string{}
	clients := &[]ClientData{}
	var policyChanged bool
	var finalAccuracy float64
	var wg sync.WaitGroup
	var weights = []any{}

	// Default parameters values
	var sampleBatchSize int64 = 64
	var cycles int64 = 10
	var learningRate float64 = 0.05
	var policy_removal int64 = -1
	var policy_reintroduction int64 = -1
	var dataProviders []string = []string{}

	var trainingBacktrack int64 = 0 // Default value

	data, ok := dataRequest["data"].(map[string]any)
	logger.Sugar().Info("Data from req: ", data)

	if ok {
		// Parameters extraction
		sampleBatchSize = extractValueOrDefault(data, "sample_batch_size", sampleBatchSize)
		cycles = extractValueOrDefault(data, "cycles", cycles)
		learningRate = extractValueOrDefault(data, "learning_rate", learningRate)
		trainingBacktrack = extractValueOrDefault(data, "training_backtrack", trainingBacktrack)
		policy_removal = extractValueOrDefault(data, "policy_removal", policy_removal)
		policy_reintroduction = extractValueOrDefault(data, "policy_reintroduction", policy_reintroduction)
	}

	metadata := map[string]any{
		"strategy":                "fed-encrypt-dynamic",
		"sample_batch_size":       sampleBatchSize,
		"requested_learning_rate": learningRate,
		"policy_mode":             "event-driven",
		"total_rounds":            cycles,
		"policy_removal":          policy_removal,
		"training_backtrack":      trainingBacktrack,
		"policy_reintroduction":   policy_reintroduction,
	}

	// logger.Sugar().Debug("metadata: ", metadata)

	trainingFailed := false

	for auth, url := range authorizedProviders {
		lower := strings.ToLower(auth)

		if !shouldExcludeClient(lower) && url != "" {
			*clients = append(*clients, ClientData{Auth: auth, Url: url, Active: true})
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

	pingErrors := make(chan error, len(authorizedProviders))

	for auth, url := range authorizedProviders {
		wg.Add(1)
		target := strings.ToLower(auth)
		endpoint := fmt.Sprintf(formattedEndpoint, url, target)

		go func() {
			defer wg.Done()
			var pingErr error
			// TODO: Repeat ping until no error, after 5 tries, cancel request
			for i := range 10 {
				logger.Sugar().Info("Sending ping to: ", target, " (", endpoint, "). Attempt [", i+1, "/10]")
				_, err := warmup.send("warmup", auth, endpoint, dataRequestJson)
				pingErr = err

				if err == nil {
					logger.Sugar().Info("Response OK from: ", target)
					break
				}

				logger.Sugar().Info("No response from: ", target)

			}
			if pingErr != nil {
				pingErrors <- fmt.Errorf("warmup %s: %w", auth, pingErr)
			}
		}()
	}

	wg.Wait()
	warmupSummary := warmup.summary()
	runTimer.merge("warmup", warmup)
	close(pingErrors)
	for err := range pingErrors {
		trainingFailed = true
		runErrors = append(runErrors, err.Error())
	}

	// Checks if policy changes (from incoming messages)
	checkPolicyUpdate(ctx, clients, user, &policyChanged)

	if !trainingFailed {
		timer := newExecutionTiming()
		err := initializeVFLServices(dataRequest, getSafeClients(clients), &weights, authorizedProviders, sampleBatchSize, timer)
		initialization = timer.summary()
		runTimer.merge("initialization", timer)
		if err != nil {
			trainingFailed = true
			runErrors = append(runErrors, "initialization: "+err.Error())
		}
	}

	logger.Sugar().Info("Running VFL for ", cycles, " rounds")
	trainingStart := time.Now()
	for cycle := range cycles {
		if trainingFailed {
			break
		}
		roundStart := time.Now()
		var reinitialization map[string]any
		logger.Sugar().Info("Running VFL training round ", cycle)

		clientsMutex.Lock()
		currentClients := append([]ClientData{}, (*clients)...)
		changed := policyChanged
		policyChanged = false
		clientsMutex.Unlock()

		if changed {
			// Aggreement has been added. Re-initilization is mandatory.
			timer := newExecutionTiming()
			err := initializeVFLServices(dataRequest, currentClients, &weights, authorizedProviders, sampleBatchSize, timer)
			reinitialization = timer.summary()
			runTimer.merge(fmt.Sprintf("round/%d/reinitialization", cycle), timer)
			if err != nil {
				trainingFailed = true
				runErrors = append(runErrors, "reinitialization: "+err.Error())
				roundTimings = append(roundTimings, map[string]any{"train_round": cycle, "reinitialization": reinitialization, "round_ms": toMs(time.Since(roundStart)), "failed": true})
				break
			}
		}

		// TODO: Implement policy change request
		if policy_removal == cycle {
			logger.Sugar().Info("Sending in the policy change request, removing client 3 from the agreement.")
			logger.Sugar().Info("TODO: Policy change request not yet implemented.")

			start := time.Now()
			_, policyErr := api.DeleteRequest(
				"http://orchestrator.orchestrator.svc.cluster.local:8080/api/v1/policyEnforcer/agreements/clientthree",
				"",
				nil)
			policyActions = append(policyActions, policyActionTiming(cycle, "removal", start, policyErr))
			if policyErr != nil {
				trainingFailed = true
				runErrors = append(runErrors, "policy removal: "+policyErr.Error())
				break
			}
		}

		// TODO: Implement policy change request
		if policy_reintroduction == cycle {
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

			start := time.Now()
			_, policyErr := api.PutRequest(
				"http://orchestrator.orchestrator.svc.cluster.local:8080/api/v1/policyEnforcer",
				rawPayload,
				nil)
			policyActions = append(policyActions, policyActionTiming(cycle, "reintroduction", start, policyErr))
			if policyErr != nil {
				trainingFailed = true
				runErrors = append(runErrors, "policy reintroduction: "+policyErr.Error())
				break
			}
		}

		timer := newExecutionTiming()
		accuracy, err := runVFLTrainingRound(dataRequest, currentClients, &weights, authorizedProviders, sampleBatchSize, timer)
		roundSummary := timer.summary()
		runTimer.merge(fmt.Sprintf("round/%d", cycle), timer)
		roundSummary["train_round"], roundSummary["round_ms"], roundSummary["failed"] = cycle, toMs(time.Since(roundStart)), err != nil
		if reinitialization != nil {
			roundSummary["reinitialization"] = reinitialization
		}
		roundTimings = append(roundTimings, roundSummary)

		finalAccuracy = accuracy

		if err != nil {
			logger.Sugar().Error("Training round returned an error.")
			trainingFailed = true
			runErrors = append(runErrors, fmt.Sprintf("round %d: %v", cycle, err))
			break
		}

		activeClientsCount := 0

		for _, client := range currentClients {
			if client.Active {
				activeClientsCount++
			}
		}

		addAndUpdateTrainingRequest(requestID, cycle, activeClientsCount, accuracy, roundSummary, currentClients)

		if trainingFailed {
			break
		}
	}
	trainingDuration := time.Since(trainingStart)

	logger.Sugar().Info("-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-")
	logger.Sugar().Info("Final accuracy achieved: ", finalAccuracy)
	logger.Sugar().Info("-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-")

	shutdown := newExecutionTiming()
	shutdownErrors := make(chan error, len(authorizedProviders))
	dataRequest["type"] = "vflShutdownRequest"

	dataRequestJson, err = json.Marshal(dataRequest)
	if err != nil {
		logger.Sugar().Errorf("Error marshalling combined data: %v", err)
		return []byte{}
	}

	for auth, url := range authorizedProviders {
		wg.Add(1)
		target := strings.ToLower(auth)
		endpoint := fmt.Sprintf(formattedEndpoint, url, target)

		go func() {
			defer wg.Done()
			_, err := shutdown.send("shutdown", auth, endpoint, dataRequestJson)
			if err != nil {
				shutdownErrors <- fmt.Errorf("shutdown %s: %w", auth, err)
			}
		}()
	}

	wg.Wait()
	close(shutdownErrors)
	for err := range shutdownErrors {
		trainingFailed = true
		runErrors = append(runErrors, err.Error())
	}
	shutdownSummary := shutdown.summary()
	runTimer.merge("shutdown", shutdown)
	runSummary := runTimer.summary()
	runSummary["approval_ms"], runSummary["total_ms"] = toMs(approval), toMs(approval+time.Since(lifecycleStart))
	runSummary["warmup"], runSummary["initialization"], runSummary["shutdown"] = warmupSummary, initialization, shutdownSummary
	runSummary["training_ms"], runSummary["rounds"], runSummary["policy_actions"] = toMs(trainingDuration), roundTimings, policyActions
	runSummary["background_completion_verified"] = false
	metadata["timing"] = runSummary
	metadata["attempted_rounds"] = len(roundTimings)
	metadata["errors"] = runErrors

	response := map[string]any{
		"jobId":    jobId,
		"accuracy": finalAccuracy,
	}

	logger.Sugar().Info("-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-")
	logger.Sugar().Info("Final accuracy achieved: ", finalAccuracy)
	logger.Sugar().Info("-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-=-")

	results, _ := getTrainingRequest(requestID)
	metadata["completed_rounds"] = len(results.Results)

	new_response := map[string]any{
		"metadata": metadata,
		"results":  results.Results,
	}

	// --- SET STATUS and UNLOCK ---
	v, ok := trainingRequests.Load(requestID)
	if ok {
		trainingDataMutex.Lock()
		reqData := v.(TrainingRequestData)
		reqData.Metadata = metadata
		if trainingFailed {
			reqData.Status = StatusFailed
		} else {
			reqData.Status = StatusDone
		}
		trainingRequests.Store(requestID, reqData)
		trainingDataMutex.Unlock()
		logger.Sugar().Infow("Job completed", "requestID", requestID, "status", reqData.Status, "accuracy", finalAccuracy)
	} else {
		logger.Sugar().Error("Could not find the training request to update status.")
	}

	// Marshal and return
	responseJson, err := json.MarshalIndent(new_response, "", "    ")
	if err != nil {
		logger.Sugar().Errorf("Error marshalling training results: %v", err)
		return []byte{}
	}

	logger.Sugar().Infow("Training results prepared", "request_id", requestID, "response_bytes", len(responseJson))
	return cleanupAndMarshalResponse(response) // note this is not the same as responseJson
}

// #region vflTrainModelRequest - Helpers

func sendRequest(endpoint string, dataRequest map[string]any) (*pb.MicroserviceCommunication, error) {
	dataRequestJson, err := json.Marshal(dataRequest)
	if err != nil {
		logger.Sugar().Errorf("Error marshalling combined data: %v", err)
		return nil, err
	}

	responseData, err := sendData(endpoint, dataRequestJson)
	if err != nil {
		logger.Sugar().Errorf("Error sending data to the server, %v", err)
	}

	serverResponse := &pb.MicroserviceCommunication{}
	err = json.Unmarshal([]byte(responseData), serverResponse)

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

		logger.Sugar().Infof("Sending request to %s.\nEndpoint: %s\nJSON:%v", target, endpoint, string(dataRequest))

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
