package main

import (
	"context"
	"fmt"

	"github.com/Jorrit05/DYNAMOS/pkg/lib"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
)

// This function handles incoming messages from the sidecar
// We currently expect the following messages:
// - requestApprovalResponse: a response to a requestApproval from the orchestrator
func handleIncomingMessages(ctx context.Context, grpcMsg *pb.SideCarMessage) error {
	// logger.Debug("start api gateway handleIncomingMessages")
	ctx, span, err := lib.StartRemoteParentSpan(ctx, serviceName+"/func: handleIncomingMessages", grpcMsg.Traces)
	if err != nil {
		logger.Sugar().Warnf("Error starting span: %v", err)
	}
	defer span.End()

	switch grpcMsg.Type {
	case "requestApprovalResponse":
		handleRequestApprovalResponseMessage(ctx, grpcMsg)
	case "policyUpdate":
		logger.Sugar().Debug("Policy update received!")
		handlePolicyUpdateMessage(ctx, grpcMsg)
	default:
		logger.Sugar().Errorf("Unknown message type: %s", grpcMsg.Type)
		return fmt.Errorf("unknown message type: %s", grpcMsg.Type)
	}
	// logger.Debug("end orchestrator handleIncomingMessages")

	return nil
}

// Incoming messages handlers

func handleRequestApprovalResponseMessage(ctx context.Context, grpcMsg *pb.SideCarMessage) {
	// Return the approval response to the client
	requestApprovalResponse := &pb.RequestApprovalResponse{}
	if err := grpcMsg.Body.UnmarshalTo(requestApprovalResponse); err != nil {
		logger.Sugar().Fatalf("Failed to unmarshal message: %v", err)
	}
	requestApprovalMutex.Lock()
	// Look up the corresponding channel in the request map
	approvalRequest, ok := requestApprovalMap[requestApprovalResponse.User.Id]
	if ok {
		approvalRequest <- validation{response: requestApprovalResponse, localContext: ctx}
		delete(requestApprovalMap, requestApprovalResponse.User.Id)
	} else {
		logger.Sugar().Error("No sessions found for this requestApprovalResponse flow: ", requestApprovalResponse, ", ", requestApprovalMap[requestApprovalResponse.User.Id])
	}
	requestApprovalMutex.Unlock()
}

func handlePolicyUpdateMessage(ctx context.Context, grpcMsg *pb.SideCarMessage) {
	policyUpdate := &pb.PolicyUpdate{}
	if err := grpcMsg.Body.UnmarshalTo(policyUpdate); err != nil {
		logger.Sugar().Errorf("Failed to unmarshal message: %v", err)
	}

	policyUpdateMutex.Lock()
	key := policyUpdate.User.Id
	policyUpdateRequest, ok := policyUpdateMap[key]
	done := policyUpdateDone[key]
	policyUpdateMutex.Unlock()

	if ok {
		select {
		case policyUpdateRequest <- PolicyUpdateResponse{response: policyUpdate, localContext: ctx}:
		case <-done:
			logger.Sugar().Warnw("Policy update arrived after training ended", "user_id", key)
		}
	} else {
		logger.Sugar().Error("No job information available for this policyUpdate with key: ", key)
	}
}
