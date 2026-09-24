package service

import (
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
)

// AuthTokenGenerator defines the interface for generating authentication tokens.
type AuthTokenGenerator interface {
	GenerateToken() *pb.Auth
}

// StaticAuthTokenGenerator generates static auth tokens.
// TODO: Replace with a proper token generation implementation.
type StaticAuthTokenGenerator struct{}

// NewStaticAuthTokenGenerator creates a new StaticAuthTokenGenerator.
func NewStaticAuthTokenGenerator() *StaticAuthTokenGenerator {
	return &StaticAuthTokenGenerator{}
}

// GenerateToken generates a static auth token.
// This is a placeholder implementation that should be replaced with proper JWT generation.
func (g *StaticAuthTokenGenerator) GenerateToken() *pb.Auth {
	return &pb.Auth{
		AccessToken:  "1234",
		RefreshToken: "1234",
	}
}
