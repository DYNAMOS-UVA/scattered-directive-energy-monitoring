package reasoner

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/eflint"
)

func TestQuoteEflintLiteral(t *testing.T) {
	cases := map[string]string{
		`Niels`:  `"Niels"`,
		`Jorrit`: `"Jorrit"`,
		`a"b`:    `"a\"b"`,
		`a\b`:    `"a\\b"`,
	}
	for in, want := range cases {
		if got := quoteEflintLiteral(in); got != want {
			t.Errorf("quoteEflintLiteral(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildLayer3Phrases(t *testing.T) {
	got := buildLayer3Phrases(`"Niels"`, []string{"VU", "UVA", "VU"})
	want := strings.Join([]string{
		`+requester("Niels").`,
		`+requested-steward("VU").`,
		`+requested-steward("UVA").`,
		``,
	}, "\n")
	if got != want {
		t.Errorf("Layer-3 phrases mismatch\nwant:\n%s\ngot:\n%s", want, got)
	}
}

// stubPhrasesSender records the phrase it was called with and returns a
// canned response/error. It implements the phrasesSender interface used by
// queryInstances.
type stubPhrasesSender struct {
	gotPhrase string
	resp      *eflint.PhrasesResponse
	err       error
}

func (s *stubPhrasesSender) SendPhrases(text string) (*eflint.PhrasesResponse, error) {
	s.gotPhrase = text
	return s.resp, s.err
}

func TestQueryInstances(t *testing.T) {
	t.Run("extracts values from inst-query-results", func(t *testing.T) {
		stub := &stubPhrasesSender{
			resp: &eflint.PhrasesResponse{
				InstQueryResults: []eflint.InstQueryResult{
					{FactType: "archetype", TaggedType: "string", Value: "DataThroughTtp"},
					{FactType: "archetype", TaggedType: "string", Value: "ComputeToData"},
				},
			},
		}
		phrase := `?-archetype When valid-archetype("Niels", "UVA", archetype).`

		got, err := queryInstances(stub, phrase)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if stub.gotPhrase != phrase {
			t.Errorf("phrase forwarded incorrectly: got %q, want %q", stub.gotPhrase, phrase)
		}
		want := []string{"DataThroughTtp", "ComputeToData"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("values mismatch: got %v, want %v", got, want)
		}
	})

	t.Run("returns empty slice when no matches", func(t *testing.T) {
		stub := &stubPhrasesSender{
			resp: &eflint.PhrasesResponse{InstQueryResults: nil},
		}
		got, err := queryInstances(stub, "?-archetype When valid-archetype(...)")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("expected empty slice, got %v", got)
		}
	})

	t.Run("propagates SendPhrases error", func(t *testing.T) {
		sentinel := errors.New("boom")
		stub := &stubPhrasesSender{err: sentinel}
		_, err := queryInstances(stub, "?-archetype When ...")
		if !errors.Is(err, sentinel) {
			t.Errorf("expected wrapped sentinel error, got %v", err)
		}
	})

	t.Run("errors on nil response", func(t *testing.T) {
		stub := &stubPhrasesSender{resp: nil, err: nil}
		_, err := queryInstances(stub, "?-archetype When ...")
		if err == nil {
			t.Errorf("expected error for nil response, got nil")
		}
	})
}
