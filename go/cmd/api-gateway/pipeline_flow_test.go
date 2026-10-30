package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
)

type pipelineTransport func(*http.Request) (*http.Response, error)

func (f pipelineTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func installPipelineTransport(t *testing.T, failed string, overlap bool) {
	t.Helper()
	old := http.DefaultTransport
	t.Cleanup(func() {
		http.DefaultTransport = old
	})
	forwardNext := make(chan struct{})
	aggregateNext := make(chan struct{})
	var forwardOnce, aggregateOnce sync.Once
	http.DefaultTransport = pipelineTransport(func(r *http.Request) (*http.Response, error) {
		var request struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			return nil, err
		}
		if request.Type == failed {
			return nil, fmt.Errorf("injected %s failure", failed)
		}
		cycle, _ := request.Data["cycle"].(float64)
		target := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		wait := func(ch chan struct{}) error {
			select {
			case <-ch:
				return nil
			case <-time.After(2 * time.Second):
				return fmt.Errorf("pipeline was serialized")
			}
		}
		data := map[string]any{}
		switch request.Type {
		case "vflPingRequest", "vflShutdownRequest":
		case "vflSampleBatchRequest":
			data["sample_batch_indexes"] = "samples"
			if failed == "missing_samples" {
				delete(data, "sample_batch_indexes")
			}
		case "vflTrainRequest":
			if cycle == 1 {
				forwardOnce.Do(func() { close(forwardNext) })
			}
			data["embeddings"] = target
			if failed == "missing_embeddings" {
				delete(data, "embeddings")
			}
		case "vflAggregateRequest":
			if overlap && cycle == 0 {
				if err := wait(forwardNext); err != nil {
					return nil, err
				}
			}
			if cycle == 1 {
				aggregateOnce.Do(func() { close(aggregateNext) })
			}
			embeddings, _ := request.Data["embeddings"].([]any)
			gradients := []string{}
			for _, embedding := range embeddings {
				gradients = append(gradients, "gradient-"+embedding.(string))
			}
			data["gradients"] = gradients
			if failed == "wrong_gradient_count" {
				data["gradients"] = []string{}
			}
			if failed == "invalid_gradient" {
				data["gradients"] = []int{1, 2, 3}
			}
			data["accuracies"] = `{"0":40,"1":50,"2":60}`
			if failed == "invalid_accuracies" {
				data["accuracies"] = "not-json"
			}
		case "vflGradientDescentRequest":
			trainingStateMutex.Lock()
			stored, ok := getTrainingRequest("pipeline-test")
			published := ok && len(stored.Results) > int(cycle)
			trainingStateMutex.Unlock()
			if published {
				return nil, fmt.Errorf("cycle %v published before update acknowledgments", cycle)
			}
			if overlap && cycle == 0 {
				if err := wait(aggregateNext); err != nil {
					return nil, err
				}
			}
			if request.Data["gradients"] != "gradient-"+target {
				return nil, fmt.Errorf("wrong gradient for %s", target)
			}
		case "vflGetAccuraciesRequest":
			data["accuracies"] = `{"0":40,"1":50,"2":60}`
			if failed == "missing_accuracy" {
				data["accuracies"] = `{"0":40}`
			}
		default:
			return nil, fmt.Errorf("unexpected request %s", request.Type)
		}
		body, err := json.Marshal(map[string]any{
			"data": data, "metadata": map[string]string{"dynamos_algorithm_ms": "0"},
		})
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})
}

func testPipeline(t *testing.T, failed string, overlap bool) (*vflPipeline, string) {
	t.Helper()
	id := "pipeline-test"
	trainingRequests.Store(id, TrainingRequestData{Status: StatusPending})
	t.Cleanup(func() { trainingRequests.Delete(id) })
	installPipelineTransport(t, failed, overlap)
	clients := &[]ClientData{
		{Auth: "clienttwo", Url: "127.0.0.1"},
		{Auth: "clientone", Url: "127.0.0.1"},
		{Auth: "clientthree", Url: "127.0.0.1"},
	}

	p := makeVFLChannels()
	startVFLPipeline(p, map[string]any{}, clients, "server", .1, "127.0.0.1", 0, 10, id)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for cycle := range int64(3) {
			if err := runVFLTrainingRound(p, map[string]any{}, "server", "127.0.0.1", 64, cycle); err != nil {
				p.fail(cycle, "server_sample", err)
				break
			}
		}
		close(p.sampleChan)
		<-p.done
	}()
	select {
	case <-finished:
	case <-time.After(8 * time.Second):
		t.Fatal("pipeline failed to drain")
	}
	return p, id
}

func TestTrainingLifecycleMetricsAndFailureStatus(t *testing.T) {
	for _, failure := range []string{"", "vflTrainRequest", "vflGetAccuraciesRequest", "missing_accuracy"} {
		t.Run("failure="+failure, func(t *testing.T) {
			installPipelineTransport(t, failure, false)
			id := t.Name()
			trainingRequests.Store(id, TrainingRequestData{Status: StatusPending})
			t.Cleanup(func() {
				trainingRequests.Delete(id)
				policyUpdateMutex.Lock()
				if ch, ok := policyUpdateMap[id]; ok {
					close(ch)
				}
				policyUpdateMutex.Unlock()
			})
			request := map[string]any{
				"user": &pb.User{Id: id},
				"data": map[string]any{"cycles": float64(30), "communication_frequency": float64(10)},
			}
			runVFLTraining(request, map[string]string{
				"server": "127.0.0.1", "clientone": "127.0.0.1",
				"clienttwo": "127.0.0.1", "clientthree": "127.0.0.1",
			}, "job", context.Background(), id, 25*time.Millisecond)
			stored, _ := getTrainingRequest(id)
			expectedStatus := StatusDone
			if failure != "" {
				expectedStatus = StatusFailed
			}
			if stored.Status != expectedStatus {
				t.Fatalf("status=%s, want %s", stored.Status, expectedStatus)
			}
			timing := stored.Metadata["timing"].(map[string]any)
			if timing["approval_ms"] != float64(25) {
				t.Fatal("approval timing missing")
			}
			for _, name := range []string{"warmup", "shutdown", "final_accuracy"} {
				if _, ok := timing[name].(map[string]any); !ok {
					t.Fatalf("%s timing missing", name)
				}
			}
			if timing["policy_check_measured"] != false {
				t.Fatal("fabricated policy timing")
			}
			if failure == "" && (len(stored.Results) != 3 || timing["measured_calls"] != 24) {
				t.Fatal("lifecycle timing lost completed calls/cycles")
			}
			if failure == "missing_accuracy" {
				for _, result := range stored.Results {
					if _, ok := result["accuracy"]; ok {
						t.Fatal("partial final accuracy update published")
					}
				}
			}
		})
	}
}

func TestPipelineOverlapAndCycleMetrics(t *testing.T) {
	p, id := testPipeline(t, "", true)
	if len(p.errors) != 0 {
		t.Fatal(p.errors)
	}
	stored, _ := getTrainingRequest(id)
	if len(stored.Results) != 3 {
		t.Fatalf("cycles=%d", len(stored.Results))
	}
	var measured int
	for index, result := range stored.Results {
		if result["train_round"] != int64(index) {
			t.Fatal("cycle correlation lost")
		}
		summary := result["timing"].(map[string]any)
		measured += summary["measured_calls"].(int)
		if len(summary["phases"].(map[string]any)) != 4 {
			t.Fatal("missing cycle stage")
		}
	}
	if measured != 24 {
		t.Fatalf("measured calls=%d, want 3*(1+3+1+3)", measured)
	}
	if len(p.phases) != 12 {
		t.Fatal("pipeline stages lost")
	}
	phase := startPhase("final_accuracy")
	accuracies, err := getVFLAccuracies(phase, map[string]any{}, "server", "127.0.0.1")
	phase.finish()
	if err != nil {
		t.Fatal(err)
	}
	if err := updateTrainingRequest(id, accuracies); err != nil {
		t.Fatal(err)
	}
	stored, _ = getTrainingRequest(id)
	if stored.Results[2]["accuracy"] != float64(60) {
		t.Fatal("final accuracy not updated")
	}
}

func TestPipelineStageErrorsDrain(t *testing.T) {
	for _, stage := range []string{
		"vflSampleBatchRequest", "vflTrainRequest",
		"vflAggregateRequest", "vflGradientDescentRequest",
		"missing_samples", "missing_embeddings", "wrong_gradient_count", "invalid_gradient", "invalid_accuracies",
	} {
		t.Run(stage, func(t *testing.T) {
			p, _ := testPipeline(t, stage, false)
			if len(p.errors) == 0 {
				t.Fatal("failure was hidden")
			}
			for _, phase := range p.phases {
				if phase.end.IsZero() {
					t.Fatal("failed stage timing not finalized")
				}
			}
		})
	}
}

func TestStatusPollingDuringPipeline(t *testing.T) {
	id := "status-concurrent"
	trainingRequests.Store(id, TrainingRequestData{Status: StatusPending})
	defer trainingRequests.Delete(id)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for cycle := range int64(50) {
			addAndUpdateTrainingRequest(id, cycle, 3, map[string]any{"duration_ms": 1})
		}
	}()
	handler := getTrainingStatusHandler()
	for range 50 {
		response := httptest.NewRecorder()
		handler(response, httptest.NewRequest(http.MethodGet, "/?id="+id, nil).WithContext(context.Background()))
		if response.Code != 200 {
			t.Fatal(response.Code)
		}
		var result map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}
