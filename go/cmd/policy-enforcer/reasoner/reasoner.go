// Package reasoner provides an abstraction layer for policy reasoning engines.
// This allows the policy enforcer to work with different reasoning backends
// (currently eFLINT, with room for Symboleo or other formalisms) behind a
// single layered request-approval interface.
package reasoner

import "context"

// -----------------------------------------------------------------------------
// Layered request-approval evaluation (eFLINT three-layer model)
// -----------------------------------------------------------------------------

// RequestApprovalParams describes a single layered request-approval evaluation.
// Layer 1 (interface policy) is already loaded by the reasoner's pool baseline.
// SharedRules supplies Layer 2 shared (consortium-wide) derivation rules; the
// per-steward Layer-2 phrases live in StewardPhrases. The Requester and
// Stewards drive the Layer-3 facts the reasoner builds for this evaluation.
type RequestApprovalParams struct {
	Requester      string            // The requester (Layer 3: +requester(...))
	Stewards       []string          // Stewards from the RequestApproval (Layer 3: +requested-steward(...))
	SharedRules    string            // Layer 2 shared agreement rules
	StewardPhrases map[string]string // Steward -> Layer 2 per-steward phrases (missing entries imply no agreement)
}

// StewardDecision is the eFLINT-derived per-steward outcome for one
// request-approval evaluation.
type StewardDecision struct {
	Permitted        bool     // permitted-at-steward(requester, steward) holds
	Archetypes       []string // values that satisfy valid-archetype(requester, steward, _)
	ComputeProviders []string // values that satisfy valid-compute-provider(requester, steward, _)
	Reason           string   // explanation when not permitted (for diagnostics)
}

// RequestApprovalResult is the aggregate outcome of a layered evaluation.
type RequestApprovalResult struct {
	PermittedRequest bool                       // permitted-request(requester) holds
	PerSteward       map[string]StewardDecision // one entry per requested steward
}

// -----------------------------------------------------------------------------
// Steward-clause introspection (read-only; used by the policy-enforcer HTTP API)
// -----------------------------------------------------------------------------

// IntrospectStewardClausesParams asks the reasoner to load Layer 1 + Layer 2
// (shared + per-steward) only, with no Layer 3, so the resulting facts can be
// inspected. It is intended for policy-engineering use (e.g. an HTTP API that
// answers "what does this steward's agreement permit?").
//
// Requesters lists the requester identities to assert as minimal Layer-3 facts
// (`+requester(X).`) before running the introspect queries. This is required
// because eFLINT generative queries enumerate only explicitly grounded fact
// instances, and requester atoms are not present in a Layer-2-only session.
// Provide every requester whose relation clauses should appear in the result;
// leave empty to return only steward-level supported facts (no relations).
type IntrospectStewardClausesParams struct {
	Steward        string   // The data steward being introspected
	SharedRules    string   // Layer 2 shared agreement rules (may be empty)
	StewardPhrases string   // Layer 2 per-steward phrases (must be non-empty)
	Requesters     []string // Requesters to ground before querying (see above)
}

// RequesterClauses captures the "what is this requester allowed to do at this
// steward" snapshot derived from the relation-allows-* facts in Layer 2.
type RequesterClauses struct {
	Requester        string   `json:"requester"`
	RequestTypes     []string `json:"request_types,omitempty"`
	Datasets         []string `json:"datasets,omitempty"`
	Archetypes       []string `json:"archetypes,omitempty"`
	ComputeProviders []string `json:"compute_providers,omitempty"`
}

// StewardClauses is the read-only summary of a steward's Layer-2 specification.
type StewardClauses struct {
	Steward                   string             `json:"steward"`
	SupportedArchetypes       []string           `json:"supported_archetypes,omitempty"`
	SupportedComputeProviders []string           `json:"supported_compute_providers,omitempty"`
	Relations                 []RequesterClauses `json:"relations,omitempty"`
}

// -----------------------------------------------------------------------------
// Reasoner Interface
// -----------------------------------------------------------------------------

// Reasoner defines the interface for policy reasoning engines under the
// layered eFLINT design. The single canonical evaluation entrypoint is
// EvaluateRequestApproval; ValidateAndPersistModel is used by the policy
// update path to validate raw policy text before it is stored.
type Reasoner interface {
	// EvaluateRequestApproval runs a single layered evaluation across all
	// requested stewards. It loads the Layer 2 shared rules and per-steward
	// agreements onto a single pool instance, builds Layer 3 from the
	// requester / requested-steward inputs, queries the Layer 1 query facts,
	// and (for each permitted steward) fires the submit-data-request act so
	// the data-request fact and obligated-log duty materialise on the
	// reasoner's execution graph.
	EvaluateRequestApproval(ctx context.Context, params RequestApprovalParams) (*RequestApprovalResult, error)

	// IntrospectStewardClauses loads Layer 1 + Layer 2 (shared + per-steward)
	// onto a clean pool instance and returns the steward-supports-* and
	// relation-allows-* facts derived from the steward's agreement, grouped
	// per requester. No Layer 3 is pushed and no Acts are fired, so this is
	// safe to expose as a policy-engineering introspection endpoint.
	IntrospectStewardClauses(ctx context.Context, params IntrospectStewardClausesParams) (*StewardClauses, error)

	// IsRunning returns whether the reasoner is ready to process requests.
	IsRunning() bool

	// ValidateAndPersistModel validates a raw policy text for the given
	// steward by handing it to the reasoner backend and, if it parses,
	// persisting it. Used by the policy-update HTTP/RabbitMQ paths.
	//
	// sharedRulesText is the Layer-2 shared rules that must be loaded onto the
	// eFLINT instance before the per-steward phrases are sent, because the
	// steward phrases reference fact types declared in the shared rules
	// (agreement, steward-supports-archetype, has-relation, etc.).
	// Pass an empty string when no shared rules are available yet.
	ValidateAndPersistModel(ctx context.Context, organization string, sharedRulesText string, modelText string) error

	// ValidateSharedRules validates the Layer-2 shared rules eFLINT text by
	// sending it to a clean pool instance on top of the Layer-1 baseline.
	// The instance is restarted on release, so no state leaks; persistence
	// is the caller's responsibility (see ValidationService).
	ValidateSharedRules(ctx context.Context, rulesText string) error

	// Name returns the name/type of this reasoner (e.g., "eflint").
	Name() string
}

// -----------------------------------------------------------------------------
// Optional Extended Interfaces
// -----------------------------------------------------------------------------

// StateManager is an optional interface for reasoners that support state
// management (export/import). The eFLINT state HTTP API uses this for the
// state-persistence proof-of-concept; it is independent of the layered
// request-approval flow.
type StateManager interface {
	// ExportState exports the current state of the reasoner.
	ExportState(ctx context.Context) ([]byte, error)

	// ImportState imports a previously exported state.
	ImportState(ctx context.Context, state []byte) error
}
