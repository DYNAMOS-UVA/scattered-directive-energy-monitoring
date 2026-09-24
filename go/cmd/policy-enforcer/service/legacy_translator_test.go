package service

import (
	"strings"
	"testing"

	"github.com/Jorrit05/DYNAMOS/pkg/api"
)

// TestTranslateLegacyAgreement_VUShape mirrors the structure of
// docs/eflint-examples/agreement_VU.eflint: one steward with one relation,
// covering all four allowed-list dimensions.
func TestTranslateLegacyAgreement_VUShape(t *testing.T) {
	agreement := &api.Agreement{
		Name: "VU",
		Relations: map[string]api.Relation{
			"jorrit.stutterheim@cloudnation.nl": {
				ID:                      "GUID",
				RequestTypes:            []string{"sqlDataRequest", "genericRequest"},
				DataSets:                []string{"wageGap"},
				AllowedArchetypes:       []string{"computeToData", "dataThroughTtp"},
				AllowedComputeProviders: []string{"SURF"},
			},
		},
		ComputeProviders: []string{"SURF", "otherCompany"},
		Archetypes:       []string{"computeToData", "dataThroughTtp", "reproducableScience"},
	}

	out := TranslateLegacyAgreement(agreement)

	mustContain := []string{
		`+data-steward("VU").`,
		`+archetype("computeToData").`,
		`+archetype("dataThroughTtp").`,
		`+archetype("reproducableScience").`,
		`+compute-provider("SURF").`,
		`+compute-provider("otherCompany").`,
		`+dataset("wageGap").`,
		`+request-type("sqlDataRequest").`,
		`+request-type("genericRequest").`,
		`+agreement("VU").`,
		`+steward-supports-archetype("VU", "computeToData").`,
		`+steward-supports-archetype("VU", "dataThroughTtp").`,
		`+steward-supports-archetype("VU", "reproducableScience").`,
		`+steward-supports-compute-provider("VU", "SURF").`,
		`+steward-supports-compute-provider("VU", "otherCompany").`,
		`+has-relation("jorrit.stutterheim@cloudnation.nl", "VU").`,
		`+relation-allows-request-type("jorrit.stutterheim@cloudnation.nl", "VU", "sqlDataRequest").`,
		`+relation-allows-request-type("jorrit.stutterheim@cloudnation.nl", "VU", "genericRequest").`,
		`+relation-allows-dataset("jorrit.stutterheim@cloudnation.nl", "VU", "wageGap").`,
		`+relation-allows-archetype("jorrit.stutterheim@cloudnation.nl", "VU", "computeToData").`,
		`+relation-allows-archetype("jorrit.stutterheim@cloudnation.nl", "VU", "dataThroughTtp").`,
		`+relation-allows-compute-provider("jorrit.stutterheim@cloudnation.nl", "VU", "SURF").`,
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Errorf("translator output missing %q\n--- output ---\n%s", want, out)
		}
	}

	// +requester(...) must NOT appear in Layer-2 output. It is a Layer-3 fact
	// built per evaluation by buildLayer3Phrases; including it here would
	// pollute the execution graph with relation-owners from foreign requests.
	if strings.Contains(out, `+requester(`) {
		t.Errorf("Layer-2 output must not contain +requester(...) — that is a Layer-3 fact;\n--- output ---\n%s", out)
	}
}

// TestTranslateLegacyAgreement_EmptyRelations covers the RUG case from
// agreements.json: an agreement is registered but no users have relations.
// The output must still declare the data-steward and the agreement(...) fact
// so that permitted-at-steward returns false (rather than the agreement
// missing entirely).
func TestTranslateLegacyAgreement_EmptyRelations(t *testing.T) {
	agreement := &api.Agreement{
		Name:      "RUG",
		Relations: map[string]api.Relation{},
	}

	out := TranslateLegacyAgreement(agreement)
	if !strings.Contains(out, `+data-steward("RUG").`) {
		t.Errorf("expected data-steward declaration, got:\n%s", out)
	}
	if !strings.Contains(out, `+agreement("RUG").`) {
		t.Errorf("expected agreement fact, got:\n%s", out)
	}
	if strings.Contains(out, `has-relation`) {
		t.Errorf("did not expect has-relation phrases, got:\n%s", out)
	}
}

// TestTranslateLegacyAgreement_DeterministicOrder ensures that calling the
// translator twice on the same input produces identical output, which keeps
// any future golden test stable.
func TestTranslateLegacyAgreement_DeterministicOrder(t *testing.T) {
	agreement := &api.Agreement{
		Name: "UVA",
		Relations: map[string]api.Relation{
			"alice@example.com": {
				AllowedArchetypes:       []string{"dataThroughTtp"},
				AllowedComputeProviders: []string{"SURF"},
			},
			"bob@example.com": {
				AllowedArchetypes:       []string{"computeToData"},
				AllowedComputeProviders: []string{"SURF"},
			},
		},
		ComputeProviders: []string{"SURF"},
		Archetypes:       []string{"computeToData", "dataThroughTtp"},
	}

	first := TranslateLegacyAgreement(agreement)
	second := TranslateLegacyAgreement(agreement)
	if first != second {
		t.Fatalf("translator output is non-deterministic")
	}

	if strings.Index(first, "alice@example.com") > strings.Index(first, "bob@example.com") {
		t.Errorf("expected relations to be emitted in sorted user order")
	}

	if strings.Contains(first, `+requester(`) {
		t.Errorf("Layer-2 output must not contain +requester(...) — that is a Layer-3 fact;\n--- output ---\n%s", first)
	}
}

func TestQuoteEflintIdentifier_EscapesQuotes(t *testing.T) {
	if got := quoteEflintIdentifier(`a"b`); got != `"a\"b"` {
		t.Errorf(`expected "a\"b", got %q`, got)
	}
	if got := quoteEflintIdentifier(`a\b`); got != `"a\\b"` {
		t.Errorf(`expected "a\\b", got %q`, got)
	}
}
