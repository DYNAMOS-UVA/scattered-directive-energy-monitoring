package service

import (
	"sort"
	"strings"

	"github.com/Jorrit05/DYNAMOS/pkg/api"
)

// TranslateLegacyAgreement converts a legacy JSON agreement (api.Agreement)
// into a Layer-2 eFLINT phrase block consistent with the shape of
// docs/eflint-examples/agreement_VU.eflint.
//
// The produced phrases declare:
//   - the type instances referenced by the agreement (data-steward, archetype,
//     compute-provider, dataset, request-type),
//   - the agreement(...) fact for the steward,
//   - the steward-supports-* facts for top-level archetypes / compute providers,
//   - the has-relation + relation-allows-* facts for each user in
//     agreement.Relations.
//
// IMPORTANT — +requester(...) is NOT emitted here. It is a Layer-3 (per
// request) fact built by the reasoner's buildLayer3Phrases. Emitting it in
// Layer-2 would ground requester instances from every agreement relation into
// the eFLINT state, causing the engine to derive permitted-request / permitted-
// at-steward facts for foreign requesters within the same evaluation. eFLINT
// creates the requester instance implicitly when the composite has-relation /
// relation-allows-* facts are asserted, so no explicit +requester is needed.
//
// All other identifiers are emitted as quoted eFLINT string literals so that
// values containing characters outside [A-Za-z0-9_-] (e.g. email addresses)
// are accepted by the reasoner.
func TranslateLegacyAgreement(agreement *api.Agreement) string {
	var b strings.Builder
	steward := agreement.Name
	stewardLit := quoteEflintIdentifier(steward)

	// Collect distinct type instances referenced anywhere in the agreement
	// so we can declare them up-front (an instance must exist before any
	// composite fact that references it).
	archetypes := newOrderedSet()
	computeProviders := newOrderedSet()
	datasets := newOrderedSet()
	requestTypes := newOrderedSet()

	for _, a := range agreement.Archetypes {
		archetypes.add(a)
	}
	for _, c := range agreement.ComputeProviders {
		computeProviders.add(c)
	}
	relationKeys := make([]string, 0, len(agreement.Relations))
	for user := range agreement.Relations {
		relationKeys = append(relationKeys, user)
	}
	sort.Strings(relationKeys)

	for _, user := range relationKeys {
		rel := agreement.Relations[user]
		for _, rt := range rel.RequestTypes {
			requestTypes.add(rt)
		}
		for _, ds := range rel.DataSets {
			datasets.add(ds)
		}
		for _, a := range rel.AllowedArchetypes {
			archetypes.add(a)
		}
		for _, c := range rel.AllowedComputeProviders {
			computeProviders.add(c)
		}
	}

	b.WriteString("// Type instances referenced by this agreement.\n")
	b.WriteString("+data-steward(")
	b.WriteString(stewardLit)
	b.WriteString(").\n")

	for _, v := range archetypes.values() {
		b.WriteString("+archetype(")
		b.WriteString(quoteEflintIdentifier(v))
		b.WriteString(").\n")
	}
	for _, v := range computeProviders.values() {
		b.WriteString("+compute-provider(")
		b.WriteString(quoteEflintIdentifier(v))
		b.WriteString(").\n")
	}
	for _, v := range datasets.values() {
		b.WriteString("+dataset(")
		b.WriteString(quoteEflintIdentifier(v))
		b.WriteString(").\n")
	}
	for _, v := range requestTypes.values() {
		b.WriteString("+request-type(")
		b.WriteString(quoteEflintIdentifier(v))
		b.WriteString(").\n")
	}

	b.WriteString("\n+agreement(")
	b.WriteString(stewardLit)
	b.WriteString(").\n\n")

	if len(agreement.Archetypes) > 0 {
		b.WriteString("// Archetypes supported at the agreement level.\n")
		for _, a := range agreement.Archetypes {
			b.WriteString("+steward-supports-archetype(")
			b.WriteString(stewardLit)
			b.WriteString(", ")
			b.WriteString(quoteEflintIdentifier(a))
			b.WriteString(").\n")
		}
	}

	if len(agreement.ComputeProviders) > 0 {
		b.WriteString("\n// Compute providers accepted by this steward.\n")
		for _, c := range agreement.ComputeProviders {
			b.WriteString("+steward-supports-compute-provider(")
			b.WriteString(stewardLit)
			b.WriteString(", ")
			b.WriteString(quoteEflintIdentifier(c))
			b.WriteString(").\n")
		}
	}

	for _, user := range relationKeys {
		rel := agreement.Relations[user]
		userLit := quoteEflintIdentifier(user)

		b.WriteString("\n// ── Relation: ")
		b.WriteString(user)
		b.WriteString(" ───────────────────────────────────────────────\n")

		b.WriteString("+has-relation(")
		b.WriteString(userLit)
		b.WriteString(", ")
		b.WriteString(stewardLit)
		b.WriteString(").\n")

		for _, rt := range rel.RequestTypes {
			b.WriteString("+relation-allows-request-type(")
			b.WriteString(userLit)
			b.WriteString(", ")
			b.WriteString(stewardLit)
			b.WriteString(", ")
			b.WriteString(quoteEflintIdentifier(rt))
			b.WriteString(").\n")
		}
		for _, ds := range rel.DataSets {
			b.WriteString("+relation-allows-dataset(")
			b.WriteString(userLit)
			b.WriteString(", ")
			b.WriteString(stewardLit)
			b.WriteString(", ")
			b.WriteString(quoteEflintIdentifier(ds))
			b.WriteString(").\n")
		}
		for _, a := range rel.AllowedArchetypes {
			b.WriteString("+relation-allows-archetype(")
			b.WriteString(userLit)
			b.WriteString(", ")
			b.WriteString(stewardLit)
			b.WriteString(", ")
			b.WriteString(quoteEflintIdentifier(a))
			b.WriteString(").\n")
		}
		for _, c := range rel.AllowedComputeProviders {
			b.WriteString("+relation-allows-compute-provider(")
			b.WriteString(userLit)
			b.WriteString(", ")
			b.WriteString(stewardLit)
			b.WriteString(", ")
			b.WriteString(quoteEflintIdentifier(c))
			b.WriteString(").\n")
		}
	}

	return b.String()
}

// quoteEflintIdentifier emits a value as an eFLINT string literal.
// Always quoting (rather than only when the value contains special characters)
// keeps the identifier convention stable across the entire layered pipeline:
// Layer-2 agreement phrases produced by the translator, the per-steward
// agreement files in configuration/eflint-models/, and the Layer-3 request
// facts built by the reasoner all use the same form.
func quoteEflintIdentifier(value string) string {
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

// orderedSet preserves insertion order while deduplicating, which keeps the
// translator output deterministic and easy to diff in tests.
type orderedSet struct {
	seen  map[string]struct{}
	order []string
}

func newOrderedSet() *orderedSet {
	return &orderedSet{seen: map[string]struct{}{}}
}

func (s *orderedSet) add(v string) {
	if _, ok := s.seen[v]; ok {
		return
	}
	s.seen[v] = struct{}{}
	s.order = append(s.order, v)
}

func (s *orderedSet) values() []string {
	return s.order
}
