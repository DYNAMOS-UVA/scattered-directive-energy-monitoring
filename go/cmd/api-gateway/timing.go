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

// Gateway-side timing of a distributed training run. The gateway drives every
// phase, so it can measure wall-clock phase durations, per-participant round
// trips and payload sizes without changes to agents or algorithm code.
// Durations use local monotonic clocks, never cross-node timestamps.
//
// A round trip includes transport, queueing and the participant's compute; the
// gateway separates these only when the job sidecar provides a duration.
// barrier_wait_ms is the synchronisation cost:
// the summed time participants that finished a parallel phase early spent
// waiting for the slowest one.

type callTiming struct {
	participant string
	duration    time.Duration
	end         time.Time
	bytesSent   int
	bytesRecv   int
	failed      bool
	algorithm   *time.Duration
}

type phaseRecorder struct {
	name  string
	start time.Time
	end   time.Time
	mu    sync.Mutex
	calls []callTiming
}

func startPhase(name string) *phaseRecorder {
	return &phaseRecorder{name: name, start: time.Now()}
}

// send performs sendData and records its round trip; safe for concurrent use.
func (p *phaseRecorder) send(participant, endpoint string, body []byte) (string, error) {
	start := time.Now()
	resp, err := sendData(endpoint, body)
	end := time.Now()

	var algorithm *time.Duration
	if err == nil {
		var timingErr error
		algorithm, timingErr = responseAlgorithmTiming(resp, end.Sub(start))
		if timingErr != nil {
			logger.Sugar().Warnw("Ignoring invalid sidecar timing", "participant", participant, "error", timingErr)
		}
	}
	p.mu.Lock()
	p.calls = append(p.calls, callTiming{
		participant: participant,
		duration:    end.Sub(start),
		end:         end,
		bytesSent:   len(body),
		bytesRecv:   len(resp),
		failed:      err != nil,
		algorithm:   algorithm,
	})
	p.mu.Unlock()
	return resp, err
}

func responseAlgorithmTiming(body string, roundTrip time.Duration) (*time.Duration, error) {
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
	if err != nil || math.IsNaN(ms) || math.IsInf(ms, 0) || ms < 0 || ms > float64(roundTrip)/float64(time.Millisecond) {
		return nil, fmt.Errorf("algorithm duration %q is not within the round trip", raw)
	}
	duration := time.Duration(ms * float64(time.Millisecond))
	return &duration, nil
}

// Sums describe measured calls only; coverage prevents missing data looking like zero.
type communicationTiming struct {
	measured      int
	unmeasured    int
	roundTrip     time.Duration
	algorithm     time.Duration
	communication time.Duration
}

func (t *communicationTiming) addCall(c callTiming) {
	if c.failed || c.algorithm == nil {
		t.unmeasured++
		return
	}
	t.measured++
	t.roundTrip += c.duration
	t.algorithm += *c.algorithm
	t.communication += c.duration - *c.algorithm
}

func (t *communicationTiming) add(other communicationTiming) {
	t.measured += other.measured
	t.unmeasured += other.unmeasured
	t.roundTrip += other.roundTrip
	t.algorithm += other.algorithm
	t.communication += other.communication
}

func (t communicationTiming) report(summary map[string]any) {
	summary["measured_calls"] = t.measured
	summary["unmeasured_calls"] = t.unmeasured
	if t.measured > 0 {
		summary["algorithm_ms"] = toMs(t.algorithm)
		summary["communication_ms"] = toMs(t.communication)
		summary["measured_round_trip_ms"] = toMs(t.roundTrip)
	}
}

func (p *phaseRecorder) finish() {
	if p.end.IsZero() {
		p.end = time.Now()
	}
}

func (p *phaseRecorder) duration() time.Duration {
	return p.end.Sub(p.start)
}

type participantTotals struct {
	duration  time.Duration
	end       time.Time
	attempts  int
	failures  int
	bytesSent int
	bytesRecv int
	timing    communicationTiming
}

// perParticipant merges retried calls to the same participant.
func (p *phaseRecorder) perParticipant() map[string]*participantTotals {
	totals := map[string]*participantTotals{}
	for _, c := range p.calls {
		t, ok := totals[c.participant]
		if !ok {
			t = &participantTotals{}
			totals[c.participant] = t
		}
		t.duration += c.duration
		t.attempts++
		t.bytesSent += c.bytesSent
		t.bytesRecv += c.bytesRecv
		t.timing.addCall(c)
		if c.failed {
			t.failures++
		}
		if c.end.After(t.end) {
			t.end = c.end
		}
	}
	return totals
}

// barrierWait is the summed time participants waited for the slowest one.
func barrierWait(totals map[string]*participantTotals) time.Duration {
	var lastEnd time.Time
	for _, t := range totals {
		if t.end.After(lastEnd) {
			lastEnd = t.end
		}
	}
	var wait time.Duration
	for _, t := range totals {
		wait += lastEnd.Sub(t.end)
	}
	return wait
}

func (p *phaseRecorder) summary() map[string]any {
	totals := p.perParticipant()

	calls := map[string]any{}
	var bytesSent, bytesRecv int
	var slowest, fastest time.Duration
	var timing communicationTiming
	first := true
	for name, t := range totals {
		call := map[string]any{
			"duration_ms":    toMs(t.duration),
			"bytes_sent":     t.bytesSent,
			"bytes_received": t.bytesRecv,
		}
		if t.attempts > 1 {
			call["attempts"] = t.attempts
		}
		if t.failures > 0 {
			call["failures"] = t.failures
		}
		t.timing.report(call)
		timing.add(t.timing)
		calls[name] = call

		bytesSent += t.bytesSent
		bytesRecv += t.bytesRecv
		if first || t.duration > slowest {
			slowest = t.duration
		}
		if first || t.duration < fastest {
			fastest = t.duration
		}
		first = false
	}

	summary := map[string]any{
		"duration_ms":     toMs(p.duration()),
		"barrier_wait_ms": toMs(barrierWait(totals)),
		"spread_ms":       toMs(slowest - fastest),
		"bytes_sent":      bytesSent,
		"bytes_received":  bytesRecv,
		"calls":           calls,
	}
	timing.report(summary)
	return summary
}

// runTimingTotals accumulates per-round values into run-level metadata.
type runTimingTotals struct {
	rounds               time.Duration
	policyChecks         time.Duration
	policyCheckCount     int
	barrierWait          time.Duration
	coordinationOverhead time.Duration
	bytesSent            int
	bytesRecv            int
	timing               communicationTiming
}

// roundTiming summarises one round: wall time, policy (reasoning) check,
// training phases, gateway coordination overhead and per-participant idle time.
func roundTiming(round, policyCheck time.Duration, phases []*phaseRecorder, totals *runTimingTotals) map[string]any {
	phaseSummaries := map[string]any{}
	phaseOrder := make([]string, 0, len(phases))
	active := map[string]time.Duration{}
	participantTimings := map[string]communicationTiming{}
	var timing communicationTiming
	var phaseSum, wait time.Duration
	var bytesSent, bytesRecv int

	for _, p := range phases {
		perParticipant := p.perParticipant()
		phaseSum += p.duration()
		wait += barrierWait(perParticipant)
		for name, t := range perParticipant {
			active[name] += t.duration
			pt := participantTimings[name]
			pt.add(t.timing)
			participantTimings[name] = pt
			timing.add(t.timing)
			bytesSent += t.bytesSent
			bytesRecv += t.bytesRecv
		}
		phaseSummaries[p.name] = p.summary()
		phaseOrder = append(phaseOrder, p.name)
	}

	names := make([]string, 0, len(active))
	for name := range active {
		names = append(names, name)
	}
	sort.Strings(names)
	participants := map[string]any{}
	for _, name := range names {
		participant := map[string]any{
			"active_ms": toMs(active[name]),
			"idle_ms":   toMs(round - active[name]),
		}
		participantTimings[name].report(participant)
		participants[name] = participant
	}

	overhead := round - policyCheck - phaseSum
	if totals != nil {
		totals.rounds += round
		totals.barrierWait += wait
		totals.coordinationOverhead += overhead
		totals.bytesSent += bytesSent
		totals.bytesRecv += bytesRecv
		totals.timing.add(timing)
	}

	summary := map[string]any{
		"round_ms":                 toMs(round),
		"policy_check_ms":          toMs(policyCheck),
		"coordination_overhead_ms": toMs(overhead),
		"barrier_wait_ms":          toMs(wait),
		"bytes_sent":               bytesSent,
		"bytes_received":           bytesRecv,
		"phase_order":              phaseOrder,
		"phases":                   phaseSummaries,
		"participants":             participants,
	}
	timing.report(summary)
	return summary
}

func toMs(d time.Duration) float64 {
	return math.Round(float64(d.Microseconds())) / 1000
}
