package main

import (
	"fmt"
	"sort"
	"time"
)

type timingInterval struct{ start, end time.Time }

func intervalUnion(intervals []timingInterval) time.Duration {
	if len(intervals) == 0 {
		return 0
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].start.Before(intervals[j].start) })
	start, end := intervals[0].start, intervals[0].end
	var duration time.Duration
	for _, interval := range intervals[1:] {
		if interval.start.After(end) {
			duration += end.Sub(start)
			start, end = interval.start, interval.end
		} else if interval.end.After(end) {
			end = interval.end
		}
	}
	return duration + end.Sub(start)
}

func pipelineTiming(start, end time.Time, phases []*phaseRecorder) map[string]any {
	stageSummaries := map[string]any{}
	stageOrder := []string{}
	var intervals []timingInterval
	var stageSum time.Duration
	var sent, received int
	var coverage communicationTiming
	requestIntervals := map[string][]timingInterval{}
	requestSums := map[string]time.Duration{}
	participantCoverage := map[string]communicationTiming{}
	var replyWait time.Duration
	for _, phase := range phases {
		summary := phase.summary()
		key := phase.name
		if phase.cycle != nil {
			key = fmt.Sprintf("%d/%s", *phase.cycle, phase.name)
			summary["cycle"] = *phase.cycle
		}
		summary["start_offset_ms"] = toMs(phase.start.Sub(start))
		summary["end_offset_ms"] = toMs(phase.end.Sub(start))
		stageSummaries[key] = summary
		stageOrder = append(stageOrder, key)
		intervals = append(intervals, timingInterval{phase.start, phase.end})
		stageSum += phase.duration()
		replyWait += barrierWait(phase.perParticipant())
		for _, call := range phase.calls {
			sent += call.bytesSent
			received += call.bytesRecv
			coverage.addCall(call)
			requestIntervals[call.participant] = append(requestIntervals[call.participant],
				timingInterval{call.end.Add(-call.duration), call.end})
			requestSums[call.participant] += call.duration
			participant := participantCoverage[call.participant]
			participant.addCall(call)
			participantCoverage[call.participant] = participant
		}
	}
	busy := intervalUnion(intervals)
	summary := map[string]any{
		"clock":                 "api-gateway",
		"duration_ms":           toMs(end.Sub(start)),
		"stage_elapsed_sum_ms":  toMs(stageSum),
		"stage_union_ms":        toMs(busy),
		"stage_overlap_ms":      toMs(stageSum - busy),
		"outside_stage_ms":      toMs(end.Sub(start) - busy),
		"reply_barrier_wait_ms": toMs(replyWait),
		"bytes_sent":            sent, "bytes_received": received,
		"phase_order": stageOrder, "phases": stageSummaries,
		"timing_semantics":      "handler_reply_latency; background compute may continue after reply",
		"policy_check_measured": false,
	}
	coverage.report(summary)
	participants := map[string]any{}
	for name, intervals := range requestIntervals {
		active := intervalUnion(intervals)
		participant := map[string]any{
			"request_active_ms":      toMs(active),
			"request_idle_ms":        toMs(end.Sub(start) - active),
			"request_elapsed_sum_ms": toMs(requestSums[name]),
		}
		participantCoverage[name].report(participant)
		participants[name] = participant
	}
	summary["participants"] = participants
	return summary
}
