package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/repository"
	"github.com/Jorrit05/DYNAMOS/pkg/api"
	"go.uber.org/zap"
)

// LegacyAgreementPhraseProvider is the AgreementPhraseProvider for stewards
// whose policy is stored as a legacy JSON agreement (api.Agreement) under
// /policyEnforcer/agreements/{steward}.
//
// On every evaluation, the JSON agreement is loaded from etcd and translated
// into a Layer-2 eFLINT phrase block (see TranslateLegacyAgreement) so the
// downstream reasoner sees a canonical layered specification regardless of the
// original storage format.
type LegacyAgreementPhraseProvider struct {
	agreementRepo repository.AgreementRepository
	logger        *zap.Logger
}

// NewLegacyAgreementPhraseProvider creates a new legacy phrase provider.
func NewLegacyAgreementPhraseProvider(
	agreementRepo repository.AgreementRepository,
	logger *zap.Logger,
) *LegacyAgreementPhraseProvider {
	return &LegacyAgreementPhraseProvider{
		agreementRepo: agreementRepo,
		logger:        logger,
	}
}

// Name returns the provider name.
func (p *LegacyAgreementPhraseProvider) Name() string {
	return "legacy"
}

// GetLayer2Phrases loads the JSON agreement from etcd and translates it.
func (p *LegacyAgreementPhraseProvider) GetLayer2Phrases(steward string) (string, bool, error) {
	agreement, found, err := p.agreementRepo.GetAgreement(steward)
	if err != nil {
		p.logger.Error("failed to retrieve legacy agreement",
			zap.String("steward", steward),
			zap.Error(err),
		)
		return "", false, fmt.Errorf("failed to retrieve legacy agreement for %s: %w", steward, err)
	}
	if !found {
		return "", false, nil
	}
	return TranslateLegacyAgreement(agreement), true, nil
}

// ValidateAndPersist validates a JSON legacy agreement against the expected
// api.Agreement structure and, if it conforms, saves it.
//
// Validation rules:
//   - JSON must decode strictly into api.Agreement (unknown fields rejected).
//   - The agreement Name must match the steward derived from the request URL.
//   - Relations, ComputeProviders, and Archetypes must be present (non-empty),
//     since an agreement with none of these would never derive any
//     relation-allows-* or steward-supports-* facts and is therefore useless.
//   - Each Relation must declare at least one requestType or dataSet, and at
//     least one allowedArchetype or allowedComputeProvider.
func (p *LegacyAgreementPhraseProvider) ValidateAndPersist(ctx context.Context, steward string, payload []byte) error {
	agreement, err := decodeStrictAgreement(payload)
	if err != nil {
		p.logger.Error("failed to decode legacy agreement", zap.Error(err))
		return fmt.Errorf("invalid legacy agreement JSON: %w", err)
	}

	if err := validateAgreementStructure(agreement, steward); err != nil {
		p.logger.Error("legacy agreement structure invalid",
			zap.String("steward", steward),
			zap.Error(err),
		)
		return err
	}

	if err := p.agreementRepo.SaveAgreement(steward, agreement); err != nil {
		p.logger.Error("failed to save legacy agreement", zap.Error(err))
		return fmt.Errorf("failed to save legacy agreement: %w", err)
	}

	return nil
}

// decodeStrictAgreement decodes the payload into api.Agreement while rejecting
// unknown fields. This catches typos and stray properties early so an
// orchestrator-side validation error is preferred over silently storing a
// degenerate agreement.
func decodeStrictAgreement(payload []byte) (*api.Agreement, error) {
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	var agreement api.Agreement
	if err := dec.Decode(&agreement); err != nil {
		return nil, err
	}
	return &agreement, nil
}

// validateAgreementStructure performs the semantic checks that decodeStrict
// cannot express. It mirrors the assumptions made by TranslateLegacyAgreement,
// which produces no useful Layer-2 phrases when these fields are empty.
func validateAgreementStructure(agreement *api.Agreement, steward string) error {
	if agreement.Name == "" {
		return fmt.Errorf("agreement field 'name' is required")
	}
	if agreement.Name != steward {
		return fmt.Errorf("agreement name mismatch: expected %s, got %s", steward, agreement.Name)
	}
	if len(agreement.Relations) == 0 {
		return fmt.Errorf("agreement field 'relations' must contain at least one entry")
	}
	if len(agreement.ComputeProviders) == 0 {
		return fmt.Errorf("agreement field 'computeProviders' must contain at least one entry")
	}
	if len(agreement.Archetypes) == 0 {
		return fmt.Errorf("agreement field 'archetypes' must contain at least one entry")
	}
	for relationKey, relation := range agreement.Relations {
		if len(relation.RequestTypes) == 0 && len(relation.DataSets) == 0 {
			return fmt.Errorf("relation %q must declare at least one of 'requestTypes' or 'dataSets'", relationKey)
		}
		if len(relation.AllowedArchetypes) == 0 && len(relation.AllowedComputeProviders) == 0 {
			return fmt.Errorf("relation %q must declare at least one of 'allowedArchetypes' or 'allowedComputeProviders'", relationKey)
		}
	}
	return nil
}
