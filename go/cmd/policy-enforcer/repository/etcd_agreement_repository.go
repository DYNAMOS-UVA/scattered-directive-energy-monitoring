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

const agreementKeyPrefix = "/policyEnforcer/agreements/"

// EtcdAgreementRepository implements AgreementRepository using etcd as the backend.
type EtcdAgreementRepository struct {
	client *clientv3.Client
}

// NewEtcdAgreementRepository creates a new EtcdAgreementRepository.
func NewEtcdAgreementRepository(client *clientv3.Client) *EtcdAgreementRepository {
	return &EtcdAgreementRepository{
		client: client,
	}
}

// GetAgreement retrieves an agreement for a specific data steward from etcd.
func (r *EtcdAgreementRepository) GetAgreement(steward string) (*api.Agreement, bool, error) {
	key := agreementKeyPrefix + steward
	output, err := etcd.GetValueFromEtcd(r.client, key, etcd.WithStopOnMissing())
	if err != nil {
		var notFound *etcd.ErrKeyNotFound
		if errors.As(err, &notFound) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("error retrieving agreement from etcd for steward %s: %w", steward, err)
	}

	if output == "" {
		return nil, false, nil
	}

	var agreement api.Agreement
	if err := json.Unmarshal([]byte(output), &agreement); err != nil {
		return nil, false, fmt.Errorf("error unmarshalling agreement for steward %s: %w", steward, err)
	}

	return &agreement, true, nil
}

// SaveAgreement saves an agreement for a specific data steward to etcd.
func (r *EtcdAgreementRepository) SaveAgreement(steward string, agreement *api.Agreement) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	key := agreementKeyPrefix + steward
	jsonBytes, err := json.Marshal(agreement)
	if err != nil {
		return fmt.Errorf("failed to marshal agreement: %w", err)
	}
	_, err = r.client.Put(ctx, key, string(jsonBytes))
	return err
}

// DeleteAgreement removes the legacy JSON agreement for the steward from etcd.
// Deleting an absent key is a no-op (no error).
func (r *EtcdAgreementRepository) DeleteAgreement(steward string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	key := agreementKeyPrefix + steward
	_, err := r.client.Delete(ctx, key)
	if err != nil {
		return fmt.Errorf("error deleting agreement from etcd for steward %s: %w", steward, err)
	}
	return nil
}
