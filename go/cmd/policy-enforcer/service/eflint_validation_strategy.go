package service

import (
	"context"
	"fmt"

	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/reasoner"
	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/repository"
	"go.uber.org/zap"
)

// EflintAgreementPhraseProvider is the AgreementPhraseProvider for stewards
// whose policy is stored as raw Layer-2 eFLINT phrases under
// /policyEnforcer/eflintModels/{steward}.
//
// GetLayer2Phrases simply returns the stored eFLINT text; ValidateAndPersist
// loads the Layer-2 shared rules first so fact types like `agreement` and
// `steward-supports-archetype` are in scope, then asks the reasoner to
// validate the steward's phrases before storing them.
type EflintAgreementPhraseProvider struct {
	modelRepo repository.EflintModelRepository
	rulesRepo repository.EflintRulesRepository
	reasoner  reasoner.Reasoner
	logger    *zap.Logger
}

// NewEflintAgreementPhraseProvider creates a new eFLINT phrase provider.
// rulesRepo is used to load the Layer-2 shared rules before validation so
// that steward phrases can reference fact types declared in those rules.
func NewEflintAgreementPhraseProvider(
	modelRepo repository.EflintModelRepository,
	rulesRepo repository.EflintRulesRepository,
	r reasoner.Reasoner,
	logger *zap.Logger,
) *EflintAgreementPhraseProvider {
	return &EflintAgreementPhraseProvider{
		modelRepo: modelRepo,
		rulesRepo: rulesRepo,
		reasoner:  r,
		logger:    logger,
	}
}

// Name returns the provider name.
func (p *EflintAgreementPhraseProvider) Name() string {
	return "eflint"
}

// GetLayer2Phrases retrieves the stored eFLINT phrase block for the steward.
func (p *EflintAgreementPhraseProvider) GetLayer2Phrases(steward string) (string, bool, error) {
	text, found, err := p.modelRepo.GetEflintModel(steward)
	if err != nil {
		p.logger.Error("failed to retrieve eFLINT model",
			zap.String("steward", steward),
			zap.Error(err),
		)
		return "", false, fmt.Errorf("failed to retrieve eFLINT model for %s: %w", steward, err)
	}
	return text, found, nil
}

// ValidateAndPersist loads the Layer-2 shared rules (so the steward's phrase
// references to `agreement`, `steward-supports-archetype`, etc. resolve),
// then asks the reasoner to validate the steward's phrases and save them.
func (p *EflintAgreementPhraseProvider) ValidateAndPersist(ctx context.Context, steward string, payload []byte) error {
	sharedRules := ""
	if p.rulesRepo != nil {
		text, found, err := p.rulesRepo.GetSharedAgreementRules()
		if err != nil {
			p.logger.Warn("could not load shared rules for model validation; proceeding without them",
				zap.String("steward", steward),
				zap.Error(err),
			)
		} else if found {
			sharedRules = text
		}
	}

	return p.reasoner.ValidateAndPersistModel(ctx, steward, sharedRules, string(payload))
}
