package main

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Jorrit05/DYNAMOS/pkg/lib"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

type timingMicroserviceClient struct {
	send func(*pb.MicroserviceCommunication) (*pb.ContinueReceiving, error)
}

func (c timingMicroserviceClient) SendData(_ context.Context, data *pb.MicroserviceCommunication, _ ...grpc.CallOption) (*pb.ContinueReceiving, error) {
	return c.send(data)
}

func TestAlgorithmTimingRoundTrip(t *testing.T) {
	var timings algorithmTimings
	msg := &pb.MicroserviceCommunication{Metadata: map[string]string{
		"accuracy": "0.9", lib.AlgorithmTimingMetadataKey: "stale",
	}}
	token := timings.start(msg)
	if _, ok := msg.Metadata[lib.AlgorithmTimingMetadataKey]; ok {
		t.Fatal("handoff retained a stale measurement")
	}
	start := time.Now()
	timings.starts[token] = start

	// The callback is a separately decoded protobuf message, as with Python gRPC.
	wire, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	reply := &pb.MicroserviceCommunication{}
	if err := proto.Unmarshal(wire, reply); err != nil {
		t.Fatal(err)
	}
	timings.finish(reply, start.Add(25*time.Millisecond))
	if got := reply.Metadata[lib.AlgorithmTimingMetadataKey]; got != "25.000000" {
		t.Fatalf("algorithm milliseconds = %q, want 25.000000", got)
	}
	if reply.Metadata["accuracy"] != "0.9" {
		t.Fatal("existing metadata was changed")
	}
	if _, ok := reply.Metadata[algorithmTimingTokenKey]; ok {
		t.Fatal("internal token escaped in the reply")
	}
	if len(timings.starts) != 0 {
		t.Fatal("completed measurement was retained")
	}
}

func TestAlgorithmTimingMissingInvalidAndForeignTokens(t *testing.T) {
	for _, token := range []string{"", "not-a-token", "another-sidecar-token"} {
		t.Run(token, func(t *testing.T) {
			var timings algorithmTimings
			msg := &pb.MicroserviceCommunication{Metadata: map[string]string{
				algorithmTimingTokenKey:        token,
				lib.AlgorithmTimingMetadataKey: "stale",
				"other":                        "preserved",
			}}
			timings.finish(msg, time.Now())
			if _, ok := msg.Metadata[lib.AlgorithmTimingMetadataKey]; ok {
				t.Fatal("invalid timing produced a measurement")
			}
			if _, ok := msg.Metadata[algorithmTimingTokenKey]; ok {
				t.Fatal("invalid token escaped")
			}
			if msg.Metadata["other"] != "preserved" {
				t.Fatal("existing metadata was changed")
			}
		})
	}
	var timings algorithmTimings
	timings.finish(&pb.MicroserviceCommunication{}, time.Now())
	unchanged := &pb.MicroserviceCommunication{Metadata: map[string]string{
		lib.AlgorithmTimingMetadataKey: "12",
	}}
	timings.finish(unchanged, time.Now())
	if unchanged.Metadata[lib.AlgorithmTimingMetadataKey] != "12" {
		t.Fatal("non-job sidecar changed an upstream measurement")
	}
}

func TestForwardToAlgorithmCleansUp(t *testing.T) {
	for _, outcome := range []string{"reply", "no-reply", "error", "dropped-token"} {
		t.Run(outcome, func(t *testing.T) {
			s := &serverInstance{}
			msg := &pb.MicroserviceCommunication{}
			client := timingMicroserviceClient{send: func(data *pb.MicroserviceCommunication) (*pb.ContinueReceiving, error) {
				switch outcome {
				case "error":
					return nil, errors.New("algorithm unavailable")
				case "no-reply":
					return &pb.ContinueReceiving{}, nil
				case "dropped-token":
					s.algorithmTimings.finish(&pb.MicroserviceCommunication{}, time.Now())
				default:
					reply := proto.Clone(data).(*pb.MicroserviceCommunication)
					s.algorithmTimings.finish(reply, time.Now())
					if _, err := strconv.ParseFloat(reply.Metadata[lib.AlgorithmTimingMetadataKey], 64); err != nil {
						t.Fatalf("missing callback duration: %v", err)
					}
				}
				return &pb.ContinueReceiving{}, nil
			}}
			_, err := s.forwardToAlgorithm(context.Background(), client, msg)
			if (err != nil) != (outcome == "error") {
				t.Fatalf("forward error = %v, outcome = %s", err, outcome)
			}
			if len(s.algorithmTimings.starts) != 0 {
				t.Fatal("no-reply/error request leaked a timer")
			}
		})
	}
}

func TestAlgorithmTimingConcurrentAndIsolated(t *testing.T) {
	var timings algorithmTimings
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			msg := &pb.MicroserviceCommunication{}
			token := timings.start(msg)
			reply := proto.Clone(msg).(*pb.MicroserviceCommunication)
			timings.finish(reply, time.Now())
			timings.cancel(token)
			if _, ok := reply.Metadata[lib.AlgorithmTimingMetadataKey]; !ok {
				t.Error("concurrent reply lost its measurement")
			}
		}()
	}
	wg.Wait()
	if len(timings.starts) != 0 {
		t.Fatal("concurrent requests leaked timers")
	}
}

func TestAlgorithmTimingRejectsForeignAndRepeatedReplies(t *testing.T) {
	var owner, other algorithmTimings
	msg := &pb.MicroserviceCommunication{}
	token := owner.start(msg)
	foreign := proto.Clone(msg).(*pb.MicroserviceCommunication)
	other.finish(foreign, time.Now())
	if _, ok := foreign.Metadata[lib.AlgorithmTimingMetadataKey]; ok {
		t.Fatal("another sidecar measured a foreign token")
	}
	if _, ok := owner.starts[token]; !ok {
		t.Fatal("another sidecar consumed the owner's timer")
	}
	reply := proto.Clone(msg).(*pb.MicroserviceCommunication)
	owner.finish(reply, time.Now())
	repeated := proto.Clone(msg).(*pb.MicroserviceCommunication)
	owner.finish(repeated, time.Now())
	if _, ok := repeated.Metadata[lib.AlgorithmTimingMetadataKey]; ok {
		t.Fatal("duplicate reply reused a completed measurement")
	}
}
