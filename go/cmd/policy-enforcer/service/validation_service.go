package service

import (
	"context"
	"fmt"
	"time"

	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/reasoner"
	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/repository"
	"github.com/Jorrit05/DYNAMOS/pkg/api"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
	"go.uber.org/zap"
)

// AllowedClausesTiming contains per-stage latency measurements for
// GetAllowedClausesForSteward. Durations are expressed in milliseconds.
type AllowedClausesTiming struct {
	ResolveProviderMs    int64
	LoadStewardPhrasesMs int64
	LoadSharedRulesMs    int64
	ReasonerIntrospectMs int64
	FilterRelationsMs    int64
	ServiceTotalMs       int64
}

// ValidationService orchestrates request-approval validation under the
// layered eFLINT design.
//
// A single evaluation now runs over all requested stewards on one eFLINT
// pool instance: per-steward Layer-2 phrases are gathered through
// AgreementPhraseProviders (eFLINT-format vs legacy-JSON), the Layer-2 shared
// rules come from EflintRulesRepository, and the reasoner builds Layer 3
// from the requester / requested-steward inputs. The resulting per-steward
// decisions are mapped back to the existing pb.ValidationResponse wire format
// so the orchestrator side does not need to change.
type ValidationService struct {
	providerConfigRepo repository.ProviderConfigRepository
	rulesRepo          repository.EflintRulesRepository
	agreementRepo      repository.AgreementRepository
	eflintModelRepo    repository.EflintModelRepository
	legacyProvider     AgreementPhraseProvider
	eflintProvider     AgreementPhraseProvider // optional: nil if eFLINT-format provider not configured

	reasoner      reasoner.Reasoner
	authGenerator AuthTokenGenerator
	logger        *zap.Logger
}

// ValidationServiceConfig holds the configuration for creating a ValidationService.
type ValidationServiceConfig struct {
	ProviderConfigRepo repository.ProviderConfigRepository
	RulesRepo          repository.EflintRulesRepository
	// AgreementRepo and EflintModelRepo are used by the format-switch
	// reconciliation in ValidateAndPersistAgreement to delete the now-stale
	// representation of a steward's policy. They are optional: if nil,
	// the old-key cleanup step is skipped.
	AgreementRepo   repository.AgreementRepository
	EflintModelRepo repository.EflintModelRepository
	LegacyProvider  AgreementPhraseProvider
	EflintProvider  AgreementPhraseProvider // optional
	Reasoner        reasoner.Reasoner
	AuthGenerator   AuthTokenGenerator
	Logger          *zap.Logger
}

// NewValidationServiceWithConfig creates a ValidationService with the given configuration.
func NewValidationServiceWithConfig(cfg ValidationServiceConfig) *ValidationService {
	return &ValidationService{
		providerConfigRepo: cfg.ProviderConfigRepo,
		rulesRepo:          cfg.RulesRepo,
		agreementRepo:      cfg.AgreementRepo,
		eflintModelRepo:    cfg.EflintModelRepo,
		legacyProvider:     cfg.LegacyProvider,
		eflintProvider:     cfg.EflintProvider,
		reasoner:           cfg.Reasoner,
		authGenerator:      cfg.AuthGenerator,
		logger:             cfg.Logger,
	}
}

// ValidateRequest processes a request approval and returns a validation response.
func (s *ValidationService) ValidateRequest(ctx context.Context, request *pb.RequestApproval) *pb.ValidationResponse {
	s.logger.Debug("Starting request validation",
		zap.String("user", request.User.UserName),
		zap.Strings("dataProviders", request.DataProviders),
	)

	response := s.buildInitialResponse(request)

	// Phase 1: collect Layer-2 phrases per steward (no eFLINT calls yet).
	stewardPhrases, missingStewards := s.collectStewardPhrases(request.DataProviders)
	for _, missing := range missingStewards {
		response.InvalidDataproviders = append(response.InvalidDataproviders, missing)
		s.logger.Debug("steward has no agreement; marking invalid",
			zap.String("steward", missing),
		)
	}

	// Phase 2: gather Layer-2 shared rules.
	sharedRules, err := s.loadSharedRules()
	if err != nil {
		s.logger.Error("failed to load Layer-2 shared rules; aborting evaluation",
			zap.Error(err),
		)
		s.markAllInvalid(request.DataProviders, response, "shared rules unavailable")
		return response
	}

	// Phase 3: run a single layered evaluation across all requested stewards.
	var eval *reasoner.RequestApprovalResult
	if len(stewardPhrases) > 0 {
		var err error
		eval, err = s.reasoner.EvaluateRequestApproval(ctx, reasoner.RequestApprovalParams{
			Requester:      request.User.UserName,
			Stewards:       request.DataProviders,
			SharedRules:    sharedRules,
			StewardPhrases: stewardPhrases,
		})
		if err != nil {
			s.logger.Error("eFLINT layered evaluation failed",
				zap.Error(err),
			)
			s.markAllInvalid(request.DataProviders, response, "reasoner error")
			return response
		}
		s.applyEvaluation(eval, request, response)
	}

	response.RequestApproved = eval != nil && eval.PermittedRequest
	if response.RequestApproved {
		response.Auth = s.authGenerator.GenerateToken()
	}

	s.logger.Info("Request validation completed",
		zap.String("user", request.User.UserName),
		zap.Bool("approved", response.RequestApproved),
		zap.Int("validProviders", len(response.ValidDataproviders)),
		zap.Int("invalidProviders", len(response.InvalidDataproviders)),
	)

	return response
}

// buildInitialResponse creates the initial ValidationResponse with base fields.
func (s *ValidationService) buildInitialResponse(request *pb.RequestApproval) *pb.ValidationResponse {
	response := &pb.ValidationResponse{
		Type:        "validationResponse",
		RequestType: request.Type,
		User: &pb.User{
			Id:       request.User.Id,
			UserName: request.User.UserName,
		},
		RequestApproved:      false,
		ValidArchetypes:      &pb.UserArchetypes{Archetypes: make(map[string]*pb.UserAllowedArchetypes)},
		Options:              make(map[string]bool),
		ValidDataproviders:   make(map[string]*pb.DataProvider),
		InvalidDataproviders: []string{},
	}

	if len(request.Options) > 0 {
		response.Options = request.Options
	}

	return response
}

// collectStewardPhrases iterates over the requested data providers, resolves
// the steward's AgreementPhraseProvider from its ProviderValidationConfig,
// and returns the Layer-2 phrase block per steward. Stewards without an
// agreement (or with a retrieval error) are returned in the second slice so
// the caller can mark them invalid.
func (s *ValidationService) collectStewardPhrases(dataProviders []string) (map[string]string, []string) {
	phrases := make(map[string]string, len(dataProviders))
	var missing []string

	for _, steward := range dataProviders {
		provider := s.resolveProvider(steward)
		text, found, err := provider.GetLayer2Phrases(steward)
		if err != nil {
			s.logger.Warn("failed to load Layer-2 phrases for steward; treating as missing",
				zap.String("steward", steward),
				zap.String("provider", provider.Name()),
				zap.Error(err),
			)
			missing = append(missing, steward)
			continue
		}
		if !found {
			missing = append(missing, steward)
			continue
		}
		phrases[steward] = text
	}

	return phrases, missing
}

// loadSharedRules reads the Layer-2 shared rules from etcd. An empty result is
// allowed (the layered evaluation can still run against the per-steward
// phrases) but is logged as a warning since the system is not configured as
// expected.
func (s *ValidationService) loadSharedRules() (string, error) {
	if s.rulesRepo == nil {
		return "", nil
	}
	text, found, err := s.rulesRepo.GetSharedAgreementRules()
	if err != nil {
		return "", fmt.Errorf("retrieving shared rules: %w", err)
	}
	if !found {
		s.logger.Warn("Layer-2 shared rules are missing in etcd; query facts may not be derivable")
	}
	return text, nil
}

// resolveProvider determines which AgreementPhraseProvider to use for a
// steward. The provider config in etcd selects between eFLINT-format phrases
// and legacy JSON; in either case the result feeds the same canonical
// layered execution path.
func (s *ValidationService) resolveProvider(steward string) AgreementPhraseProvider {
	if s.providerConfigRepo == nil {
		return s.legacyProvider
	}

	config, found, err := s.providerConfigRepo.GetProviderConfig(steward)
	if err != nil {
		s.logger.Warn("failed to retrieve provider config; defaulting to legacy",
			zap.String("steward", steward),
			zap.Error(err),
		)
		return s.legacyProvider
	}
	if !found {
		return s.legacyProvider
	}

	if config.ValidationStrategy == api.ValidationStrategyEflint && s.eflintProvider != nil {
		return s.eflintProvider
	}
	return s.legacyProvider
}

// applyEvaluation maps the reasoner's per-steward decisions onto the
// pb.ValidationResponse. Stewards with permitted-at-steward = true land in
// ValidDataproviders together with their matched archetypes / compute
// providers; everyone else is added to InvalidDataproviders.
func (s *ValidationService) applyEvaluation(
	eval *reasoner.RequestApprovalResult,
	request *pb.RequestApproval,
	response *pb.ValidationResponse,
) {
	if eval == nil {
		return
	}
	response.ValidArchetypes.UserName = request.User.UserName
	for _, steward := range request.DataProviders {
		decision, ok := eval.PerSteward[steward]
		if !ok {
			continue
		}
		if !decision.Permitted {
			if !containsString(response.InvalidDataproviders, steward) {
				response.InvalidDataproviders = append(response.InvalidDataproviders, steward)
			}
			s.logger.Debug("steward marked invalid by reasoner",
				zap.String("steward", steward),
				zap.String("reason", decision.Reason),
			)
			continue
		}
		response.ValidArchetypes.Archetypes[steward] = &pb.UserAllowedArchetypes{
			Archetypes: decision.Archetypes,
		}
		response.ValidDataproviders[steward] = &pb.DataProvider{
			Archetypes:       decision.Archetypes,
			ComputeProviders: decision.ComputeProviders,
		}
	}
}

// markAllInvalid is invoked when an unrecoverable error short-circuits the
// evaluation (no shared rules, reasoner error). Every requested steward is
// marked invalid so the orchestrator does not see a partial success.
func (s *ValidationService) markAllInvalid(dataProviders []string, response *pb.ValidationResponse, reason string) {
	for _, steward := range dataProviders {
		if containsString(response.InvalidDataproviders, steward) {
			continue
		}
		response.InvalidDataproviders = append(response.InvalidDataproviders, steward)
		s.logger.Debug("steward marked invalid",
			zap.String("steward", steward),
			zap.String("reason", reason),
		)
	}
}

// ValidateAndPersistAgreement validates and stores a steward's agreement in
// the requested format ("json" → legacy provider, "eflint" → eFLINT provider).
// On a successful save it reconciles /policyEnforcer/configs/{steward} so the
// steward's ValidationStrategy matches the new format and deletes the now-
// obsolete representation under the alternate etcd prefix.
func (s *ValidationService) ValidateAndPersistAgreement(ctx context.Context, steward, format string, payload []byte) error {
	provider, strategy, err := s.providerForFormat(format)
	if err != nil {
		return err
	}

	s.logger.Info("Validating and persisting agreement",
		zap.String("steward", steward),
		zap.String("provider", provider.Name()),
		zap.String("format", format),
	)

	if err := provider.ValidateAndPersist(ctx, steward, payload); err != nil {
		return err
	}

	s.reconcileProviderConfig(steward, strategy)
	return nil
}

// providerForFormat picks the AgreementPhraseProvider that matches the input
// format. Format values come from the orchestrator HTTP layer
// (Content-Type-derived) and are checked here so the policy-enforcer never
// silently routes a payload to the wrong validator.
func (s *ValidationService) providerForFormat(format string) (AgreementPhraseProvider, string, error) {
	switch format {
	case api.ValidationStrategyEflint:
		if s.eflintProvider == nil {
			return nil, "", fmt.Errorf("eflint agreement provider is not configured")
		}
		return s.eflintProvider, api.ValidationStrategyEflint, nil
	case api.ValidationStrategyLegacy, "json", "":
		// "" preserves backwards-compatibility with callers that do not yet
		// populate the new Format field; "json" is the orchestrator HTTP
		// label that maps to the legacy provider.
		if s.legacyProvider == nil {
			return nil, "", fmt.Errorf("legacy agreement provider is not configured")
		}
		return s.legacyProvider, api.ValidationStrategyLegacy, nil
	default:
		return nil, "", fmt.Errorf("unsupported agreement format %q", format)
	}
}

// reconcileProviderConfig updates the steward's provider config (and removes
// the previous-format etcd entry) after a successful save. Failures are
// logged but not returned: the new agreement is already persisted, and
// the next evaluation can recover from a partial reconciliation on its own.
func (s *ValidationService) reconcileProviderConfig(steward, newStrategy string) {
	if s.providerConfigRepo == nil {
		return
	}

	existing, found, err := s.providerConfigRepo.GetProviderConfig(steward)
	if err != nil {
		s.logger.Warn("failed to load existing provider config; skipping reconciliation",
			zap.String("steward", steward),
			zap.Error(err),
		)
		return
	}

	previousStrategy := ""
	if found && existing != nil {
		previousStrategy = existing.ValidationStrategy
	}

	if found && previousStrategy == newStrategy {
		return
	}

	newConfig := &api.ProviderValidationConfig{
		Name:               steward,
		ValidationStrategy: newStrategy,
	}
	if found && existing != nil {
		newConfig.AgreementLocation = existing.AgreementLocation
	}

	if err := s.providerConfigRepo.SaveProviderConfig(steward, newConfig); err != nil {
		s.logger.Error("failed to update provider config after format switch",
			zap.String("steward", steward),
			zap.String("strategy", newStrategy),
			zap.Error(err),
		)
		return
	}

	if previousStrategy == "" {
		return
	}

	s.deleteObsoleteFormatKey(steward, previousStrategy)
}

// deleteObsoleteFormatKey removes the steward's stale representation in the
// previously-active format after the format has been switched. Errors are
// logged because the new agreement is the source of truth either way.
func (s *ValidationService) deleteObsoleteFormatKey(steward, previousStrategy string) {
	switch previousStrategy {
	case api.ValidationStrategyLegacy:
		if s.agreementRepo == nil {
			return
		}
		if err := s.agreementRepo.DeleteAgreement(steward); err != nil {
			s.logger.Warn("failed to delete obsolete legacy agreement after format switch",
				zap.String("steward", steward),
				zap.Error(err),
			)
		}
	case api.ValidationStrategyEflint:
		if s.eflintModelRepo == nil {
			return
		}
		if err := s.eflintModelRepo.DeleteEflintModel(steward); err != nil {
			s.logger.Warn("failed to delete obsolete eFLINT model after format switch",
				zap.String("steward", steward),
				zap.Error(err),
			)
		}
	}
}

// ValidateAndPersistSharedRules validates the Layer-2 shared rules eFLINT
// text by parsing it on a clean pool instance and, on success, persists it
// under /policyEnforcer/eflintRules/shared. The orchestrator triggers a
// re-evaluation of every running job after this succeeds.
func (s *ValidationService) ValidateAndPersistSharedRules(ctx context.Context, payload []byte) error {
	if s.rulesRepo == nil {
		return fmt.Errorf("shared rules repository is not configured")
	}
	if s.reasoner == nil {
		return fmt.Errorf("reasoner is not configured")
	}

	rulesText := string(payload)
	if rulesText == "" {
		return fmt.Errorf("shared rules payload is empty")
	}

	s.logger.Info("Validating shared rules against eFLINT")
	if err := s.reasoner.ValidateSharedRules(ctx, rulesText); err != nil {
		return err
	}

	if err := s.rulesRepo.SaveSharedAgreementRules(rulesText); err != nil {
		s.logger.Error("failed to save shared rules", zap.Error(err))
		return fmt.Errorf("failed to save shared rules: %w", err)
	}

	return nil
}

// GetAllowedClausesForSteward loads the steward's Layer-2 phrases (via the
// configured provider) plus the Layer-2 shared rules, asks the reasoner to
// introspect the steward-supports-* / relation-allows-* facts, and returns
// the resulting StewardClauses snapshot. If `requester` is non-empty the
// returned clauses are narrowed to that requester's relation only. Returns
// (nil, nil) when the steward has no agreement registered.
//
// This is read-only: no Layer 3 is pushed and no Acts are fired, so the
// endpoint is safe to expose to a policy engineer.
func (s *ValidationService) GetAllowedClausesForSteward(ctx context.Context, steward, requester string) (*reasoner.StewardClauses, error) {
	clauses, _, err := s.GetAllowedClausesForStewardWithTiming(ctx, steward, requester)
	return clauses, err
}

// GetAllowedClausesForStewardWithTiming behaves like
// GetAllowedClausesForSteward, but also returns per-stage timing metrics.
func (s *ValidationService) GetAllowedClausesForStewardWithTiming(
	ctx context.Context,
	steward,
	requester string,
) (*reasoner.StewardClauses, AllowedClausesTiming, error) {
	timing := AllowedClausesTiming{}
	serviceStart := time.Now()

	if steward == "" {
		timing.ServiceTotalMs = time.Since(serviceStart).Milliseconds()
		return nil, timing, fmt.Errorf("steward is required")
	}

	stageStart := time.Now()
	provider := s.resolveProvider(steward)
	timing.ResolveProviderMs = time.Since(stageStart).Milliseconds()

	stageStart = time.Now()
	phrases, found, err := provider.GetLayer2Phrases(steward)
	timing.LoadStewardPhrasesMs = time.Since(stageStart).Milliseconds()
	if err != nil {
		s.logger.Warn("failed to load Layer-2 phrases for steward",
			zap.String("steward", steward),
			zap.String("provider", provider.Name()),
			zap.Error(err),
		)
		timing.ServiceTotalMs = time.Since(serviceStart).Milliseconds()
		return nil, timing, fmt.Errorf("loading Layer-2 phrases for %s: %w", steward, err)
	}
	if !found {
		timing.ServiceTotalMs = time.Since(serviceStart).Milliseconds()
		return nil, timing, nil
	}

	stageStart = time.Now()
	sharedRules, err := s.loadSharedRules()
	timing.LoadSharedRulesMs = time.Since(stageStart).Milliseconds()
	if err != nil {
		timing.ServiceTotalMs = time.Since(serviceStart).Milliseconds()
		return nil, timing, err
	}

	params := reasoner.IntrospectStewardClausesParams{
		Steward:        steward,
		SharedRules:    sharedRules,
		StewardPhrases: phrases,
	}
	if requester != "" {
		params.Requesters = []string{requester}
	}

	stageStart = time.Now()
	clauses, err := s.reasoner.IntrospectStewardClauses(ctx, params)
	timing.ReasonerIntrospectMs = time.Since(stageStart).Milliseconds()
	if err != nil {
		timing.ServiceTotalMs = time.Since(serviceStart).Milliseconds()
		return nil, timing, fmt.Errorf("introspecting steward clauses: %w", err)
	}

	// When a specific requester was requested, filter the relations down to
	// just that requester as a safety net (the reasoner already limits results
	// by grounding only the requested requester atom, but this guard handles
	// any stub or legacy reasoner implementation that ignores the Requesters
	// field).
	stageStart = time.Now()
	if requester != "" && clauses != nil {
		filtered := make([]reasoner.RequesterClauses, 0, 1)
		for _, rel := range clauses.Relations {
			if rel.Requester == requester {
				filtered = append(filtered, rel)
				break
			}
		}
		clauses.Relations = filtered
	}
	timing.FilterRelationsMs = time.Since(stageStart).Milliseconds()

	timing.ServiceTotalMs = time.Since(serviceStart).Milliseconds()
	return clauses, timing, nil
}

func containsString(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}
