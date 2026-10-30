package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Jorrit05/DYNAMOS/pkg/lib"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

type timingClient struct {
	err error
}

func (c timingClient) SendData(context.Context, *pb.MicroserviceCommunication, ...grpc.CallOption) (*pb.ContinueReceiving, error) {
	return &pb.ContinueReceiving{}, c.err
}

func TestAsyncReplyRetainsTimerAfterDispatch(t *testing.T) {
	s := &serverInstance{}
	message := &pb.MicroserviceCommunication{Metadata: map[string]string{
		"existing": "preserved",
	}}
	if _, err := s.forwardToAlgorithm(context.Background(), timingClient{}, message); err != nil {
		t.Fatal(err)
	}
	if len(s.algorithmTimings.starts) != 1 {
		t.Fatal("dispatch acknowledgment must not discard the asynchronous timer")
	}
	token := message.Metadata[algorithmTimingTokenKey]
	pending := s.algorithmTimings.starts[token]
	reply := proto.Clone(message).(*pb.MicroserviceCommunication)
	s.algorithmTimings.finish(reply, pending.start.Add(25*time.Millisecond))
	if reply.Metadata[lib.AlgorithmTimingMetadataKey] != "25.000000" {
		t.Fatalf("async duration missing: %v", reply.Metadata)
	}
	if len(s.algorithmTimings.starts) != 0 || reply.Metadata["existing"] != "preserved" {
		t.Fatal("timer leaked or existing metadata changed")
	}
	if _, ok := reply.Metadata[algorithmTimingTokenKey]; ok {
		t.Fatal("internal token escaped")
	}
	s.algorithmTimings.finish(message, time.Now())
	if _, ok := message.Metadata[lib.AlgorithmTimingMetadataKey]; ok {
		t.Fatal("duplicate reply reused the timer")
	}
}

func TestFailedOrExpiredDispatchCleansUp(t *testing.T) {
	s := &serverInstance{}
	message := &pb.MicroserviceCommunication{}
	if _, err := s.forwardToAlgorithm(context.Background(), timingClient{err: errors.New("unavailable")}, message); err == nil {
		t.Fatal("dispatch error hidden")
	}
	if len(s.algorithmTimings.starts) != 0 {
		t.Fatal("failed dispatch leaked timer")
	}
	token := s.algorithmTimings.start(message)
	s.algorithmTimings.starts[token].expiry.Reset(time.Millisecond)
	deadline := time.After(time.Second)
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for {
		s.algorithmTimings.mu.Lock()
		pending := len(s.algorithmTimings.starts)
		s.algorithmTimings.mu.Unlock()
		if pending == 0 {
			break
		}
		select {
		case <-poll.C:
		case <-deadline:
			t.Fatal("unanswered callback timer did not expire")
		}
	}
	s.algorithmTimings.finish(message, time.Now())
	if _, ok := message.Metadata[lib.AlgorithmTimingMetadataKey]; ok {
		t.Fatal("expired timer produced fabricated measurement")
	}

}

func TestChainedHandlersAccumulateRatherThanOverwrite(t *testing.T) {
	var first, second algorithmTimings
	message := &pb.MicroserviceCommunication{}
	token := first.start(message)
	first.finish(message, first.starts[token].start.Add(20*time.Millisecond))
	token = second.start(message)
	second.finish(message, second.starts[token].start.Add(3*time.Millisecond))
	if message.Metadata[lib.AlgorithmTimingMetadataKey] != "23.000000" {
		t.Fatalf("later relay overwrote earlier training handler: %v", message.Metadata)
	}
}

func TestInvalidUpstreamTimingIsUnmeasured(t *testing.T) {
	var timings algorithmTimings
	message := &pb.MicroserviceCommunication{Metadata: map[string]string{
		lib.AlgorithmTimingMetadataKey: "invalid",
	}}
	token := timings.start(message)
	timings.finish(message, timings.starts[token].start.Add(time.Millisecond))
	if message.Metadata[lib.AlgorithmTimingMetadataKey] != "invalid" {
		t.Fatal("invalid chain duration was replaced by a success-shaped partial measurement")
	}
	var relay algorithmTimings
	token = relay.start(message)
	relay.finish(message, relay.starts[token].start.Add(time.Millisecond))
	if message.Metadata[lib.AlgorithmTimingMetadataKey] != "invalid" {
		t.Fatal("later relay hid invalid upstream timing")
	}
}

func TestAsyncTimingConcurrentAndForeignReplies(t *testing.T) {
	var owner, foreign algorithmTimings
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			message := &pb.MicroserviceCommunication{}
			owner.start(message)
			reply := proto.Clone(message).(*pb.MicroserviceCommunication)
			foreign.finish(reply, time.Now())
			if _, ok := reply.Metadata[lib.AlgorithmTimingMetadataKey]; ok {
				t.Error("foreign sidecar fabricated measurement")
			}
			owner.finish(message, time.Now())
			if _, ok := message.Metadata[lib.AlgorithmTimingMetadataKey]; !ok {
				t.Error("owner lost asynchronous timing")
			}
		}()
	}
	wg.Wait()
	if len(owner.starts) != 0 {
		t.Fatal("concurrent timers leaked")
	}
}
