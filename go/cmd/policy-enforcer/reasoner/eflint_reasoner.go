package reasoner

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/eflint"
	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/repository"
)

// EflintReasoner implements the layered Reasoner interface on top of an
// eFLINT instance pool. Each evaluation acquires one pool entry that already
// has the Layer-1 interface policy loaded as its boot model; the layered
// evaluation logic itself lives in eflint_layered.go.
type EflintReasoner struct {
	pool      *eflint.InstancePool
	modelRepo repository.EflintModelRepository
	logger    *zap.Logger
}

// NewEflintReasoner creates a new eFLINT-based reasoner backed by an instance pool.
func NewEflintReasoner(pool *eflint.InstancePool, modelRepo repository.EflintModelRepository, logger *zap.Logger) *EflintReasoner {
	return &EflintReasoner{
		pool:      pool,
		modelRepo: modelRepo,
		logger:    logger,
	}
}

// Name returns the name of this reasoner.
func (r *EflintReasoner) Name() string {
	return "eflint"
}

// IsRunning checks if the eFLINT instance pool is operational.
func (r *EflintReasoner) IsRunning() bool {
	return r.pool.GetTargetSize() > 0
}

// ValidateAndPersistModel validates a raw eFLINT model by sending it to a
// pool instance (so syntactic errors and unbound references surface as
// SendPhrases errors) and, on success, persists it under
// /policyEnforcer/eflintModels/{organization}.
//
// sharedRulesText (Layer 2 shared rules) is loaded onto the instance first so
// that fact types like `agreement` and `steward-supports-archetype` are in
// scope when the per-steward phrases are evaluated.
func (r *EflintReasoner) ValidateAndPersistModel(ctx context.Context, organization string, sharedRulesText string, modelText string) error {
	entry, err := r.pool.Acquire()
	if err != nil {
		return fmt.Errorf("failed to acquire eFLINT instance: %w", err)
	}
	defer r.pool.Release(entry)

	if strings.TrimSpace(sharedRulesText) != "" {
		if _, err := entry.Manager.SendPhrases(sharedRulesText); err != nil {
			r.logger.Error("failed to load shared rules during model validation", zap.Error(err))
			return fmt.Errorf("failed to load shared rules during model validation: %w", err)
		}
	}

	if _, err := entry.Manager.SendPhrases(modelText); err != nil {
		r.logger.Error("invalid eFLINT specification", zap.Error(err))
		return fmt.Errorf("invalid eFLINT specification: %w", err)
	}

	if err := r.modelRepo.SaveEflintModel(organization, modelText); err != nil {
		r.logger.Error("failed to save eFLINT model", zap.Error(err))
		return fmt.Errorf("failed to save eFLINT model: %w", err)
	}

	return nil
}

// ValidateSharedRules verifies that the Layer-2 shared rules eFLINT text
// parses against the Layer-1 baseline of a clean pool instance. The instance
// is released (and restarted to empty) afterwards, so this performs no state
// changes; persistence to etcd is handled by the caller.
func (r *EflintReasoner) ValidateSharedRules(ctx context.Context, rulesText string) error {
	entry, err := r.pool.Acquire()
	if err != nil {
		return fmt.Errorf("failed to acquire eFLINT instance: %w", err)
	}
	defer r.pool.Release(entry)

	if _, err := entry.Manager.SendPhrases(rulesText); err != nil {
		r.logger.Error("invalid eFLINT shared rules", zap.Error(err))
		return fmt.Errorf("invalid eFLINT shared rules: %w", err)
	}

	return nil
}

// Compile-time check that EflintReasoner satisfies the layered Reasoner interface.
var _ Reasoner = (*EflintReasoner)(nil)
