package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
)

func deliverWithinDeadline(t *testing.T, id string, message *pb.MicroserviceCommunication) bool {
	t.Helper()
	done := make(chan bool, 1)
	go func() { done <- isHttpWaiting(context.Background(), message, id) }()
	select {
	case delivered := <-done:
		return delivered
	case <-time.After(time.Second):
		t.Fatal("late/duplicate reply blocked the agent consumer")
		return false
	}
}

func TestHTTPResponseImmediateReplyAndCleanup(t *testing.T) {
	id := t.Name()
	response, cleanup := registerHTTPResponse(id)
	defer cleanup()
	message := &pb.MicroserviceCommunication{Result: []byte("immediate")}
	if !deliverWithinDeadline(t, id, message) {
		t.Fatal("reply dispatched before HTTP select was lost")
	}
	if got := <-response; got.response != message {
		t.Fatal("wrong response")
	}
	if deliverWithinDeadline(t, id, message) {
		t.Fatal("duplicate reply reused a consumed correlation")
	}
	mutex.Lock()
	_, exists := responseMap[id]
	mutex.Unlock()
	if exists {
		t.Fatal("consumed response leaked correlation")
	}
}

func TestHTTPResponseAfterTimeoutDoesNotBlockNextRequest(t *testing.T) {
	id := t.Name()
	_, cleanup := registerHTTPResponse(id)
	cleanup()
	if deliverWithinDeadline(t, id, &pb.MicroserviceCommunication{}) {
		t.Fatal("expired correlation was retained")
	}
	next, cleanupNext := registerHTTPResponse(id + "-next")
	defer cleanupNext()
	message := &pb.MicroserviceCommunication{Result: []byte("next")}
	if !deliverWithinDeadline(t, id+"-next", message) || (<-next).response != message {
		t.Fatal("late response prevented later successful request")
	}
}

func TestHTTPResponseAbandonedUnbufferedChannelCannotBlockConsumer(t *testing.T) {
	id := t.Name()
	mutex.Lock()
	responseMap[id] = make(chan dataResponse)
	mutex.Unlock()
	if !deliverWithinDeadline(t, id, &pb.MicroserviceCommunication{}) {
		t.Fatal("abandoned correlation was not handled")
	}
	mutex.Lock()
	_, exists := responseMap[id]
	mutex.Unlock()
	if exists {
		t.Fatal("abandoned correlation was not removed")
	}
}

func TestHTTPResponseCancellationRace(t *testing.T) {
	for index := range 200 {
		id := fmt.Sprintf("%s-%d", t.Name(), index)
		_, cleanup := registerHTTPResponse(id)
		var wg sync.WaitGroup
		wg.Add(3)
		for range 2 {
			go func() {
				defer wg.Done()
				isHttpWaiting(context.Background(), &pb.MicroserviceCommunication{}, id)
			}()
		}
		go func() { defer wg.Done(); cleanup() }()
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("cancellation/duplicate race blocked delivery")
		}
		mutex.Lock()
		_, exists := responseMap[id]
		mutex.Unlock()
		if exists {
			t.Fatal("cancellation leaked correlation")
		}
	}
}
