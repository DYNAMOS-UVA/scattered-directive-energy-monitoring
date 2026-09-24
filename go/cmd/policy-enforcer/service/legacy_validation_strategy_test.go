package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Jorrit05/DYNAMOS/pkg/api"
	"go.uber.org/zap"
)

// stubAgreementRepo lets the strict-validation tests run against
// LegacyAgreementPhraseProvider without a real etcd backend. It records the
// last save so the success path can assert that a parsed Agreement was
// persisted.
type stubAgreementRepo struct {
	saveErr   error
	lastSaved *api.Agreement
}

func (s *stubAgreementRepo) GetAgreement(steward string) (*api.Agreement, bool, error) {
	return nil, false, nil
}
func (s *stubAgreementRepo) SaveAgreement(steward string, agreement *api.Agreement) error {
	s.lastSaved = agreement
	return s.saveErr
}
func (s *stubAgreementRepo) DeleteAgreement(steward string) error { return nil }

func newLegacyProvider(t *testing.T) (*LegacyAgreementPhraseProvider, *stubAgreementRepo) {
	t.Helper()
	logger, _ := zap.NewDevelopment()
	repo := &stubAgreementRepo{}
	return NewLegacyAgreementPhraseProvider(repo, logger), repo
}

const validAgreementJSON = `{
  "name": "VU",
  "computeProviders": ["SURF"],
  "archetypes": ["computeToData"],
  "relations": {
    "alice@x": {
      "ID": "alice@x",
      "requestTypes": ["sqlDataRequest"],
      "dataSets": [],
      "allowedArchetypes": ["computeToData"],
      "allowedComputeProviders": ["SURF"]
    }
  }
}`

func TestLegacy_ValidateAndPersist_AcceptsCanonicalAgreement(t *testing.T) {
	provider, repo := newLegacyProvider(t)

	if err := provider.ValidateAndPersist(context.Background(), "VU", []byte(validAgreementJSON)); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if repo.lastSaved == nil || repo.lastSaved.Name != "VU" {
		t.Fatalf("expected agreement to be persisted, got %+v", repo.lastSaved)
	}
}

func TestLegacy_ValidateAndPersist_RejectsMalformedJSON(t *testing.T) {
	provider, repo := newLegacyProvider(t)

	err := provider.ValidateAndPersist(context.Background(), "VU", []byte(`{`))
	if err == nil {
		t.Fatalf("expected JSON parse error")
	}
	if repo.lastSaved != nil {
		t.Errorf("agreement must not be saved when JSON is malformed")
	}
}

func TestLegacy_ValidateAndPersist_RejectsUnknownFields(t *testing.T) {
	provider, _ := newLegacyProvider(t)

	payload := []byte(`{"name":"VU","computeProviders":["SURF"],"archetypes":["computeToData"],"relations":{"alice@x":{"ID":"alice@x","requestTypes":["sqlDataRequest"],"allowedArchetypes":["computeToData"],"allowedComputeProviders":["SURF"]}},"surpriseField":true}`)
	err := provider.ValidateAndPersist(context.Background(), "VU", payload)
	if err == nil {
		t.Fatalf("expected error on unknown field")
	}
	if !strings.Contains(err.Error(), "surpriseField") {
		t.Errorf("expected error to mention the offending field, got %v", err)
	}
}

func TestLegacy_ValidateAndPersist_RejectsNameMismatch(t *testing.T) {
	provider, _ := newLegacyProvider(t)

	err := provider.ValidateAndPersist(context.Background(), "UVA", []byte(validAgreementJSON))
	if err == nil || !strings.Contains(err.Error(), "agreement name mismatch") {
		t.Fatalf("expected name-mismatch error, got %v", err)
	}
}

func TestLegacy_ValidateAndPersist_RejectsEmptyRequiredCollections(t *testing.T) {
	provider, _ := newLegacyProvider(t)

	cases := map[string]string{
		"no relations": `{"name":"VU","computeProviders":["SURF"],"archetypes":["computeToData"],"relations":{}}`,
		"no compute providers": `{"name":"VU","computeProviders":[],"archetypes":["computeToData"],"relations":{"a":{"ID":"a","requestTypes":["x"],"allowedArchetypes":["y"]}}}`,
		"no archetypes": `{"name":"VU","computeProviders":["SURF"],"archetypes":[],"relations":{"a":{"ID":"a","requestTypes":["x"],"allowedArchetypes":["y"]}}}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if err := provider.ValidateAndPersist(context.Background(), "VU", []byte(payload)); err == nil {
				t.Errorf("expected error for %s", name)
			}
		})
	}
}

func TestLegacy_ValidateAndPersist_RejectsRelationWithoutRequestOrDataSet(t *testing.T) {
	provider, _ := newLegacyProvider(t)

	payload := []byte(`{"name":"VU","computeProviders":["SURF"],"archetypes":["computeToData"],"relations":{"alice@x":{"ID":"alice@x","allowedArchetypes":["computeToData"]}}}`)
	if err := provider.ValidateAndPersist(context.Background(), "VU", payload); err == nil {
		t.Errorf("relation without requestTypes or dataSets must be rejected")
	}
}

func TestLegacy_ValidateAndPersist_RejectsRelationWithoutAllowed(t *testing.T) {
	provider, _ := newLegacyProvider(t)

	payload := []byte(`{"name":"VU","computeProviders":["SURF"],"archetypes":["computeToData"],"relations":{"alice@x":{"ID":"alice@x","requestTypes":["sqlDataRequest"]}}}`)
	if err := provider.ValidateAndPersist(context.Background(), "VU", payload); err == nil {
		t.Errorf("relation without allowedArchetypes/allowedComputeProviders must be rejected")
	}
}

func TestLegacy_ValidateAndPersist_RepoErrorIsWrapped(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	repo := &stubAgreementRepo{saveErr: errors.New("boom")}
	provider := NewLegacyAgreementPhraseProvider(repo, logger)

	err := provider.ValidateAndPersist(context.Background(), "VU", []byte(validAgreementJSON))
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected wrapped save error, got %v", err)
	}
}
