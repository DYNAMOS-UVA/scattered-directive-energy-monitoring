package main

import (
	"context"
	"fmt"

	"github.com/Jorrit05/DYNAMOS/pkg/lib"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
)

func handleIncomingMessages(ctx context.Context, grpcMsg *pb.SideCarMessage) error {
	logger.Debug("start orchestrator handleIncomingMessages")
	ctx, span, err := lib.StartRemoteParentSpan(ctx, serviceName+"/func: handleIncomingMessages", grpcMsg.Traces)
	if err != nil {
		logger.Sugar().Warnf("Error starting span: %v", err)
	}
	defer span.End()

	logger.Sugar().Debugw("Type:", "MessageType", grpcMsg.Type)

	switch grpcMsg.Type {
	case "validationResponse":
		// validationResponse is the flow where a policy Enforcer approved or denied a request
		validationResponse := &pb.ValidationResponse{}
		if err := grpcMsg.Body.UnmarshalTo(validationResponse); err != nil {
			logger.Sugar().Fatalf("Failed to unmarshal message: %v", err)
		}
		handleRequestApproval(ctx, validationResponse, false)

	case "revalidationResponse":
		// I think this is unused
		// validationResponse is the flow where a policy Enforcer approved or denied a request
		validationResponse := &pb.ValidationResponse{}
		if err := grpcMsg.Body.UnmarshalTo(validationResponse); err != nil {
			logger.Sugar().Fatalf("Failed to unmarshal message: %v", err)
		}
		handleRequestApproval(ctx, validationResponse, true)

	case "policyUpdate":
		// policyUpdate is the flow where a contract is changed, and jobs need to be updated
		policyUpdate := &pb.PolicyUpdate{}
		if err := grpcMsg.Body.UnmarshalTo(policyUpdate); err != nil {
			logger.Sugar().Fatalf("Failed to unmarshal message: %v", err)
		}
		policyUpdateMutex.Lock()
		// Look up the corresponding channel in the request map
		jobCompositionRequest, ok := policyUpdateMap[policyUpdate.RequestMetadata.CorrelationId]
		if ok {
			delete(policyUpdateMap, policyUpdate.RequestMetadata.CorrelationId)
			processPolicyUpdate(ctx, jobCompositionRequest, policyUpdate)
		} else {
			logger.Sugar().Error("no job information available for this policy update")
		}
		policyUpdateMutex.Unlock()

	case "agreementUpdate", "sharedRulesUpdate":
		// Acks for both are routed back to awaitPolicyEnforcerAck by correlation ID.
		ack := &pb.PolicyUpdate{}
		if err := grpcMsg.Body.UnmarshalTo(ack); err != nil {
			logger.Sugar().Errorf("Failed to unmarshal %s ack: %v", grpcMsg.Type, err)
			return err
		}
		agreementUpdateMutex.Lock()
		resChan, ok := agreementUpdateMap[ack.RequestMetadata.CorrelationId]
		if ok {
			delete(agreementUpdateMap, ack.RequestMetadata.CorrelationId)
			resChan <- ack
		} else {
			logger.Sugar().Warnf("no pending %s found for correlation ID %s", grpcMsg.Type, ack.RequestMetadata.CorrelationId)
		}
		agreementUpdateMutex.Unlock()

	default:
		logger.Sugar().Errorf("Unknown message type: %s", grpcMsg.Type)
		return fmt.Errorf("unknown message type: %s", grpcMsg.Type)
	}
	logger.Debug("end orchestrator handleIncomingMessages")

	return nil
}
