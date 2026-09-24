package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Jorrit05/DYNAMOS/pkg/api"
	"github.com/Jorrit05/DYNAMOS/pkg/etcd"
	clientv3 "go.etcd.io/etcd/client/v3"
)

const providerConfigKeyPrefix = "/policyEnforcer/configs/"

// ProviderConfigRepository defines the interface for retrieving provider validation configurations.
// This abstraction allows determining which validation strategy to use for each data provider.
type ProviderConfigRepository interface {
	// GetProviderConfig retrieves the validation configuration for a specific data provider.
	// Returns the config and a boolean indicating if the config was found.
	GetProviderConfig(provider string) (*api.ProviderValidationConfig, bool, error)

	// SaveProviderConfig writes the validation configuration for the provider.
	// Used when reconciling a format switch so subsequent evaluations resolve
	// the correct AgreementPhraseProvider.
	SaveProviderConfig(provider string, config *api.ProviderValidationConfig) error
}

// EtcdProviderConfigRepository implements ProviderConfigRepository using etcd as the backend.
type EtcdProviderConfigRepository struct {
	client *clientv3.Client
}

// NewEtcdProviderConfigRepository creates a new EtcdProviderConfigRepository.
func NewEtcdProviderConfigRepository(client *clientv3.Client) *EtcdProviderConfigRepository {
	return &EtcdProviderConfigRepository{
		client: client,
	}
}

// GetProviderConfig retrieves the validation configuration for a specific provider from etcd.
func (r *EtcdProviderConfigRepository) GetProviderConfig(provider string) (*api.ProviderValidationConfig, bool, error) {
	key := providerConfigKeyPrefix + provider
	output, err := etcd.GetValueFromEtcd(r.client, key, etcd.WithStopOnMissing())
	if err != nil {
		var notFound *etcd.ErrKeyNotFound
		if errors.As(err, &notFound) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("error retrieving provider config from etcd for provider %s: %w", provider, err)
	}

	if output == "" {
		return nil, false, nil
	}

	var config api.ProviderValidationConfig
	if err := json.Unmarshal([]byte(output), &config); err != nil {
		return nil, false, fmt.Errorf("error unmarshalling provider config for provider %s: %w", provider, err)
	}

	return &config, true, nil
}

// SaveProviderConfig persists the provider's validation configuration to etcd.
func (r *EtcdProviderConfigRepository) SaveProviderConfig(provider string, config *api.ProviderValidationConfig) error {
	if config == nil {
		return fmt.Errorf("provider config is nil")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	key := providerConfigKeyPrefix + provider
	jsonBytes, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal provider config for %s: %w", provider, err)
	}
	if _, err := r.client.Put(ctx, key, string(jsonBytes)); err != nil {
		return fmt.Errorf("error saving provider config to etcd for %s: %w", provider, err)
	}
	return nil
}
