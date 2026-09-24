package service

import (
	"context"
)

// AgreementPhraseProvider produces the Layer-2 per-steward eFLINT phrase block
// for a single data steward. The layered evaluation flow consumes one provider
// per steward (as resolved from the steward's ProviderValidationConfig in etcd):
// the eFLINT-format provider returns the stored phrases verbatim, while the
// legacy-JSON provider loads the JSON agreement and translates it on the fly.
//
// All providers feed the same canonical execution path
// (Layer 1 + Layer 2 shared + per-steward Layer 2 + Layer 3) inside the
// eFLINT reasoner; the only thing that differs between providers is how the
// per-steward Layer-2 phrases are obtained.
type AgreementPhraseProvider interface {
	// Name returns the provider's name for logging / diagnostics
	// (e.g. "legacy", "eflint").
	Name() string

	// GetLayer2Phrases returns the eFLINT phrase block describing the steward's
	// Layer-2 facts (agreement, steward-supports-*, has-relation,
	// relation-allows-*). The boolean indicates whether the steward has an
	// agreement registered at all.
	GetLayer2Phrases(steward string) (string, bool, error)

	// ValidateAndPersist accepts an incoming policy payload, validates it,
	// and persists it through the provider's native repository. Used by the
	// HTTP API for runtime policy updates; does not participate in the
	// per-request validation flow.
	ValidateAndPersist(ctx context.Context, steward string, payload []byte) error
}
