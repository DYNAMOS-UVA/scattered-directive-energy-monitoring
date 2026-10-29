package main

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/Jorrit05/DYNAMOS/pkg/lib"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
	"github.com/google/uuid"
)

const algorithmTimingTokenKey = "dynamos_algorithm_timing_token"

type algorithmTimings struct {
	mu     sync.Mutex
	starts map[string]time.Time
}

func (t *algorithmTimings) start(data *pb.MicroserviceCommunication) string {
	if data.Metadata == nil {
		data.Metadata = make(map[string]string)
	}
	token := uuid.NewString()
	data.Metadata[algorithmTimingTokenKey] = token
	delete(data.Metadata, lib.AlgorithmTimingMetadataKey)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.starts == nil {
		t.starts = make(map[string]time.Time)
	}
	t.starts[token] = time.Now()
	return token
}

func (t *algorithmTimings) cancel(token string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.starts, token)
}

func (t *algorithmTimings) finish(data *pb.MicroserviceCommunication, end time.Time) {
	token, ok := data.Metadata[algorithmTimingTokenKey]
	if !ok {
		return
	}
	delete(data.Metadata, algorithmTimingTokenKey)
	delete(data.Metadata, lib.AlgorithmTimingMetadataKey)

	t.mu.Lock()
	start, found := t.starts[token]
	delete(t.starts, token)
	t.mu.Unlock()
	if !found || end.Before(start) {
		logger.Sugar().Warnw("Ignoring unmatched algorithm timing reply", "token", token)
		return
	}
	elapsed := end.Sub(start)
	data.Metadata[lib.AlgorithmTimingMetadataKey] = strconv.FormatFloat(
		float64(elapsed)/float64(time.Millisecond), 'f', 6, 64,
	)
}

func (s *serverInstance) forwardToAlgorithm(ctx context.Context, client pb.MicroserviceClient, data *pb.MicroserviceCommunication) (*pb.ContinueReceiving, error) {
	token := s.algorithmTimings.start(data)
	// The supported chain replies through SendData before this RPC returns.
	defer s.algorithmTimings.cancel(token)
	return client.SendData(ctx, data)
}
