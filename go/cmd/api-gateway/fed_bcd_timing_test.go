package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
)

type timingTransport func(*http.Request) (*http.Response, error)

func (f timingTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func mockFedBCD(t *testing.T, failedType string) {
	t.Helper()
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = timingTransport(func(r *http.Request) (*http.Response, error) {
		var request struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			return nil, err
		}
		target := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if request.Type == failedType {
			return nil, fmt.Errorf("injected %s failure", failedType)
		}
		data := map[string]any{}
		switch request.Type {
		case "vflSampleBatchRequest":
			data["sample_batch_indexes"] = "sample"
		case "vflTrainRequest":
			if request.Data["sample_batch_indexes"] != "sample" {
				t.Error("forward request lost sample indexes")
			}
			data["embeddings"] = target
		case "vflAggregateRequest":
			embeddings, ok := request.Data["embeddings"].([]any)
			if !ok || len(embeddings) != 3 {
				return nil, fmt.Errorf("incorrect embeddings: %v", request.Data["embeddings"])
			}
			gradients := make([]string, len(embeddings))
			for index, embedding := range embeddings {
				gradients[index] = "gradient-" + embedding.(string)
			}
			data["gradients"] = gradients
		case "vflGradientDescentRequest":
			if request.Data["gradients"] != "gradient-"+target {
				t.Errorf("wrong gradient for %s: %v", target, request.Data)
			}
		case "vflLocalUpdateRequest":
			data["accuracies"] = []float64{40, 50}
		case "vflPingRequest", "vflShutdownRequest":
		default:
			return nil, fmt.Errorf("unexpected request %q", request.Type)
		}
		body, err := json.Marshal(map[string]any{
			"data": data, "metadata": map[string]string{"dynamos_algorithm_ms": "0"},
		})
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(string(body))),
		}, nil
	})
}

func fedBCDClients() []ClientData {
	return []ClientData{
		{Auth: "clientone", Url: "127.0.0.1"},
		{Auth: "clienttwo", Url: "127.0.0.1"},
		{Auth: "clientthree", Url: "127.0.0.1"},
	}
}

func TestFedBCDTimingPhasesAndRequestIsolation(t *testing.T) {
	mockFedBCD(t, "")
	base := map[string]any{"type": "unchanged", "data": map[string]any{"cycles": 120}}
	var phases []*phaseRecorder
	start := time.Now()
	accuracies, err := runVFLTrainingRound(base, fedBCDClients(), "server", "127.0.0.1", .1, 0, 10, 64, &phases)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(accuracies, []float64{40, 50}) || base["type"] != "unchanged" {
		t.Fatalf("accuracies=%v, base request=%v", accuracies, base)
	}
	summary := roundTiming(time.Since(start), 0, phases, nil)
	if !reflect.DeepEqual(summary["phase_order"], []string{
		"server_sample", "client_forward", "server_aggregate", "local_update",
	}) {
		t.Fatalf("phases = %v", summary["phase_order"])
	}
	if summary["measured_calls"] != 9 || summary["unmeasured_calls"] != 0 {
		t.Fatalf("measurement coverage = %v", summary)
	}
	if summary["coordination_overhead_ms"].(float64) < 0 {
		t.Fatal("parallel local updates were counted twice")
	}
	update := summary["phases"].(map[string]any)["local_update"].(map[string]any)
	if len(update["calls"].(map[string]any)) != 4 {
		t.Fatal("local update must cover all three clients and server")
	}
}

func TestFedBCDPhaseFailuresAreRecorded(t *testing.T) {
	for _, failedType := range []string{
		"vflSampleBatchRequest", "vflTrainRequest", "vflAggregateRequest",
		"vflGradientDescentRequest", "vflLocalUpdateRequest",
	} {
		t.Run(failedType, func(t *testing.T) {
			mockFedBCD(t, failedType)
			var phases []*phaseRecorder
			_, err := runVFLTrainingRound(map[string]any{}, fedBCDClients(), "server", "127.0.0.1", .1, 0, 10, 64, &phases)
			if err == nil {
				t.Fatal("phase error was hidden")
			}
			last := phases[len(phases)-1]
			if last.end.IsZero() || last.summary()["unmeasured_calls"].(int) == 0 {
				t.Fatal("failed phase was not finalized and recorded")
			}
		})
	}
}

func TestFedBCDFullRunTimingMetadata(t *testing.T) {
	mockFedBCD(t, "")
	id := "timing-full-run"
	user := &pb.User{Id: id}
	trainingRequests.Store(id, TrainingRequestData{Status: StatusPending})
	t.Cleanup(func() {
		trainingRequests.Delete(id)
		policyUpdateMutex.Lock()
		if channel, ok := policyUpdateMap[id]; ok {
			close(channel)
		}
		policyUpdateMutex.Unlock()
	})
	providers := map[string]string{"server": "127.0.0.1"}
	for _, client := range fedBCDClients() {
		providers[client.Auth] = client.Url
	}
	runVFLTraining(map[string]any{
		"user": user, "data": map[string]any{
			"cycles": float64(120), "communication_frequency": float64(10),
		},
	}, providers, "job", context.Background(), id, time.Millisecond)
	stored, _ := trainingRequests.Load(id)
	result := stored.(TrainingRequestData)
	if result.Status != StatusDone || len(result.Results) != 12 {
		t.Fatalf("status=%s rounds=%d, want done/12", result.Status, len(result.Results))
	}
	if result.Metadata["synchronization_rounds"] != int64(12) ||
		result.Metadata["completed_local_steps"] != int64(120) {
		t.Fatalf("incorrect FedBCD step metadata: %v", result.Metadata)
	}
	timing := result.Metadata["timing"].(map[string]any)
	if timing["measured_calls"] != 108 || timing["policy_checks"] != 0 {
		t.Fatalf("incorrect run coverage/policy mode: %v", timing)
	}
	for _, phase := range []string{"warmup", "shutdown"} {
		if timing[phase].(map[string]any)["measured_calls"] != 4 {
			t.Fatalf("%s timing missing", phase)
		}
	}
}

func TestFedBCDLocalUpdatesRemainParallel(t *testing.T) {
	mockFedBCD(t, "")
	transport := http.DefaultTransport
	serverStarted := make(chan struct{})
	var once sync.Once
	http.DefaultTransport = timingTransport(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		var request struct{ Type string }
		if err := json.Unmarshal(body, &request); err != nil {
			return nil, err
		}
		switch request.Type {
		case "vflLocalUpdateRequest":
			once.Do(func() { close(serverStarted) })
		case "vflGradientDescentRequest":
			select {
			case <-serverStarted:
			case <-time.After(5 * time.Second):
				return nil, fmt.Errorf("client updates serialized before server update")
			}
		}
		return transport.RoundTrip(r)
	})
	var phases []*phaseRecorder
	if _, err := runVFLTrainingRound(map[string]any{}, fedBCDClients(), "server", "127.0.0.1", .1, 0, 10, 64, &phases); err != nil {
		t.Fatal(err)
	}
}
