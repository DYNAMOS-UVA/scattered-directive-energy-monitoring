package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
