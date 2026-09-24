package repository

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Jorrit05/DYNAMOS/pkg/api"
	"github.com/Jorrit05/DYNAMOS/pkg/etcd"
	clientv3 "go.etcd.io/etcd/client/v3"
)

const eflintStateKeyPrefix = "/policyEnforcer/eflint-states/"

// EflintStateRepository defines the interface for retrieving and storing eFLINT execution states.
// This abstraction allows loading and saving eFLINT checkpoint states per organization.
type EflintStateRepository interface {
	// GetEflintState retrieves the saved eFLINT state for a specific provider.
	// Returns the state and a boolean indicating if the state was found.
	GetEflintState(provider string) (*api.EflintSavedState, bool, error)

	// SaveEflintState saves an eFLINT state for a specific provider.
	SaveEflintState(provider string, state *api.EflintSavedState) error
}

// EtcdEflintStateRepository implements EflintStateRepository using etcd as the backend.
type EtcdEflintStateRepository struct {
	client *clientv3.Client
}

// NewEtcdEflintStateRepository creates a new EtcdEflintStateRepository.
func NewEtcdEflintStateRepository(client *clientv3.Client) *EtcdEflintStateRepository {
	return &EtcdEflintStateRepository{
		client: client,
	}
}

// GetEflintState retrieves the saved eFLINT state for a specific provider from etcd.
// When the key is not found (ErrKeyNotFound), it returns (nil, false, nil) so the caller
// can trigger on-demand bootstrapping; other errors are returned as-is.
func (r *EtcdEflintStateRepository) GetEflintState(provider string) (*api.EflintSavedState, bool, error) {
	key := eflintStateKeyPrefix + provider
	output, err := etcd.GetValueFromEtcd(r.client, key)
	if err != nil {
		var notFound *etcd.ErrKeyNotFound
		if errors.As(err, &notFound) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("error retrieving eFLINT state from etcd for provider %s: %w", provider, err)
	}

	if output == "" {
		return nil, false, nil
	}

	var state api.EflintSavedState
	if err := json.Unmarshal([]byte(output), &state); err != nil {
		return nil, false, fmt.Errorf("error unmarshalling eFLINT state for provider %s: %w", provider, err)
	}

	return &state, true, nil
}

// SaveEflintState saves an eFLINT state for a specific provider to etcd.
func (r *EtcdEflintStateRepository) SaveEflintState(provider string, state *api.EflintSavedState) error {
	key := eflintStateKeyPrefix + provider
	return etcd.SaveStructToEtcd(r.client, key, state)
}
