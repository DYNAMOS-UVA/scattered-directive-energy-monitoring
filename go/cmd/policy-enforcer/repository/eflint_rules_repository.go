package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Jorrit05/DYNAMOS/pkg/etcd"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// sharedRulesKey is the etcd key where the Layer-2 shared agreement rules
// (02_agreement_rules.eflint) are stored. The orchestrator writes this key
// at startup and the policy-enforcer reads it for every request evaluation.
const sharedRulesKey = "/policyEnforcer/eflintRules/shared"

// EflintRulesRepository defines access to the Layer-2 shared agreement rules
// (intermediate fact declarations + Extend rules that make the Layer-1 query
// facts derivable). These rules are consortium-shared and runtime-mutable;
// replacing them is the only way to change the derivation logic without a
// codebase release.
type EflintRulesRepository interface {
	// GetSharedAgreementRules retrieves the Layer-2 shared rules eFLINT text.
	// Returns the rules text and a boolean indicating whether the entry exists.
	GetSharedAgreementRules() (string, bool, error)

	// SaveSharedAgreementRules saves the Layer-2 shared rules eFLINT text.
	SaveSharedAgreementRules(text string) error
}

// EtcdEflintRulesRepository implements EflintRulesRepository using etcd.
type EtcdEflintRulesRepository struct {
	client *clientv3.Client
}

// NewEtcdEflintRulesRepository creates a new EtcdEflintRulesRepository.
func NewEtcdEflintRulesRepository(client *clientv3.Client) *EtcdEflintRulesRepository {
	return &EtcdEflintRulesRepository{client: client}
}

// GetSharedAgreementRules retrieves the Layer-2 shared rules from etcd.
func (r *EtcdEflintRulesRepository) GetSharedAgreementRules() (string, bool, error) {
	output, err := etcd.GetValueFromEtcd(r.client, sharedRulesKey, etcd.WithStopOnMissing())
	if err != nil {
		var notFound *etcd.ErrKeyNotFound
		if errors.As(err, &notFound) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("error retrieving shared agreement rules from etcd: %w", err)
	}

	if output == "" {
		return "", false, nil
	}

	return output, true, nil
}

// SaveSharedAgreementRules saves the Layer-2 shared rules to etcd.
func (r *EtcdEflintRulesRepository) SaveSharedAgreementRules(text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := r.client.Put(ctx, sharedRulesKey, text)
	return err
}
