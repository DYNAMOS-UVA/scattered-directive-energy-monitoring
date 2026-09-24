package service

import (
	"context"

	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
)

// ResponseSender defines the interface for sending validation responses.
// This abstraction allows swapping between different message brokers.
type ResponseSender interface {
	SendValidationResponse(ctx context.Context, response *pb.ValidationResponse) error
	SendPolicyUpdate(ctx context.Context, policyUpdate *pb.PolicyUpdate) error
}

// RabbitMQResponseSender implements ResponseSender using RabbitMQ.
type RabbitMQResponseSender struct {
	client pb.RabbitMQClient
}

// NewRabbitMQResponseSender creates a new RabbitMQResponseSender.
func NewRabbitMQResponseSender(client pb.RabbitMQClient) *RabbitMQResponseSender {
	return &RabbitMQResponseSender{
		client: client,
	}
}

// SendValidationResponse sends a validation response via RabbitMQ.
func (s *RabbitMQResponseSender) SendValidationResponse(ctx context.Context, response *pb.ValidationResponse) error {
	_, err := s.client.SendValidationResponse(ctx, response)
	return err
}

// SendPolicyUpdate sends a policy update via RabbitMQ.
func (s *RabbitMQResponseSender) SendPolicyUpdate(ctx context.Context, policyUpdate *pb.PolicyUpdate) error {
	_, err := s.client.SendPolicyUpdate(ctx, policyUpdate)
	return err
}
