package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Jorrit05/DYNAMOS/pkg/etcd"
	clientv3 "go.etcd.io/etcd/client/v3"
)

const eflintModelKeyPrefix = "/policyEnforcer/eflintModels/"

// EflintModelRepository defines the interface for retrieving eFLINT specification text.
// The raw eFLINT file content is stored in etcd by the orchestrator at startup
// (see orchestrator/etcd_config.go) and retrieved here for use with the phrases command.
type EflintModelRepository interface {
	// GetEflintModel retrieves the raw eFLINT specification text for a provider.
	// The modelName typically corresponds to the provider/organization name (e.g., "VU", "UVA").
	// Returns the specification text and a boolean indicating if it was found.
	GetEflintModel(modelName string) (string, bool, error)

	// SaveEflintModel saves the raw eFLINT specification text for a provider.
	SaveEflintModel(modelName string, modelText string) error

	// DeleteEflintModel removes the eFLINT model for the provider from etcd.
	// Used when reconciling a format switch (legacy replaces eFLINT).
	// Deleting a non-existent key is not an error.
	DeleteEflintModel(modelName string) error
}

// EtcdEflintModelRepository implements EflintModelRepository using etcd as the backend.
type EtcdEflintModelRepository struct {
	client *clientv3.Client
}

// NewEtcdEflintModelRepository creates a new EtcdEflintModelRepository.
func NewEtcdEflintModelRepository(client *clientv3.Client) *EtcdEflintModelRepository {
	return &EtcdEflintModelRepository{
		client: client,
	}
}

// GetEflintModel retrieves the raw eFLINT specification text for a provider from etcd.
// The specification is stored at /policyEnforcer/eflintModels/{modelName} by the orchestrator.
func (r *EtcdEflintModelRepository) GetEflintModel(modelName string) (string, bool, error) {
	key := eflintModelKeyPrefix + modelName
	output, err := etcd.GetValueFromEtcd(r.client, key, etcd.WithStopOnMissing())
	if err != nil {
		var notFound *etcd.ErrKeyNotFound
		if errors.As(err, &notFound) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("error retrieving eFLINT model from etcd for %s: %w", modelName, err)
	}

	if output == "" {
		return "", false, nil
	}

	return output, true, nil
}

// SaveEflintModel saves the raw eFLINT specification text for a provider to etcd.
func (r *EtcdEflintModelRepository) SaveEflintModel(modelName string, modelText string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	key := eflintModelKeyPrefix + modelName
	_, err := r.client.Put(ctx, key, modelText)
	return err
}

// DeleteEflintModel removes the eFLINT model for the provider from etcd.
// Deleting an absent key is a no-op (no error).
func (r *EtcdEflintModelRepository) DeleteEflintModel(modelName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	key := eflintModelKeyPrefix + modelName
	_, err := r.client.Delete(ctx, key)
	if err != nil {
		return fmt.Errorf("error deleting eFLINT model from etcd for %s: %w", modelName, err)
	}
	return nil
}
