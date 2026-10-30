package main

import (
	"context"

	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
	"go.opencensus.io/trace"
)

func registerHTTPResponse(correlationID string) (<-chan dataResponse, func()) {
	// A reply racing with cancellation must not block the shared message consumer.
	response := make(chan dataResponse, 1)
	mutex.Lock()
	responseMap[correlationID] = response
	mutex.Unlock()
	return response, func() {
		mutex.Lock()
		delete(responseMap, correlationID)
		mutex.Unlock()
	}
}

func isHttpWaiting(ctx context.Context, msComm *pb.MicroserviceCommunication, correlationId string) bool {
	ctx, span := trace.StartSpan(ctx, "isHttpWaiting")
	defer span.End()
	mutex.Lock()
	dataResponseChan, ok := responseMap[correlationId]
	if ok {
		delete(responseMap, correlationId)
	}
	mutex.Unlock()

	if ok {
		select {
		case dataResponseChan <- dataResponse{response: msComm, localContext: ctx}:
			logger.Sugar().Debugw("Delivered HTTP response", "correlation_id", correlationId)
		default:
			logger.Sugar().Warnw("Discarding duplicate or undeliverable HTTP response", "correlation_id", correlationId)
		}
		return true
	}
	return false
}
