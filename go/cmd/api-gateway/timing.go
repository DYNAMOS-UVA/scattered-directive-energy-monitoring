package main

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/Jorrit05/DYNAMOS/pkg/lib"
)

type timedCall struct {
	Stage       string   `json:"stage"`
	Participant string   `json:"participant"`
	StartMS     float64  `json:"start_ms"`
	EndMS       float64  `json:"end_ms"`
	DurationMS  float64  `json:"duration_ms"`
	BytesSent   int      `json:"bytes_sent"`
	BytesRecv   int      `json:"bytes_received"`
	Failed      bool     `json:"failed"`
	Error       string   `json:"error,omitempty"`
	AlgorithmMS *float64 `json:"algorithm_ms,omitempty"`
	start       time.Time
	end         time.Time
}

func (t *executionTiming) failReply(stage, participant string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := len(t.calls) - 1; i >= 0; i-- {
		if t.calls[i].Stage == stage && t.calls[i].Participant == participant {
			t.calls[i].Failed = true
			t.calls[i].Error = err.Error()
			return
		}
	}
}

type executionTiming struct {
	start time.Time
	mu    sync.Mutex
	calls []timedCall
}

func newExecutionTiming() *executionTiming {
	return &executionTiming{start: time.Now()}
}

func (t *executionTiming) merge(prefix string, other *executionTiming) {
	other.mu.Lock()
	calls := append([]timedCall{}, other.calls...)
	other.mu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, call := range calls {
		call.Stage = prefix + "/" + call.Stage
		call.StartMS, call.EndMS = toMs(call.start.Sub(t.start)), toMs(call.end.Sub(t.start))
		t.calls = append(t.calls, call)
	}
}

func failTrainingApproval(id string, err error, elapsed time.Duration) {
	logger.Sugar().Errorw("Training approval failed", "request_id", id, "error", err)
	data, ok := getTrainingRequest(id)
	if !ok {
		return
	}
	data.Status = StatusFailed
	data.Metadata = map[string]any{"error": err.Error(), "timing": map[string]any{"approval_ms": toMs(elapsed)}}
	trainingDataMutex.Lock()
	trainingRequests.Store(id, data)
	trainingDataMutex.Unlock()
}

func policyActionTiming(cycle int64, action string, start time.Time, err error) map[string]any {
	result := map[string]any{
		"train_round": cycle, "action": action, "duration_ms": toMs(time.Since(start)),
		"failed": err != nil, "semantics": "HTTP policy mutation only; event propagation and reasoning are not measured",
	}
	if err != nil {
		logger.Sugar().Errorw("Policy mutation failed", "action", action, "error", err)
		result["error"] = err.Error()
	}
	return result
}
func toMs(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

func responseAlgorithmTiming(body string, roundTrip time.Duration) (*float64, error) {
	var response struct {
		Metadata map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		return nil, fmt.Errorf("decode timing metadata: %w", err)
	}
	raw, ok := response.Metadata[lib.AlgorithmTimingMetadataKey]
	if !ok {
		return nil, nil
	}
	ms, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(ms) || math.IsInf(ms, 0) || ms < 0 || ms > toMs(roundTrip) {
		return nil, fmt.Errorf("handler duration %q is outside the round trip", raw)
	}
	return &ms, nil
}

func (t *executionTiming) send(stage, participant, endpoint string, body []byte) (string, error) {
	start := time.Now()
	response, err := sendData(endpoint, body)
	end := time.Now()
	call := timedCall{
		Stage: stage, Participant: participant, start: start, end: end,
		StartMS: toMs(start.Sub(t.start)), EndMS: toMs(end.Sub(t.start)),
		DurationMS: toMs(end.Sub(start)), BytesSent: len(body), BytesRecv: len(response), Failed: err != nil,
	}
	if err == nil {
		var timingErr error
		call.AlgorithmMS, timingErr = responseAlgorithmTiming(response, end.Sub(start))
		if timingErr != nil {
			logger.Sugar().Warnw("Ignoring invalid sidecar timing", "participant", participant, "stage", stage, "error", timingErr)
		}
	} else {
		call.Error = err.Error()
		logger.Sugar().Errorw("Timed agent request failed", "participant", participant, "stage", stage, "error", err)
	}
	t.mu.Lock()
	t.calls = append(t.calls, call)
	t.mu.Unlock()
	return response, err
}

type timeInterval struct{ start, end time.Time }

func intervalUnion(intervals []timeInterval) time.Duration {
	if len(intervals) == 0 {
		return 0
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].start.Before(intervals[j].start) })
	start, end := intervals[0].start, intervals[0].end
	var total time.Duration
	for _, current := range intervals[1:] {
		if current.start.After(end) {
			total += end.Sub(start)
			start, end = current.start, current.end
		} else if current.end.After(end) {
			end = current.end
		}
	}
	return total + end.Sub(start)
}

func callSummary(calls []timedCall) map[string]any {
	var sent, received, failures, measured int
	var elapsed, algorithm, communication, measuredRTT float64
	for _, call := range calls {
		sent += call.BytesSent
		received += call.BytesRecv
		elapsed += call.DurationMS
		if call.Failed {
			failures++
		} else if call.AlgorithmMS != nil {
			measured++
			algorithm += *call.AlgorithmMS
			measuredRTT += call.DurationMS
			communication += call.DurationMS - *call.AlgorithmMS
		}
	}
	result := map[string]any{
		"attempts": len(calls), "failures": failures, "bytes_sent": sent, "bytes_received": received,
		"request_elapsed_sum_ms": elapsed, "measured_calls": measured, "unmeasured_calls": len(calls) - measured,
	}
	if measured > 0 {
		result["algorithm_ms"], result["communication_ms"], result["measured_round_trip_ms"] = algorithm, communication, measuredRTT
	}
	return result
}

func (t *executionTiming) summary() map[string]any {
	end := time.Now()
	t.mu.Lock()
	calls := append([]timedCall{}, t.calls...)
	t.mu.Unlock()
	sort.Slice(calls, func(i, j int) bool { return calls[i].start.Before(calls[j].start) })
	stages := map[string][]timedCall{}
	participants := map[string][]timedCall{}
	order := []string{}
	for _, call := range calls {
		if _, ok := stages[call.Stage]; !ok {
			order = append(order, call.Stage)
		}
		stages[call.Stage] = append(stages[call.Stage], call)
		participants[call.Participant] = append(participants[call.Participant], call)
	}
	phases := map[string]any{}
	var stageIntervals []timeInterval
	var stageSum, barrierWait time.Duration
	for _, name := range order {
		group := stages[name]
		first, last := group[0].start, group[0].end
		lastReplies := map[string]time.Time{}
		for _, call := range group {
			if call.end.After(last) {
				last = call.end
			}
			if call.end.After(lastReplies[call.Participant]) {
				lastReplies[call.Participant] = call.end
			}
		}
		var wait time.Duration
		for _, reply := range lastReplies {
			wait += last.Sub(reply)
		}
		phase := callSummary(group)
		phase["start_ms"], phase["end_ms"] = toMs(first.Sub(t.start)), toMs(last.Sub(t.start))
		phase["duration_ms"], phase["barrier_wait_ms"] = toMs(last.Sub(first)), toMs(wait)
		phase["calls"] = group
		phases[name] = phase
		stageIntervals = append(stageIntervals, timeInterval{first, last})
		stageSum += last.Sub(first)
		barrierWait += wait
	}
	participantSummaries := map[string]any{}
	for name, group := range participants {
		var intervals []timeInterval
		for _, call := range group {
			intervals = append(intervals, timeInterval{call.start, call.end})
		}
		active := intervalUnion(intervals)
		summary := callSummary(group)
		summary["request_active_ms"], summary["no_outstanding_request_ms"] = toMs(active), toMs(end.Sub(t.start)-active)
		participantSummaries[name] = summary
	}
	union := intervalUnion(stageIntervals)
	result := callSummary(calls)
	result["duration_ms"], result["phase_order"], result["phases"] = toMs(end.Sub(t.start)), order, phases
	result["stage_elapsed_sum_ms"], result["stage_union_ms"], result["stage_overlap_ms"] = toMs(stageSum), toMs(union), toMs(stageSum-union)
	result["outside_stage_ms"], result["reply_barrier_wait_ms"] = toMs(end.Sub(t.start)-union), toMs(barrierWait)
	result["participants"] = participantSummaries
	result["clock"] = "api-gateway"
	result["policy_check_measured"] = false
	result["timing_semantics"] = "sidecar handler-to-reply latency, not pure cryptographic compute or independently verified background completion"
	return result
}
