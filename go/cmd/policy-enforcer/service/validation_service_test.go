package service

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/reasoner"
	"github.com/Jorrit05/DYNAMOS/pkg/api"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
	"go.uber.org/zap"
)

// --- Test doubles -----------------------------------------------------------

type fakeProviderConfigRepo struct {
	configs   map[string]*api.ProviderValidationConfig
	savedKeys []string
}

func (f *fakeProviderConfigRepo) GetProviderConfig(provider string) (*api.ProviderValidationConfig, bool, error) {
	if c, ok := f.configs[provider]; ok {
		return c, true, nil
	}
	return nil, false, nil
}

func (f *fakeProviderConfigRepo) SaveProviderConfig(provider string, config *api.ProviderValidationConfig) error {
	if f.configs == nil {
		f.configs = map[string]*api.ProviderValidationConfig{}
	}
	f.configs[provider] = config
	f.savedKeys = append(f.savedKeys, provider)
	return nil
}

type fakeRulesRepo struct {
	rules     string
	err       error
	saved     string
	saveCalls int
}

func (f *fakeRulesRepo) GetSharedAgreementRules() (string, bool, error) {
	if f.err != nil {
		return "", false, f.err
	}
	return f.rules, f.rules != "", nil
}

func (f *fakeRulesRepo) SaveSharedAgreementRules(text string) error {
	f.saved = text
	f.saveCalls++
	return nil
}

type fakeAgreementRepo struct {
	agreements  map[string]*api.Agreement
	deleteCalls []string
}

func (f *fakeAgreementRepo) GetAgreement(steward string) (*api.Agreement, bool, error) {
	if a, ok := f.agreements[steward]; ok {
		return a, true, nil
	}
	return nil, false, nil
}
func (f *fakeAgreementRepo) SaveAgreement(steward string, agreement *api.Agreement) error {
	if f.agreements == nil {
		f.agreements = map[string]*api.Agreement{}
	}
	f.agreements[steward] = agreement
	return nil
}
func (f *fakeAgreementRepo) DeleteAgreement(steward string) error {
	f.deleteCalls = append(f.deleteCalls, steward)
	delete(f.agreements, steward)
	return nil
}

type fakeEflintModelRepo struct {
	models      map[string]string
	deleteCalls []string
}

func (f *fakeEflintModelRepo) GetEflintModel(modelName string) (string, bool, error) {
	if v, ok := f.models[modelName]; ok {
		return v, true, nil
	}
	return "", false, nil
}
func (f *fakeEflintModelRepo) SaveEflintModel(modelName string, modelText string) error {
	if f.models == nil {
		f.models = map[string]string{}
	}
	f.models[modelName] = modelText
	return nil
}
func (f *fakeEflintModelRepo) DeleteEflintModel(modelName string) error {
	f.deleteCalls = append(f.deleteCalls, modelName)
	delete(f.models, modelName)
	return nil
}

type fakePhraseProvider struct {
	name              string
	phrases           map[string]string
	validateCalls     []validatePersistCall
	validateErr       error
	validatePersisted bool
}

type validatePersistCall struct {
	steward string
	payload string
}

func (p *fakePhraseProvider) Name() string { return p.name }
func (p *fakePhraseProvider) GetLayer2Phrases(steward string) (string, bool, error) {
	if v, ok := p.phrases[steward]; ok {
		return v, true, nil
	}
	return "", false, nil
}
func (p *fakePhraseProvider) ValidateAndPersist(ctx context.Context, steward string, payload []byte) error {
	p.validateCalls = append(p.validateCalls, validatePersistCall{steward: steward, payload: string(payload)})
	if p.validateErr != nil {
		return p.validateErr
	}
	p.validatePersisted = true
	return nil
}

// fakeReasoner records the params it receives and returns a pre-canned result.
type fakeReasoner struct {
	receivedParams reasoner.RequestApprovalParams
	called         bool
	result         *reasoner.RequestApprovalResult
	err            error

	introspectParams reasoner.IntrospectStewardClausesParams
	introspectCalled bool
	introspectResult *reasoner.StewardClauses
	introspectErr    error

	validateModelCalls []string
	lastValidatedModel string
	validateModelErr   error

	validateSharedRulesCalls int
	lastValidatedSharedRules string
	validateSharedRulesErr   error
}

func (r *fakeReasoner) EvaluateRequestApproval(ctx context.Context, params reasoner.RequestApprovalParams) (*reasoner.RequestApprovalResult, error) {
	r.called = true
	r.receivedParams = params
	return r.result, r.err
}
func (r *fakeReasoner) IntrospectStewardClauses(ctx context.Context, params reasoner.IntrospectStewardClausesParams) (*reasoner.StewardClauses, error) {
	r.introspectCalled = true
	r.introspectParams = params
	return r.introspectResult, r.introspectErr
}
func (r *fakeReasoner) IsRunning() bool { return true }
func (r *fakeReasoner) ValidateAndPersistModel(ctx context.Context, organization string, sharedRulesText string, modelText string) error {
	r.validateModelCalls = append(r.validateModelCalls, organization)
	r.lastValidatedModel = modelText
	return r.validateModelErr
}
func (r *fakeReasoner) ValidateSharedRules(ctx context.Context, rulesText string) error {
	r.validateSharedRulesCalls++
	r.lastValidatedSharedRules = rulesText
	return r.validateSharedRulesErr
}
func (r *fakeReasoner) Name() string { return "fake" }

type fakeAuthGen struct{}

func (fakeAuthGen) GenerateToken() *pb.Auth {
	return &pb.Auth{AccessToken: "test", RefreshToken: "test"}
}

// --- Tests ------------------------------------------------------------------

func newSvcWithFakes(t *testing.T) (*ValidationService, *fakeReasoner, *fakePhraseProvider, *fakePhraseProvider) {
	t.Helper()
	logger, _ := zap.NewDevelopment()

	configs := map[string]*api.ProviderValidationConfig{
		"VU":  {Name: "VU", ValidationStrategy: api.ValidationStrategyEflint},
		"UVA": {Name: "UVA", ValidationStrategy: api.ValidationStrategyEflint},
		"RUG": {Name: "RUG", ValidationStrategy: api.ValidationStrategyLegacy},
	}
	eflintProvider := &fakePhraseProvider{
		name: "eflint",
		phrases: map[string]string{
			"VU":  `+agreement("VU").`,
			"UVA": `+agreement("UVA").`,
		},
	}
	legacyProvider := &fakePhraseProvider{
		name:    "legacy",
		phrases: map[string]string{}, // RUG has no agreement
	}
	r := &fakeReasoner{}

	svc := NewValidationServiceWithConfig(ValidationServiceConfig{
		ProviderConfigRepo: &fakeProviderConfigRepo{configs: configs},
		RulesRepo:          &fakeRulesRepo{rules: `// shared rules`},
		LegacyProvider:     legacyProvider,
		EflintProvider:     eflintProvider,
		Reasoner:           r,
		AuthGenerator:      fakeAuthGen{},
		Logger:             logger,
	})
	return svc, r, eflintProvider, legacyProvider
}

func TestValidationService_AppliesEvaluationToWireFormat(t *testing.T) {
	svc, r, _, _ := newSvcWithFakes(t)

	r.result = &reasoner.RequestApprovalResult{
		PermittedRequest: true,
		PerSteward: map[string]reasoner.StewardDecision{
			"VU": {
				Permitted:        true,
				Archetypes:       []string{"computeToData"},
				ComputeProviders: []string{"SURF"},
			},
			"UVA": {
				Permitted:        true,
				Archetypes:       []string{"computeToData", "dataThroughTtp"},
				ComputeProviders: []string{"SURF"},
			},
		},
	}

	resp := svc.ValidateRequest(context.Background(), &pb.RequestApproval{
		Type:          "requestApproval",
		User:          &pb.User{Id: "1", UserName: "jorrit.stutterheim@cloudnation.nl"},
		DataProviders: []string{"VU", "UVA", "RUG"},
	})

	if !r.called {
		t.Fatalf("expected reasoner.EvaluateRequestApproval to be called")
	}

	// Stewards passed to the reasoner should match the request input.
	if !reflect.DeepEqual(r.receivedParams.Stewards, []string{"VU", "UVA", "RUG"}) {
		t.Errorf("unexpected stewards passed to reasoner: %v", r.receivedParams.Stewards)
	}
	if r.receivedParams.Requester != "jorrit.stutterheim@cloudnation.nl" {
		t.Errorf("unexpected requester: %q", r.receivedParams.Requester)
	}
	if _, ok := r.receivedParams.StewardPhrases["RUG"]; ok {
		t.Errorf("RUG has no agreement; phrases must not be passed for it")
	}

	// Wire-format expectations.
	if !resp.RequestApproved {
		t.Fatalf("expected RequestApproved=true")
	}
	if got := resp.ValidDataproviders["VU"]; got == nil || !reflect.DeepEqual(got.Archetypes, []string{"computeToData"}) {
		t.Errorf("unexpected VU archetypes: %+v", got)
	}
	if got := resp.ValidDataproviders["UVA"]; got == nil || !reflect.DeepEqual(got.Archetypes, []string{"computeToData", "dataThroughTtp"}) {
		t.Errorf("unexpected UVA archetypes: %+v", got)
	}
	if !containsString(resp.InvalidDataproviders, "RUG") {
		t.Errorf("expected RUG in InvalidDataproviders, got %v", resp.InvalidDataproviders)
	}
	if resp.Auth == nil {
		t.Errorf("expected auth token to be generated for approved request")
	}
	if resp.ValidArchetypes.UserName != "jorrit.stutterheim@cloudnation.nl" {
		t.Errorf("expected ValidArchetypes.UserName to be set")
	}
}

func TestValidationService_ReasonerErrorMarksAllInvalid(t *testing.T) {
	svc, r, _, _ := newSvcWithFakes(t)
	r.err = errors.New("eflint pool exhausted")

	resp := svc.ValidateRequest(context.Background(), &pb.RequestApproval{
		Type:          "requestApproval",
		User:          &pb.User{Id: "1", UserName: "user"},
		DataProviders: []string{"VU", "UVA"},
	})

	if resp.RequestApproved {
		t.Fatalf("expected RequestApproved=false on reasoner error")
	}
	got := append([]string(nil), resp.InvalidDataproviders...)
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"UVA", "VU"}) {
		t.Errorf("expected both stewards marked invalid, got %v", got)
	}
}

func TestValidationService_NoAgreementsSkipsReasoner(t *testing.T) {
	svc, r, eflintProvider, _ := newSvcWithFakes(t)
	eflintProvider.phrases = map[string]string{} // no agreements

	resp := svc.ValidateRequest(context.Background(), &pb.RequestApproval{
		Type:          "requestApproval",
		User:          &pb.User{Id: "1", UserName: "user"},
		DataProviders: []string{"VU"},
	})

	if r.called {
		t.Errorf("reasoner should not be called when no agreements are available")
	}
	if resp.RequestApproved {
		t.Errorf("expected RequestApproved=false")
	}
	if !containsString(resp.InvalidDataproviders, "VU") {
		t.Errorf("VU should be in InvalidDataproviders")
	}
}

// TestValidationService_GetAllowedClauses_ResolvesProviderAndPassesPhrases
// asserts that GetAllowedClausesForSteward picks the configured provider,
// loads the steward's Layer-2 phrases, hands them to the reasoner together
// with the shared rules, and returns whatever the reasoner produced.
func TestValidationService_GetAllowedClauses_ResolvesProviderAndPassesPhrases(t *testing.T) {
	svc, r, _, _ := newSvcWithFakes(t)
	r.introspectResult = &reasoner.StewardClauses{
		Steward:                   "VU",
		SupportedArchetypes:       []string{"computeToData", "dataThroughTtp"},
		SupportedComputeProviders: []string{"SURF"},
		Relations: []reasoner.RequesterClauses{
			{Requester: "alice@x", Archetypes: []string{"computeToData"}, ComputeProviders: []string{"SURF"}},
			{Requester: "bob@x", Archetypes: []string{"dataThroughTtp"}, ComputeProviders: []string{"SURF"}},
		},
	}

	out, err := svc.GetAllowedClausesForSteward(context.Background(), "VU", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !r.introspectCalled {
		t.Fatalf("expected IntrospectStewardClauses to be called")
	}
	if r.introspectParams.Steward != "VU" {
		t.Errorf("expected steward=VU, got %q", r.introspectParams.Steward)
	}
	if r.introspectParams.StewardPhrases != `+agreement("VU").` {
		t.Errorf("expected steward phrases to come from eflint provider, got %q", r.introspectParams.StewardPhrases)
	}
	if r.introspectParams.SharedRules != `// shared rules` {
		t.Errorf("expected shared rules to be loaded, got %q", r.introspectParams.SharedRules)
	}
	if len(out.Relations) != 2 {
		t.Errorf("expected both relations returned, got %d", len(out.Relations))
	}
}

func TestValidationService_GetAllowedClauses_FiltersByRequester(t *testing.T) {
	svc, r, _, _ := newSvcWithFakes(t)
	// The stub reasoner returns both relations regardless of Requesters; the
	// service must post-filter as a safety net.
	r.introspectResult = &reasoner.StewardClauses{
		Steward: "VU",
		Relations: []reasoner.RequesterClauses{
			{Requester: "alice@x"},
			{Requester: "bob@x"},
		},
	}

	out, err := svc.GetAllowedClausesForSteward(context.Background(), "VU", "bob@x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Relations) != 1 || out.Relations[0].Requester != "bob@x" {
		t.Errorf("expected only bob@x's relation, got %+v", out.Relations)
	}
	// Verify the service forwarded the requester to the reasoner so it can
	// ground the requester atom before querying.
	if len(r.introspectParams.Requesters) != 1 || r.introspectParams.Requesters[0] != "bob@x" {
		t.Errorf("expected Requesters=[bob@x] forwarded to reasoner, got %v", r.introspectParams.Requesters)
	}
}

func TestValidationService_GetAllowedClauses_NoAgreementReturnsNil(t *testing.T) {
	svc, r, eflintProvider, _ := newSvcWithFakes(t)
	eflintProvider.phrases = map[string]string{}

	out, err := svc.GetAllowedClausesForSteward(context.Background(), "VU", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != nil {
		t.Errorf("expected nil result for steward without agreement, got %+v", out)
	}
	if r.introspectCalled {
		t.Errorf("reasoner should not be called when steward has no agreement")
	}
}

// TestValidationService_ResolvesProviderByConfig confirms that legacy vs eflint
// providers are picked according to /policyEnforcer/configs/{provider}.
func TestValidationService_ResolvesProviderByConfig(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	cfgRepo := &fakeProviderConfigRepo{configs: map[string]*api.ProviderValidationConfig{
		"X": {Name: "X", ValidationStrategy: api.ValidationStrategyLegacy},
		"Y": {Name: "Y", ValidationStrategy: api.ValidationStrategyEflint},
	}}
	legacy := &fakePhraseProvider{name: "legacy", phrases: map[string]string{"X": `+agreement("X").`}}
	eflintP := &fakePhraseProvider{name: "eflint", phrases: map[string]string{"Y": `+agreement("Y").`}}
	r := &fakeReasoner{result: &reasoner.RequestApprovalResult{
		PerSteward: map[string]reasoner.StewardDecision{
			"X": {Permitted: false, Reason: "no relation"},
			"Y": {Permitted: false, Reason: "no relation"},
		},
	}}

	svc := NewValidationServiceWithConfig(ValidationServiceConfig{
		ProviderConfigRepo: cfgRepo,
		RulesRepo:          &fakeRulesRepo{rules: `// rules`},
		LegacyProvider:     legacy,
		EflintProvider:     eflintP,
		Reasoner:           r,
		AuthGenerator:      fakeAuthGen{},
		Logger:             logger,
	})

	_ = svc.ValidateRequest(context.Background(), &pb.RequestApproval{
		Type:          "requestApproval",
		User:          &pb.User{UserName: "user"},
		DataProviders: []string{"X", "Y"},
	})

	if got := r.receivedParams.StewardPhrases["X"]; got != `+agreement("X").` {
		t.Errorf("X should resolve via legacy provider, got %q", got)
	}
	if got := r.receivedParams.StewardPhrases["Y"]; got != `+agreement("Y").` {
		t.Errorf("Y should resolve via eflint provider, got %q", got)
	}
}

// --- ValidateAndPersistAgreement tests --------------------------------------

// newPersistSvcWithFakes wires the validation service for the persist/format
// path with stubs that observe the reconciliation side-effects.
func newPersistSvcWithFakes(t *testing.T, initialStrategy string) (
	*ValidationService,
	*fakeProviderConfigRepo,
	*fakeAgreementRepo,
	*fakeEflintModelRepo,
	*fakePhraseProvider,
	*fakePhraseProvider,
) {
	t.Helper()
	logger, _ := zap.NewDevelopment()

	cfgRepo := &fakeProviderConfigRepo{
		configs: map[string]*api.ProviderValidationConfig{},
	}
	if initialStrategy != "" {
		cfgRepo.configs["VU"] = &api.ProviderValidationConfig{
			Name:               "VU",
			ValidationStrategy: initialStrategy,
		}
	}
	agreementRepo := &fakeAgreementRepo{}
	modelRepo := &fakeEflintModelRepo{}
	legacy := &fakePhraseProvider{name: "legacy"}
	eflintP := &fakePhraseProvider{name: "eflint"}

	svc := NewValidationServiceWithConfig(ValidationServiceConfig{
		ProviderConfigRepo: cfgRepo,
		RulesRepo:          &fakeRulesRepo{},
		AgreementRepo:      agreementRepo,
		EflintModelRepo:    modelRepo,
		LegacyProvider:     legacy,
		EflintProvider:     eflintP,
		Reasoner:           &fakeReasoner{},
		AuthGenerator:      fakeAuthGen{},
		Logger:             logger,
	})

	return svc, cfgRepo, agreementRepo, modelRepo, legacy, eflintP
}

func TestValidateAndPersistAgreement_JsonRoutesToLegacy(t *testing.T) {
	svc, cfgRepo, agreementRepo, modelRepo, legacy, eflintP := newPersistSvcWithFakes(t, api.ValidationStrategyLegacy)

	payload := []byte(`{"name":"VU"}`)
	if err := svc.ValidateAndPersistAgreement(context.Background(), "VU", "json", payload); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(legacy.validateCalls) != 1 || legacy.validateCalls[0].steward != "VU" {
		t.Errorf("expected legacy provider to receive 1 call for VU, got %+v", legacy.validateCalls)
	}
	if len(eflintP.validateCalls) != 0 {
		t.Errorf("expected eflint provider not to be called, got %+v", eflintP.validateCalls)
	}
	// Strategy unchanged: no config save, no obsolete-key delete.
	if len(cfgRepo.savedKeys) != 0 {
		t.Errorf("expected no provider config save when strategy unchanged, got %+v", cfgRepo.savedKeys)
	}
	if len(agreementRepo.deleteCalls) != 0 || len(modelRepo.deleteCalls) != 0 {
		t.Errorf("expected no obsolete deletes when strategy unchanged, got agreement=%v model=%v",
			agreementRepo.deleteCalls, modelRepo.deleteCalls)
	}
}

func TestValidateAndPersistAgreement_EflintRoutesToEflint(t *testing.T) {
	svc, cfgRepo, agreementRepo, modelRepo, legacy, eflintP := newPersistSvcWithFakes(t, api.ValidationStrategyEflint)

	if err := svc.ValidateAndPersistAgreement(context.Background(), "VU", api.ValidationStrategyEflint, []byte(`+agreement("VU").`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(eflintP.validateCalls) != 1 {
		t.Errorf("expected eflint provider to be called once, got %+v", eflintP.validateCalls)
	}
	if len(legacy.validateCalls) != 0 {
		t.Errorf("expected legacy provider not to be called, got %+v", legacy.validateCalls)
	}
	if len(cfgRepo.savedKeys) != 0 {
		t.Errorf("expected no config save when strategy unchanged, got %+v", cfgRepo.savedKeys)
	}
	if len(agreementRepo.deleteCalls) != 0 || len(modelRepo.deleteCalls) != 0 {
		t.Errorf("unexpected deletes: agreement=%v model=%v", agreementRepo.deleteCalls, modelRepo.deleteCalls)
	}
}

func TestValidateAndPersistAgreement_SwitchLegacyToEflint(t *testing.T) {
	svc, cfgRepo, agreementRepo, modelRepo, _, _ := newPersistSvcWithFakes(t, api.ValidationStrategyLegacy)

	if err := svc.ValidateAndPersistAgreement(context.Background(), "VU", api.ValidationStrategyEflint, []byte(`+agreement("VU").`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, ok := cfgRepo.configs["VU"]
	if !ok || updated.ValidationStrategy != api.ValidationStrategyEflint {
		t.Errorf("expected provider config for VU to flip to eflint, got %+v", updated)
	}
	if !contains(agreementRepo.deleteCalls, "VU") {
		t.Errorf("expected obsolete legacy JSON to be deleted, got %v", agreementRepo.deleteCalls)
	}
	if len(modelRepo.deleteCalls) != 0 {
		t.Errorf("expected eFLINT model NOT to be deleted on legacy->eflint switch, got %v", modelRepo.deleteCalls)
	}
}

func TestValidateAndPersistAgreement_SwitchEflintToLegacy(t *testing.T) {
	svc, cfgRepo, agreementRepo, modelRepo, _, _ := newPersistSvcWithFakes(t, api.ValidationStrategyEflint)

	if err := svc.ValidateAndPersistAgreement(context.Background(), "VU", "json", []byte(`{"name":"VU"}`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, ok := cfgRepo.configs["VU"]
	if !ok || updated.ValidationStrategy != api.ValidationStrategyLegacy {
		t.Errorf("expected provider config for VU to flip to legacy, got %+v", updated)
	}
	if !contains(modelRepo.deleteCalls, "VU") {
		t.Errorf("expected obsolete eFLINT model to be deleted, got %v", modelRepo.deleteCalls)
	}
	if len(agreementRepo.deleteCalls) != 0 {
		t.Errorf("expected legacy JSON NOT to be deleted on eflint->legacy switch, got %v", agreementRepo.deleteCalls)
	}
}

func TestValidateAndPersistAgreement_NoExistingConfigCreatesOneWithoutDeletes(t *testing.T) {
	svc, cfgRepo, agreementRepo, modelRepo, _, _ := newPersistSvcWithFakes(t, "")

	if err := svc.ValidateAndPersistAgreement(context.Background(), "VU", api.ValidationStrategyEflint, []byte(`+agreement("VU").`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, ok := cfgRepo.configs["VU"]
	if !ok || updated.ValidationStrategy != api.ValidationStrategyEflint {
		t.Errorf("expected new provider config with eflint strategy, got %+v", updated)
	}
	if len(agreementRepo.deleteCalls) != 0 || len(modelRepo.deleteCalls) != 0 {
		t.Errorf("expected no deletes when there was no prior config, got agreement=%v model=%v",
			agreementRepo.deleteCalls, modelRepo.deleteCalls)
	}
}

func TestValidateAndPersistAgreement_UnknownFormatRejected(t *testing.T) {
	svc, _, _, _, legacy, eflintP := newPersistSvcWithFakes(t, "")

	err := svc.ValidateAndPersistAgreement(context.Background(), "VU", "yaml", []byte("name: VU"))
	if err == nil {
		t.Fatalf("expected error for unknown format")
	}
	if len(legacy.validateCalls)+len(eflintP.validateCalls) != 0 {
		t.Errorf("no provider should be invoked for unknown format")
	}
}

func TestValidateAndPersistAgreement_ProviderErrorIsReturnedAndNoReconciliation(t *testing.T) {
	svc, cfgRepo, agreementRepo, modelRepo, legacy, _ := newPersistSvcWithFakes(t, api.ValidationStrategyEflint)
	legacy.validateErr = errInjected{msg: "bad json"}

	err := svc.ValidateAndPersistAgreement(context.Background(), "VU", "json", []byte(`{}`))
	if err == nil {
		t.Fatalf("expected error from provider")
	}
	if len(cfgRepo.savedKeys) != 0 {
		t.Errorf("config should not be updated when provider validation failed")
	}
	if len(agreementRepo.deleteCalls) != 0 || len(modelRepo.deleteCalls) != 0 {
		t.Errorf("no deletes expected when validation failed")
	}
}

// --- ValidateAndPersistSharedRules tests ------------------------------------

func TestValidateAndPersistSharedRules_PersistsOnSuccess(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	rulesRepo := &fakeRulesRepo{}
	r := &fakeReasoner{}

	svc := NewValidationServiceWithConfig(ValidationServiceConfig{
		ProviderConfigRepo: &fakeProviderConfigRepo{},
		RulesRepo:          rulesRepo,
		LegacyProvider:     &fakePhraseProvider{name: "legacy"},
		EflintProvider:     &fakePhraseProvider{name: "eflint"},
		Reasoner:           r,
		AuthGenerator:      fakeAuthGen{},
		Logger:             logger,
	})

	if err := svc.ValidateAndPersistSharedRules(context.Background(), []byte(`// hello rules`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.validateSharedRulesCalls != 1 {
		t.Errorf("expected reasoner.ValidateSharedRules to be called once, got %d", r.validateSharedRulesCalls)
	}
	if r.lastValidatedSharedRules != "// hello rules" {
		t.Errorf("expected rules text to be forwarded to reasoner, got %q", r.lastValidatedSharedRules)
	}
	if rulesRepo.saveCalls != 1 || rulesRepo.saved != "// hello rules" {
		t.Errorf("expected shared rules to be saved exactly once, got calls=%d saved=%q", rulesRepo.saveCalls, rulesRepo.saved)
	}
}

func TestValidateAndPersistSharedRules_ReasonerErrorPreventsPersist(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	rulesRepo := &fakeRulesRepo{}
	r := &fakeReasoner{validateSharedRulesErr: errInjected{msg: "bad rules"}}

	svc := NewValidationServiceWithConfig(ValidationServiceConfig{
		ProviderConfigRepo: &fakeProviderConfigRepo{},
		RulesRepo:          rulesRepo,
		LegacyProvider:     &fakePhraseProvider{name: "legacy"},
		EflintProvider:     &fakePhraseProvider{name: "eflint"},
		Reasoner:           r,
		AuthGenerator:      fakeAuthGen{},
		Logger:             logger,
	})

	if err := svc.ValidateAndPersistSharedRules(context.Background(), []byte(`// bad`)); err == nil {
		t.Fatalf("expected error from reasoner to propagate")
	}
	if rulesRepo.saveCalls != 0 {
		t.Errorf("shared rules must not be persisted when reasoner rejects them")
	}
}

func TestValidateAndPersistSharedRules_EmptyPayloadRejected(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	rulesRepo := &fakeRulesRepo{}
	r := &fakeReasoner{}

	svc := NewValidationServiceWithConfig(ValidationServiceConfig{
		ProviderConfigRepo: &fakeProviderConfigRepo{},
		RulesRepo:          rulesRepo,
		LegacyProvider:     &fakePhraseProvider{name: "legacy"},
		EflintProvider:     &fakePhraseProvider{name: "eflint"},
		Reasoner:           r,
		AuthGenerator:      fakeAuthGen{},
		Logger:             logger,
	})

	if err := svc.ValidateAndPersistSharedRules(context.Background(), []byte{}); err == nil {
		t.Fatalf("expected error for empty payload")
	}
	if r.validateSharedRulesCalls != 0 {
		t.Errorf("reasoner should not be called for empty payload")
	}
}

// --- helpers ----------------------------------------------------------------

type errInjected struct{ msg string }

func (e errInjected) Error() string { return e.msg }

func contains(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}
