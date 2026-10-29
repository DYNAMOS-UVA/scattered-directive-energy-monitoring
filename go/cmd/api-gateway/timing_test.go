package main

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Jorrit05/DYNAMOS/pkg/lib"
)

func fixedPhase(name string, start time.Time, calls ...callTiming) *phaseRecorder {
	p := &phaseRecorder{name: name, start: start, calls: calls}
	var end time.Time
	for _, c := range calls {
		if c.end.After(end) {
			end = c.end
		}
	}
	p.end = end
	return p
}

func TestResponseAlgorithmTiming(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want *time.Duration
		fail bool
	}{
		{name: "old sidecar", body: `{"metadata":{"accuracy":"0.9"}}`},
		{name: "no metadata", body: `{}`},
		{name: "null metadata", body: `{"metadata":null}`},
		{name: "valid", body: `{"metadata":{"dynamos_algorithm_ms":"25.125"}}`, want: durationPointer(25125 * time.Microsecond)},
		{name: "zero is measured", body: `{"metadata":{"dynamos_algorithm_ms":"0"}}`, want: durationPointer(0)},
		{name: "negative", body: `{"metadata":{"dynamos_algorithm_ms":"-1"}}`, fail: true},
		{name: "nan", body: `{"metadata":{"dynamos_algorithm_ms":"NaN"}}`, fail: true},
		{name: "infinity", body: `{"metadata":{"dynamos_algorithm_ms":"+Inf"}}`, fail: true},
		{name: "too long", body: `{"metadata":{"dynamos_algorithm_ms":"101"}}`, fail: true},
		{name: "overflow", body: `{"metadata":{"dynamos_algorithm_ms":"1e300"}}`, fail: true},
		{name: "malformed value", body: `{"metadata":{"dynamos_algorithm_ms":"bad"}}`, fail: true},
		{name: "wrong type", body: `{"metadata":{"dynamos_algorithm_ms":12}}`, fail: true},
		{name: "malformed response", body: `not-json`, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := responseAlgorithmTiming(tc.body, 100*time.Millisecond)
			if (err != nil) != tc.fail {
				t.Fatalf("error = %v, want failure %v", err, tc.fail)
			}
			if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Fatalf("duration = %v, want %v", got, tc.want)
			}
		})
	}
}

func durationPointer(d time.Duration) *time.Duration { return &d }

func TestCommunicationTimingCoverageAndTotals(t *testing.T) {
	t0 := time.Now()
	ms := time.Millisecond
	phase := fixedPhase("forward", t0,
		callTiming{participant: "clientone", duration: 100 * ms, end: t0.Add(100 * ms), algorithm: durationPointer(30 * ms)},
		callTiming{participant: "clienttwo", duration: 50 * ms, end: t0.Add(50 * ms)},
		callTiming{participant: "clientone", duration: 10 * ms, end: t0.Add(110 * ms), failed: true},
		callTiming{participant: "clientthree", duration: 20 * ms, end: t0.Add(20 * ms), algorithm: durationPointer(0)},
	)
	totals := &runTimingTotals{}
	round := roundTiming(120*ms, 0, []*phaseRecorder{phase}, totals)
	run := make(map[string]any)
	totals.timing.report(run)
	phaseSummary := round["phases"].(map[string]any)["forward"].(map[string]any)
	for _, summary := range []map[string]any{round, run, phaseSummary} {
		if summary["algorithm_ms"] != float64(30) || summary["communication_ms"] != float64(90) ||
			summary["measured_round_trip_ms"] != float64(120) || summary["measured_calls"] != 2 || summary["unmeasured_calls"] != 2 {
			t.Errorf("incorrect partial-coverage summary: %v", summary)
		}
	}
	calls := phaseSummary["calls"].(map[string]any)
	retried := calls["clientone"].(map[string]any)
	if retried["duration_ms"] != float64(110) || retried["measured_round_trip_ms"] != float64(100) || retried["unmeasured_calls"] != 1 {
		t.Errorf("retry mixed measured/unmeasured durations: %v", retried)
	}
	for _, container := range []map[string]any{calls, round["participants"].(map[string]any)} {
		unmeasured := container["clienttwo"].(map[string]any)
		if _, ok := unmeasured["algorithm_ms"]; ok {
			t.Error("missing metadata was treated as zero algorithm time")
		}
		if _, ok := unmeasured["communication_ms"]; ok {
			t.Error("missing metadata was treated as measured communication")
		}
	}
	roundTiming(120*ms, 0, []*phaseRecorder{phase}, totals)
	totals.timing.report(run)
	if run["measured_calls"] != 4 || run["unmeasured_calls"] != 4 ||
		run["algorithm_ms"] != float64(60) || run["communication_ms"] != float64(180) {
		t.Errorf("run did not accumulate both rounds: %v", run)
	}
}

func TestPhaseRecorderExportsSidecarMeasurements(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond)
		json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]string{
			lib.AlgorithmTimingMetadataKey: "2",
		}})
	}))
	defer server.Close()
	phase := startPhase("forward")
	if _, err := phase.send("clientone", server.URL, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	phase.finish()
	call := phase.summary()["calls"].(map[string]any)["clientone"].(map[string]any)
	if call["algorithm_ms"] != float64(2) || call["measured_calls"] != 1 || call["unmeasured_calls"] != 0 {
		t.Fatalf("sidecar measurement missing: %v", call)
	}
	sum := call["algorithm_ms"].(float64) + call["communication_ms"].(float64)
	if math.Abs(sum-call["duration_ms"].(float64)) > 0.002 {
		t.Fatalf("call decomposition does not match within rounding precision: %v", call)
	}
}

func TestPhaseRecorderInvalidTimingDoesNotFailRequest(t *testing.T) {
	for _, body := range []string{
		`{"metadata":{"dynamos_algorithm_ms":"-1"}}`,
		`{"metadata":{"dynamos_algorithm_ms":"invalid"}}`,
		`{"metadata":{}}`,
	} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(body))
			}))
			defer server.Close()
			phase := startPhase("forward")
			response, err := phase.send("clientone", server.URL, []byte("{}"))
			if err != nil || response != body {
				t.Fatalf("optional timing affected the response: %q, %v", response, err)
			}
			phase.finish()
			summary := phase.summary()
			if summary["measured_calls"] != 0 || summary["unmeasured_calls"] != 1 {
				t.Fatalf("incorrect missing/invalid coverage: %v", summary)
			}
			if _, ok := summary["communication_ms"]; ok {
				t.Fatal("missing/invalid timing fabricated a communication measurement")
			}
		})
	}
}

func TestRoundTimingDecomposition(t *testing.T) {
	t0 := time.Unix(0, 0)
	ms := time.Millisecond

	// Clients finish the forward phase at 20 ms and 80 ms: 60 ms barrier wait.
	forward := fixedPhase("client_forward", t0,
		callTiming{participant: "clientone", duration: 20 * ms, end: t0.Add(20 * ms), bytesSent: 10, bytesRecv: 100},
		callTiming{participant: "clienttwo", duration: 80 * ms, end: t0.Add(80 * ms), bytesSent: 10, bytesRecv: 100},
	)
	aggregate := fixedPhase("server_aggregate", t0.Add(80*ms),
		callTiming{participant: "server", duration: 50 * ms, end: t0.Add(130 * ms), bytesSent: 200, bytesRecv: 40},
	)
	// Retried call to the same participant is merged, not double-counted as waiting.
	update := fixedPhase("client_update", t0.Add(130*ms),
		callTiming{participant: "clientone", duration: 10 * ms, end: t0.Add(140 * ms), bytesSent: 20},
		callTiming{participant: "clienttwo", duration: 5 * ms, end: t0.Add(135 * ms), bytesSent: 20, failed: true},
		callTiming{participant: "clienttwo", duration: 10 * ms, end: t0.Add(145 * ms), bytesSent: 20},
	)

	totals := &runTimingTotals{}
	got := roundTiming(200*ms, 30*ms, []*phaseRecorder{forward, aggregate, update}, totals)

	expect := map[string]float64{
		"round_ms":                 200,
		"policy_check_ms":          30,
		"barrier_wait_ms":          60 + 5, // forward + update (clientone done 5 ms early)
		"coordination_overhead_ms": 200 - 30 - (80 + 50 + 15),
	}
	for key, want := range expect {
		if got[key].(float64) != want {
			t.Errorf("%s = %v, want %v", key, got[key], want)
		}
	}
	if got["bytes_sent"].(int) != 280 || got["bytes_received"].(int) != 240 {
		t.Errorf("bytes = %v/%v, want 280/240", got["bytes_sent"], got["bytes_received"])
	}

	participants := got["participants"].(map[string]any)
	server := participants["server"].(map[string]any)
	if server["active_ms"].(float64) != 50 || server["idle_ms"].(float64) != 150 {
		t.Errorf("server = %v, want active 50 / idle 150", server)
	}
	clientTwo := participants["clienttwo"].(map[string]any)
	if clientTwo["active_ms"].(float64) != 95 {
		t.Errorf("clienttwo active = %v, want 95", clientTwo["active_ms"])
	}

	updateCalls := got["phases"].(map[string]any)["client_update"].(map[string]any)["calls"].(map[string]any)
	retried := updateCalls["clienttwo"].(map[string]any)
	if retried["attempts"] != 2 || retried["failures"] != 1 {
		t.Errorf("retried call = %v, want 2 attempts / 1 failure", retried)
	}

	if totals.rounds != 200*ms || totals.barrierWait != 65*ms || totals.bytesSent != 280 {
		t.Errorf("totals = %+v", totals)
	}
}

func TestPhaseRecorderSendMeasuresRoundTrips(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(120 * time.Millisecond)
		w.Write([]byte("slow-response"))
	}))
	defer slow.Close()
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer fast.Close()

	phase := startPhase("client_forward")
	done := make(chan struct{})
	go func() { phase.send("slow", slow.URL, []byte("payload")); close(done) }()
	if _, err := phase.send("fast", fast.URL, []byte("payload")); err != nil {
		t.Fatalf("fast send failed: %v", err)
	}
	<-done
	phase.finish()

	summary := phase.summary()
	calls := summary["calls"].(map[string]any)
	slowCall := calls["slow"].(map[string]any)
	if slowCall["duration_ms"].(float64) < 120 || slowCall["bytes_received"] != len("slow-response") || slowCall["bytes_sent"] != len("payload") {
		t.Errorf("slow call = %v", slowCall)
	}
	if wait := summary["barrier_wait_ms"].(float64); wait < 100 {
		t.Errorf("barrier_wait_ms = %v, want >= 100 (fast participant waited for slow)", wait)
	}
	if summary["duration_ms"].(float64) < slowCall["duration_ms"].(float64) {
		t.Errorf("phase shorter than its slowest call: %v", summary)
	}
}
