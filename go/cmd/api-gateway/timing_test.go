package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Jorrit05/DYNAMOS/pkg/lib"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestHandlerTimingValidation(t *testing.T) {
	for _, raw := range []string{"-1", "NaN", "Inf", "1001", "invalid"} {
		body := fmt.Sprintf(`{"metadata":{"%s":%q}}`, lib.AlgorithmTimingMetadataKey, raw)
		if _, err := responseAlgorithmTiming(body, time.Second); err == nil {
			t.Errorf("accepted invalid timing %s", raw)
		}
	}
	if value, err := responseAlgorithmTiming(`{}`, time.Second); err != nil || value != nil {
		t.Fatal("missing timing must remain unmeasured", value, err)
	}
	body := fmt.Sprintf(`{"metadata":{"%s":"0"}}`, lib.AlgorithmTimingMetadataKey)
	if value, err := responseAlgorithmTiming(body, time.Second); err != nil || value == nil || *value != 0 {
		t.Fatal("explicit zero should be measured", value, err)
	}
}

func TestTimingOverlapAccounting(t *testing.T) {
	start := time.Now().Add(-20 * time.Millisecond)
	algorithm := 2.0
	timer := &executionTiming{start: start, calls: []timedCall{
		{Stage: "parallel", Participant: "one", start: start, end: start.Add(10 * time.Millisecond), DurationMS: 10, AlgorithmMS: &algorithm},
		{Stage: "parallel", Participant: "two", start: start.Add(time.Millisecond), end: start.Add(6 * time.Millisecond), DurationMS: 5},
		{Stage: "next", Participant: "one", start: start.Add(8 * time.Millisecond), end: start.Add(15 * time.Millisecond), DurationMS: 7, Failed: true},
	}}
	summary := timer.summary()
	for field, want := range map[string]float64{"stage_elapsed_sum_ms": 17, "stage_union_ms": 15, "stage_overlap_ms": 2, "reply_barrier_wait_ms": 4} {
		if got := summary[field].(float64); math.Abs(got-want) > 0.001 {
			t.Errorf("%s = %v, want %v", field, got, want)
		}
	}
	if summary["measured_calls"] != 1 || summary["unmeasured_calls"] != 2 {
		t.Fatal("incorrect coverage", summary)
	}
	one := summary["participants"].(map[string]any)["one"].(map[string]any)
	if one["request_active_ms"] != 15.0 {
		t.Fatal("overlapping participant requests were summed", one)
	}
}

func encryptedFixture(t *testing.T, failType string, invalidAccuracy bool) map[string]string {
	return encryptedFixtureWithHook(t, failType, invalidAccuracy, nil)
}

func encryptedFixtureWithHook(t *testing.T, failType string, invalidAccuracy bool, hook func(string, string, map[string]any)) map[string]string {
	t.Helper()
	var mu sync.Mutex
	arrivals := 0
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			http.Error(w, "decode", 400)
			return
		}
		participant := strings.TrimPrefix(r.URL.Path, "/")
		if hook != nil {
			hook(participant, request.Type, request.Data)
		}
		if request.Type == failType {
			http.Error(w, "injected failure", 500)
			return
		}
		data := map[string]any{}
		switch request.Type {
		case "vflInitializeRequest":
			switch participant {
			case Authority:
				data = map[string]any{"mife_encryption_keys": []any{"a", "b", "c", "d"}, "mife_public_key": "public", "sife_public_key": "public"}
			case Aggregator:
				data["weights"] = []any{"w1", "w2", "w3"}
			default:
				size := 1
				if participant == Server {
					size = 0
				}
				data["features_size"] = size
			}
		case "vflSampleBatchRequest":
			data["sample_batch_indexes"] = "indexes"
		case "vflExtractCiphertextsRequest":
			mu.Lock()
			group := release
			arrivals++
			if arrivals == 4 {
				close(release)
				release = make(chan struct{})
				arrivals = 0
			}
			mu.Unlock()
			select {
			case <-group:
			case <-time.After(2 * time.Second):
				t.Error("ciphertext requests were serialized")
				http.Error(w, "barrier", 500)
				return
			}
			data["feature_dimension"] = "ciphertext"
			if participant != Server {
				data["sample_dimension"] = []any{"ciphertext"}
			}
		case "vflMIFEDKGenerationRequest":
			data["dks_features_mife"] = []any{"dk"}
		case "vflFeaturesDecryptionRequest":
			data["decrypted_features_dimension"] = []any{1}
		case "vflCalculateAccuracyRequest":
			data["logistic_error"] = []any{1}
			if !invalidAccuracy {
				data["batch_accuracy"] = 0.75
			}
		case "vflSIFEDKGenerationRequest":
			data["dk_samples_sife"] = "dk"
		case "vflSamplesDecryptionRequest":
			data["weights"] = []any{"w1", "w2", "w3"}
		}
		value, err := structpb.NewStruct(data)
		if err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(&pb.MicroserviceCommunication{
			Data: value, Metadata: map[string]string{lib.AlgorithmTimingMetadataKey: "0"},
		})
	}))
	t.Cleanup(server.Close)
	old := formattedEndpoint
	formattedEndpoint = "http://%s/%s"
	t.Cleanup(func() { formattedEndpoint = old })
	providers := map[string]string{}
	for _, name := range []string{Authority, Aggregator, Server, "clientone", "clienttwo", "clientthree"} {
		providers[name] = strings.TrimPrefix(server.URL, "http://")
	}
	return providers
}

func runEncryptedFixture(t *testing.T, providers map[string]string, cycles float64) TrainingRequestData {
	t.Helper()
	id := t.Name()
	trainingRequests.Store(id, TrainingRequestData{Status: StatusPending, Results: []map[string]any{}})
	t.Cleanup(func() { trainingRequests.Delete(id) })
	runVFLTraining(map[string]any{
		"user": &pb.User{Id: id},
		"data": map[string]any{"cycles": cycles, "sample_batch_size": float64(2)},
	}, providers, "job", context.Background(), id, time.Millisecond)
	result, ok := getTrainingRequest(id)
	if !ok {
		t.Fatal("missing result")
	}
	return result
}

func TestEncryptedRunTiming(t *testing.T) {
	providers := encryptedFixture(t, "", false)
	result := runEncryptedFixture(t, providers, 2)
	if result.Status != StatusDone || len(result.Results) != 2 {
		t.Fatal("training failed", result)
	}
	timing := result.Metadata["timing"].(map[string]any)
	// Six warmups + six initialization calls + two ten-call rounds + six shutdowns.
	if timing["attempts"] != 38 || timing["measured_calls"] != 38 || timing["unmeasured_calls"] != 0 {
		t.Fatal("incorrect lifecycle call coverage", timing)
	}
	for _, round := range result.Results {
		metrics := round["timing"].(map[string]any)
		if metrics["attempts"] != 10 || len(metrics["phase_order"].([]string)) != 7 {
			t.Fatal("encrypted stage shape changed", metrics)
		}
		if round["clients"] != 4 || round["accuracy"] != 0.75 {
			t.Fatal("party/label-server semantics changed", round)
		}
	}
	if timing["total_ms"].(float64) < timing["duration_ms"].(float64)+1 {
		t.Fatal("approval missing from lifecycle")
	}
	// Poll the production status endpoint and ensure the enriched JSON is valid.
	recorder := httptest.NewRecorder()
	getTrainingStatusHandler()(recorder, httptest.NewRequest("GET", "/?id="+t.Name(), nil))
	var status map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil || status["status"] != StatusDone {
		t.Fatal("invalid polling result", err, recorder.Body.String())
	}
	if path := os.Getenv("DYNAMOS_TEST_RESULT_PATH"); path != "" {
		if err := os.WriteFile(path, recorder.Body.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEncryptedFailuresAreRetained(t *testing.T) {
	for _, stage := range []string{
		"vflInitializeRequest", "vflSampleBatchRequest", "vflExtractCiphertextsRequest",
		"vflMIFEDKGenerationRequest", "vflFeaturesDecryptionRequest", "vflCalculateAccuracyRequest",
		"vflSIFEDKGenerationRequest", "vflSamplesDecryptionRequest", "vflShutdownRequest", "vflPingRequest",
	} {
		t.Run(stage, func(t *testing.T) {
			result := runEncryptedFixture(t, encryptedFixture(t, stage, false), 1)
			if result.Status != StatusFailed || len(result.Metadata["errors"].([]string)) == 0 {
				t.Fatal("failure became success", result)
			}
			timing := result.Metadata["timing"].(map[string]any)
			if timing["failures"].(int) == 0 || timing["unmeasured_calls"].(int) == 0 {
				t.Fatal("failed calls missing from metrics")
			}
		})
	}
}

func TestMissingAccuracyIsFailure(t *testing.T) {
	result := runEncryptedFixture(t, encryptedFixture(t, "", true), 1)
	if result.Status != StatusFailed || len(result.Results) != 0 {
		t.Fatal("missing accuracy accepted as zero", result)
	}
	metrics := result.Metadata["timing"].(map[string]any)
	if metrics["failures"] != 1 {
		t.Fatal("invalid reply not recorded as failed", metrics)
	}
}

func TestAccuracyUsesWorkerFractionScale(t *testing.T) {
	for _, accuracy := range []float64{0, 0.75, 1, -0.1, 75} {
		data, err := structpb.NewStruct(map[string]any{"batch_accuracy": accuracy, "logistic_error": []any{1}})
		if err != nil {
			t.Fatal(err)
		}
		err = validateTrainingReply("vflCalculateAccuracyRequest", Server, &pb.MicroserviceCommunication{Data: data})
		if (err == nil) != (accuracy >= 0 && accuracy <= 1) {
			t.Errorf("accuracy %v: %v", accuracy, err)
		}
	}
}

func TestInactivePartyMaskAndReinitializationTiming(t *testing.T) {
	var masks atomic.Int32
	providers := encryptedFixtureWithHook(t, "", false, func(participant, requestType string, data map[string]any) {
		if requestType == "vflMIFEDKGenerationRequest" {
			mask := data["active_parties"].([]any)
			if len(mask) != 4 || mask[3] != float64(0) || mask[0] != float64(1) {
				t.Error("inactive party/label server mask changed", mask)
			}
			masks.Add(1)
		}
	})
	clients := []ClientData{
		{Auth: Server, Active: true}, {Auth: "clientone", Active: true},
		{Auth: "clienttwo", Active: true}, {Auth: "clientthree", Active: false},
	}
	weights := []any{}
	total := newExecutionTiming()
	request := map[string]any{"data": map[string]any{}}
	for i := range 2 {
		timer := newExecutionTiming()
		if err := initializeVFLServices(request, clients, &weights, providers, 2, timer); err != nil {
			t.Fatal(err)
		}
		total.merge(fmt.Sprintf("initialization/%d", i), timer)
	}
	timer := newExecutionTiming()
	if _, err := runVFLTrainingRound(request, clients, &weights, providers, 2, timer); err != nil {
		t.Fatal(err)
	}
	total.merge("round/0", timer)
	metrics := total.summary()
	if metrics["attempts"] != 22 || len(metrics["phase_order"].([]string)) != 13 || masks.Load() != 1 {
		t.Fatal("reinitialization/stage metrics lost", metrics, masks.Load())
	}
}

func TestStatusPollingDuringTimingUpdates(t *testing.T) {
	id := t.Name()
	trainingRequests.Store(id, TrainingRequestData{Status: StatusPending, Results: []map[string]any{}})
	defer trainingRequests.Delete(id)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 50 {
			addAndUpdateTrainingRequest(id, int64(i), 4, 0.75, map[string]any{"duration_ms": 1.0}, nil)
		}
	}()
	for range 50 {
		recorder := httptest.NewRecorder()
		getTrainingStatusHandler()(recorder, httptest.NewRequest("GET", "/?id="+id, nil))
		if recorder.Code != http.StatusOK || !json.Valid(recorder.Body.Bytes()) {
			t.Fatal("invalid polling snapshot", recorder.Code)
		}
	}
	wg.Wait()
}

func TestPolicyMutationTimingDoesNotClaimReasoning(t *testing.T) {
	record := policyActionTiming(3, "removal", time.Now(), fmt.Errorf("mutation failed"))
	if record["failed"] != true || record["train_round"] != int64(3) || record["error"] == nil {
		t.Fatal("policy request failure lost", record)
	}
	if _, ok := record["policy_check_ms"]; ok {
		t.Fatal("HTTP mutation was reported as policy reasoning")
	}
}

func TestInvalidRequestReleasesActiveSlot(t *testing.T) {
	for _, body := range []string{`invalid`, `{"dataRequest":null}`} {
		recorder := httptest.NewRecorder()
		requestHandler()(recorder, httptest.NewRequest("POST", "/", strings.NewReader(body)))
		activeJobLock.Lock()
		active := activeJobID
		activeJobLock.Unlock()
		if recorder.Code != http.StatusBadRequest || active != "" {
			t.Fatal("invalid request retained the active job", recorder.Code, active)
		}

	}
}

func TestLatePolicyUpdateDoesNotBlockConsumer(t *testing.T) {
	id := t.Name()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	policyUpdateMutex.Lock()
	policyUpdateMap[id] = make(chan PolicyUpdateResponse)
	policyUpdateDone[id] = ctx.Done()
	policyUpdateMutex.Unlock()
	defer func() {
		policyUpdateMutex.Lock()
		delete(policyUpdateMap, id)
		delete(policyUpdateDone, id)
		policyUpdateMutex.Unlock()
	}()
	body, err := anypb.New(&pb.PolicyUpdate{User: &pb.User{Id: id}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		handlePolicyUpdateMessage(context.Background(), &pb.SideCarMessage{Body: body})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("late policy update blocked consumer")
	}
}
