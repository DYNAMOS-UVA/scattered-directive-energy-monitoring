package main

import (
	"testing"
	"time"
)

func TestPipelineIntervalUnion(t *testing.T) {
	start := time.Now()
	ms := time.Millisecond
	first := fixedPhase("forward", start, callTiming{
		participant: "client", duration: 100 * ms, end: start.Add(100 * ms),
		algorithm: durationPointer(10 * ms), bytesSent: 10, bytesRecv: 20,
	})
	second := fixedPhase("aggregate", start.Add(50*ms), callTiming{
		participant: "client", duration: 100 * ms, end: start.Add(150 * ms),
		algorithm: durationPointer(20 * ms), bytesSent: 30, bytesRecv: 40,
	})
	cycle0, cycle1 := int64(0), int64(1)
	first.cycle, second.cycle = &cycle0, &cycle1
	summary := pipelineTiming(start, start.Add(200*ms), []*phaseRecorder{second, first})
	for key, expected := range map[string]float64{
		"duration_ms": 200, "stage_elapsed_sum_ms": 200,
		"stage_union_ms": 150, "stage_overlap_ms": 50, "outside_stage_ms": 50,
		"algorithm_ms": 30, "communication_ms": 170, "measured_round_trip_ms": 200,
	} {
		if summary[key] != expected {
			t.Errorf("%s=%v, want %v", key, summary[key], expected)
		}
	}
	if len(summary["phases"].(map[string]any)) != 2 {
		t.Fatal("overlapping cycles overwrote their stage timings")
	}
	participant := summary["participants"].(map[string]any)["client"].(map[string]any)
	if participant["request_active_ms"] != float64(150) || participant["request_idle_ms"] != float64(50) ||
		participant["request_elapsed_sum_ms"] != float64(200) {
		t.Fatalf("overlapping calls double-counted participant activity: %v", participant)
	}
	if summary["bytes_sent"] != 40 || summary["bytes_received"] != 60 || summary["measured_calls"] != 2 {
		t.Fatal("payloads or coverage lost across overlapping phases")
	}
	if _, exists := summary["coordination_overhead_ms"]; exists {
		t.Fatal("overlap must not be reported as a sequential coordination residual")
	}
}

func TestPipelineRepeatedStagesAndZeroCalls(t *testing.T) {
	start := time.Now()
	one, two := int64(1), int64(2)
	phases := []*phaseRecorder{
		{name: "forward", start: start, end: start.Add(time.Millisecond), cycle: &one},
		{name: "forward", start: start, end: start.Add(time.Millisecond), cycle: &two},
	}
	summary := pipelineTiming(start, start.Add(2*time.Millisecond), phases)
	if len(summary["phases"].(map[string]any)) != 2 || summary["measured_calls"] != 0 {
		t.Fatal("stage identity or coverage lost")
	}
	if _, measured := summary["algorithm_ms"]; measured {
		t.Fatal("unmeasured compute must not be zero")
	}
}
