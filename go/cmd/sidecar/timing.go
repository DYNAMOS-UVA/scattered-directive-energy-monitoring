package main

import (
	"context"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/Jorrit05/DYNAMOS/pkg/lib"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
	"github.com/google/uuid"
)

const algorithmTimingTokenKey = "dynamos_algorithm_timing_token"
const replyTimingTimeout = 60 * time.Second

type pendingTiming struct {
	start    time.Time
	expiry   *time.Timer
	upstream time.Duration
	valid    bool
}

type algorithmTimings struct {
	mu     sync.Mutex
	starts map[string]pendingTiming
}

func (t *algorithmTimings) start(data *pb.MicroserviceCommunication) string {
	if data.Metadata == nil {
		data.Metadata = make(map[string]string)
	}
	token := uuid.NewString()
	data.Metadata[algorithmTimingTokenKey] = token
	var upstream time.Duration
	valid := true
	if raw, ok := data.Metadata[lib.AlgorithmTimingMetadataKey]; ok {
		ms, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(ms) || math.IsInf(ms, 0) || ms < 0 || ms >= float64(math.MaxInt64)/float64(time.Millisecond) {
			logger.Sugar().Warnw("Ignoring invalid upstream handler timing", "value", raw)
			valid = false
		} else {
			upstream = time.Duration(ms * float64(time.Millisecond))
		}
	}
	delete(data.Metadata, lib.AlgorithmTimingMetadataKey)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.starts == nil {
		t.starts = make(map[string]pendingTiming)
	}
	t.starts[token] = pendingTiming{
		start: time.Now(), upstream: upstream, valid: valid,
		expiry: time.AfterFunc(replyTimingTimeout, func() {
			if t.cancel(token) {
				logger.Sugar().Warn("Expired unanswered handler timing")
			}
		}),
	}
	return token
}

func (t *algorithmTimings) cancel(token string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if pending, ok := t.starts[token]; ok {
		pending.expiry.Stop()
		delete(t.starts, token)
		return true
	}
	return false
}

func (t *algorithmTimings) finish(data *pb.MicroserviceCommunication, end time.Time) {
	token, ok := data.Metadata[algorithmTimingTokenKey]
	if !ok {
		return
	}
	delete(data.Metadata, algorithmTimingTokenKey)
	delete(data.Metadata, lib.AlgorithmTimingMetadataKey)
	t.mu.Lock()
	pending, found := t.starts[token]
	delete(t.starts, token)
	t.mu.Unlock()
	if found {
		pending.expiry.Stop()
	}
	if !found || end.Before(pending.start) {
		logger.Sugar().Warnw("Ignoring unmatched handler timing reply", "token", token)
		return
	}
	elapsed := end.Sub(pending.start)
	if !pending.valid || elapsed > time.Duration(math.MaxInt64)-pending.upstream {
		logger.Sugar().Warn("Invalid or overflowing accumulated handler timing")
		data.Metadata[lib.AlgorithmTimingMetadataKey] = "invalid"
		return
	}
	data.Metadata[lib.AlgorithmTimingMetadataKey] = strconv.FormatFloat(
		float64(elapsed+pending.upstream)/float64(time.Millisecond), 'f', 6, 64)
}

func (s *serverInstance) forwardToAlgorithm(ctx context.Context, client pb.MicroserviceClient, data *pb.MicroserviceCommunication) (*pb.ContinueReceiving, error) {
	token := s.algorithmTimings.start(data)
	// Python can reply after the dispatch RPC returns.
	response, err := client.SendData(ctx, data)
	if err != nil {
		s.algorithmTimings.cancel(token)
	}
	return response, err
}
