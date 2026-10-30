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
)

type timingClient struct {
	pb.MicroserviceClient
	reply *pb.MicroserviceCommunication
	err   error
}

func (c *timingClient) SendData(_ context.Context, data *pb.MicroserviceCommunication, _ ...grpc.CallOption) (*pb.ContinueReceiving, error) {
	c.reply = data
	return &pb.ContinueReceiving{}, c.err
}

func TestTimingSurvivesDispatchReturnAndAccumulates(t *testing.T) {
	server := &serverInstance{}
	data := &pb.MicroserviceCommunication{Metadata: map[string]string{lib.AlgorithmTimingMetadataKey: "5"}}
	client := &timingClient{}
	if _, err := server.forwardToAlgorithm(context.Background(), client, data); err != nil {
		t.Fatal(err)
	}
	server.algorithmTimings.finish(client.reply, time.Now().Add(2*time.Millisecond))
	ms, err := strconv.ParseFloat(data.Metadata[lib.AlgorithmTimingMetadataKey], 64)
	if err != nil || ms < 7 {
		t.Fatal("reply lost upstream/local timing", ms, err)
	}
	if _, ok := data.Metadata[algorithmTimingTokenKey]; ok {
		t.Fatal("internal token leaked")
	}
	server.algorithmTimings.finish(data, time.Now())
	if data.Metadata[lib.AlgorithmTimingMetadataKey] == "" {
		t.Fatal("duplicate completion erased valid timing")
	}
	if len(server.algorithmTimings.starts) != 0 {
		t.Fatal("pending timer leaked")
	}
}

func TestTimingErrorAndCancellation(t *testing.T) {
	server := &serverInstance{}
	data := &pb.MicroserviceCommunication{}
	_, err := server.forwardToAlgorithm(context.Background(), &timingClient{err: errors.New("dispatch failed")}, data)
	if err == nil || len(server.algorithmTimings.starts) != 0 {
		t.Fatal("RPC failure leaked timer")
	}
	server.algorithmTimings.finish(data, time.Now())
	if _, ok := data.Metadata[lib.AlgorithmTimingMetadataKey]; ok {
		t.Fatal("failed dispatch gained measured timing")
	}
	for _, raw := range []string{"NaN", "-1", "invalid", "1e100"} {
		reply := &pb.MicroserviceCommunication{Metadata: map[string]string{lib.AlgorithmTimingMetadataKey: raw}}
		token := server.algorithmTimings.start(reply)
		server.algorithmTimings.finish(reply, time.Now())
		if reply.Metadata[lib.AlgorithmTimingMetadataKey] != "invalid" || server.algorithmTimings.cancel(token) {
			t.Fatal("invalid chained timing accepted")
		}
	}
	reply := &pb.MicroserviceCommunication{}
	token := server.algorithmTimings.start(reply)
	if !server.algorithmTimings.cancel(token) {
		t.Fatal("expiry/cancel did not clean up")
	}
	server.algorithmTimings.finish(reply, time.Now())
	if _, ok := reply.Metadata[lib.AlgorithmTimingMetadataKey]; ok {
		t.Fatal("expired reply gained measured timing")
	}
}

func TestConcurrentTimingCorrelation(t *testing.T) {
	var timers algorithmTimings
	var wg sync.WaitGroup
	for range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reply := &pb.MicroserviceCommunication{}
			timers.start(reply)
			timers.finish(reply, time.Now())
			if _, err := strconv.ParseFloat(reply.Metadata[lib.AlgorithmTimingMetadataKey], 64); err != nil {
				t.Error("correlation lost", err)
			}
		}()
	}
	wg.Wait()
	if len(timers.starts) != 0 {
		t.Fatal("pending timers leaked")
	}
}
