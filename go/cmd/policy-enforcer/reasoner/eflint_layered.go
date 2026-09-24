package reasoner

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/eflint"
)

// -----------------------------------------------------------------------------
// Instance query helper
// -----------------------------------------------------------------------------

// phrasesSender is the narrow interface used by queryInstances and queryHolds.
// *eflint.Manager satisfies it; tests can pass a stub.
type phrasesSender interface {
	SendPhrases(text string) (*eflint.PhrasesResponse, error)
}

// queryInstances sends a single generative eFLINT phrase (`?-factType When …`)
// to the reasoner and returns the matched fact values from
// `inst-query-results`. This lets the reasoner do the filtering / intersection
// directly, instead of dumping every grounded fact and filtering in Go.
func queryInstances(s phrasesSender, phrase string) ([]string, error) {
	resp, err := s.SendPhrases(phrase)
	if err != nil {
		return nil, fmt.Errorf("instance query %q failed: %w", phrase, err)
	}
	if resp == nil {
		return nil, fmt.Errorf("nil response for instance query %q", phrase)
	}
	out := make([]string, 0, len(resp.InstQueryResults))
	for _, res := range resp.InstQueryResults {
		out = append(out, res.Value)
	}
	return out, nil
}

// loggedQueryInstances wraps queryInstances with DEBUG-level logging of
// the phrase sent, the raw inst-query-results from the server, and the
// extracted values. Use this instead of queryInstances wherever the
// EflintReasoner has a logger available.
func (r *EflintReasoner) loggedQueryInstances(s phrasesSender, phrase string) ([]string, error) {
	resp, err := s.SendPhrases(phrase)
	if err != nil {
		r.logger.Debug("eflint query failed",
			zap.String("phrase", phrase),
			zap.Error(err),
		)
		return nil, fmt.Errorf("instance query %q failed: %w", phrase, err)
	}
	if resp == nil {
		r.logger.Debug("eflint query nil response", zap.String("phrase", phrase))
		return nil, fmt.Errorf("nil response for instance query %q", phrase)
	}
	r.logger.Debug("eflint query raw inst-query-results",
		zap.String("phrase", phrase),
		zap.Any("inst_query_results", resp.InstQueryResults),
	)
	out := make([]string, 0, len(resp.InstQueryResults))
	for _, res := range resp.InstQueryResults {
		out = append(out, res.Value)
	}
	r.logger.Debug("eflint query extracted values",
		zap.String("phrase", phrase),
		zap.Strings("values", out),
	)
	return out, nil
}

// EvaluateRequestApproval implements the layered request-approval evaluation
// (Layer 1 + Layer 2 + Layer 3) on top of the eFLINT instance pool.
//
// Lifecycle for a single evaluation:
//
//  1. Acquire one pool entry. Each entry is started with the Layer-1 interface
//     policy as its boot model, so the base fact types and query-fact
//     declarations are already in place.
//  2. Push the Layer-2 shared rules (intermediate facts + Extend rules).
//  3. Push the per-steward Layer-2 phrases for each steward we have an
//     agreement for. Stewards missing from StewardPhrases stay absent: the
//     reasoner naturally derives permitted-at-steward = false for them.
//  4. Push the Layer-3 request facts: +requester(R). +requested-steward(S)...
//  5. Query permitted-request(R) for the top-level decision.
//  6. For each requested steward, query permitted-at-steward(R, S). If it
//     holds, intersect the relation-allows-* and steward-supports-* base
//     facts to materialise the valid-archetype / valid-compute-provider sets,
//     and fire submit-data-request(R, S) so the data-request fact and
//     obligated-log duty become part of the audit trail.
//  7. Release the pool entry; the pool restarts the eFLINT process with the
//     Layer-1 baseline so no state leaks between requests.
func (r *EflintReasoner) EvaluateRequestApproval(ctx context.Context, params RequestApprovalParams) (*RequestApprovalResult, error) {
	if params.Requester == "" {
		return nil, fmt.Errorf("requester is required")
	}

	entry, err := r.pool.Acquire()
	if err != nil {
		return nil, fmt.Errorf("failed to acquire eFLINT instance: %w", err)
	}
	defer func() {
		go r.pool.Release(entry)
	}()
	mgr := entry.Manager

	if strings.TrimSpace(params.SharedRules) != "" {
		if _, err := mgr.SendPhrases(params.SharedRules); err != nil {
			return nil, fmt.Errorf("failed to load Layer-2 shared rules: %w", err)
		}
	}

	for _, steward := range params.Stewards {
		text, ok := params.StewardPhrases[steward]
		if !ok || strings.TrimSpace(text) == "" {
			r.logger.Debug("no Layer-2 phrases for steward; skipping",
				zap.String("steward", steward),
			)
			continue
		}
		if _, err := mgr.SendPhrases(text); err != nil {
			return nil, fmt.Errorf("failed to load Layer-2 agreement for %s: %w", steward, err)
		}
	}

	requesterLit := quoteEflintLiteral(params.Requester)
	layer3 := buildLayer3Phrases(requesterLit, params.Stewards)
	if _, err := mgr.SendPhrases(layer3); err != nil {
		return nil, fmt.Errorf("failed to load Layer-3 request facts: %w", err)
	}

	permittedRequest, err := queryHolds(mgr, fmt.Sprintf("permitted-request(%s)", requesterLit))
	if err != nil {
		return nil, fmt.Errorf("failed to query permitted-request: %w", err)
	}

	result := &RequestApprovalResult{
		PermittedRequest: permittedRequest,
		PerSteward:       make(map[string]StewardDecision, len(params.Stewards)),
	}

	for _, steward := range params.Stewards {
		decision := StewardDecision{}
		if _, hasPhrases := params.StewardPhrases[steward]; !hasPhrases {
			decision.Reason = "no agreement registered"
			result.PerSteward[steward] = decision
			continue
		}

		stewardLit := quoteEflintLiteral(steward)
		permittedAtSteward, err := queryHolds(mgr,
			fmt.Sprintf("permitted-at-steward(%s, %s)", requesterLit, stewardLit))
		if err != nil {
			return nil, fmt.Errorf("failed to query permitted-at-steward(%s): %w", steward, err)
		}
		decision.Permitted = permittedAtSteward
		if !permittedAtSteward {
			decision.Reason = "permitted-at-steward did not hold"
			result.PerSteward[steward] = decision
			continue
		}

		archetypes, computeProviders, err := r.collectValidIntersections(mgr, params.Requester, steward)
		if err != nil {
			return nil, fmt.Errorf("failed to collect valid intersections for %s: %w", steward, err)
		}
		decision.Archetypes = archetypes
		decision.ComputeProviders = computeProviders

		// Fire the submit-data-request act so the data-request fact and the
		// obligated-log duty appear on the execution graph. Failure to fire
		// is non-fatal for the validation outcome but is logged for audit.
		submitPhrase := fmt.Sprintf("submit-data-request(%s, %s).", requesterLit, stewardLit)
		if _, submitErr := mgr.SendPhrases(submitPhrase); submitErr != nil {
			r.logger.Warn("submit-data-request did not fire cleanly",
				zap.String("steward", steward),
				zap.String("requester", params.Requester),
				zap.Error(submitErr),
			)
		}

		result.PerSteward[steward] = decision
	}

	return result, nil
}

// collectValidIntersections derives the matched archetypes / compute providers
// for (requester, steward) by asking the reasoner directly for the derived
// valid-archetype / valid-compute-provider instances. The Layer-2 shared rules
// define these as the intersection of relation-allows-* and steward-supports-*,
// so a single generative query per dimension returns the already-intersected
// result set.
func (r *EflintReasoner) collectValidIntersections(mgr *eflint.Manager, requester, steward string) ([]string, []string, error) {
	reqLit := quoteEflintLiteral(requester)
	stLit := quoteEflintLiteral(steward)

	matchedArchetypes, err := r.loggedQueryInstances(mgr,
		fmt.Sprintf("?-archetype When valid-archetype(%s, %s, archetype).", reqLit, stLit))
	if err != nil {
		return nil, nil, err
	}

	matchedComputeProvs, err := r.loggedQueryInstances(mgr,
		fmt.Sprintf("?-compute-provider When valid-compute-provider(%s, %s, compute-provider).", reqLit, stLit))
	if err != nil {
		return nil, nil, err
	}

	return matchedArchetypes, matchedComputeProvs, nil
}

// queryHolds runs a single `?Holds(...)` phrase through SendPhrases and parses
// the response. The eFLINT server returns query-results entries that are
// "success" when the queried fact holds and something else (typically
// "failed") otherwise.
func queryHolds(mgr *eflint.Manager, factExpression string) (bool, error) {
	phrase := fmt.Sprintf("?Holds(%s).", factExpression)
	resp, err := mgr.SendPhrases(phrase)
	if err != nil {
		return false, fmt.Errorf("query phrase %q failed: %w", phrase, err)
	}
	if resp == nil {
		return false, fmt.Errorf("nil response for query %q", phrase)
	}
	for _, qr := range resp.QueryResults {
		if strings.EqualFold(strings.TrimSpace(qr), "success") {
			return true, nil
		}
	}
	return false, nil
}

// buildRequesterPhrases produces a minimal phrase block that grounds the
// given requester atoms (`+requester(X).`) without asserting any
// requested-steward or firing any Acts. Used by IntrospectStewardClauses to
// enable generative requester queries in a Layer-2-only session.
func buildRequesterPhrases(requesters []string) string {
	var b strings.Builder
	seen := map[string]struct{}{}
	for _, req := range requesters {
		if _, ok := seen[req]; ok {
			continue
		}
		seen[req] = struct{}{}
		b.WriteString("+requester(")
		b.WriteString(quoteEflintLiteral(req))
		b.WriteString(").\n")
	}
	return b.String()
}

// buildLayer3Phrases produces the Layer-3 request-fact block for one
// evaluation.
func buildLayer3Phrases(requesterLit string, stewards []string) string {
	var b strings.Builder
	b.WriteString("+requester(")
	b.WriteString(requesterLit)
	b.WriteString(").\n")
	seen := map[string]struct{}{}
	for _, st := range stewards {
		if _, ok := seen[st]; ok {
			continue
		}
		seen[st] = struct{}{}
		b.WriteString("+requested-steward(")
		b.WriteString(quoteEflintLiteral(st))
		b.WriteString(").\n")
	}
	return b.String()
}

// quoteEflintLiteral wraps a value as an eFLINT string literal. eFLINT requires
// all fact instance values to be quoted strings — bare atoms are not supported.
// Values containing special characters (e.g. backslashes or double-quotes) are
// escaped so they round-trip correctly through the eFLINT JSON protocol.
func quoteEflintLiteral(value string) string {
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

// IntrospectStewardClauses implements the read-only Layer 1 + Layer 2
// loader used by the policy-enforcer HTTP API. It pushes the shared rules
// and the steward's per-steward phrases onto a clean pool instance and uses
// targeted generative queries (`?-factType When …`) to materialise the
// steward-supports-* and relation-allows-* facts per requester. No Layer 3
// is pushed and no Acts are fired.
func (r *EflintReasoner) IntrospectStewardClauses(ctx context.Context, params IntrospectStewardClausesParams) (*StewardClauses, error) {
	if strings.TrimSpace(params.Steward) == "" {
		return nil, fmt.Errorf("steward is required")
	}
	if strings.TrimSpace(params.StewardPhrases) == "" {
		return nil, fmt.Errorf("no Layer-2 phrases for steward %q", params.Steward)
	}

	entry, err := r.pool.Acquire()
	if err != nil {
		return nil, fmt.Errorf("failed to acquire eFLINT instance: %w", err)
	}
	defer func() {
		go r.pool.Release(entry)
	}()
	mgr := entry.Manager

	if strings.TrimSpace(params.SharedRules) != "" {
		if _, err := mgr.SendPhrases(params.SharedRules); err != nil {
			return nil, fmt.Errorf("failed to load Layer-2 shared rules: %w", err)
		}
	}
	if _, err := mgr.SendPhrases(params.StewardPhrases); err != nil {
		return nil, fmt.Errorf("failed to load Layer-2 phrases for %s: %w", params.Steward, err)
	}

	// Ground the provided requester atoms so that generative queries of the
	// form `?-requester When has-relation(requester, steward).` can enumerate
	// them. These are the only Layer-3 facts pushed here; no requested-steward
	// facts and no Acts are fired, keeping the session read-only.
	if len(params.Requesters) > 0 {
		requesterPhrases := buildRequesterPhrases(params.Requesters)
		if _, err := mgr.SendPhrases(requesterPhrases); err != nil {
			return nil, fmt.Errorf("failed to ground requester facts: %w", err)
		}
	}

	stLit := quoteEflintLiteral(params.Steward)
	out := &StewardClauses{Steward: params.Steward}

	out.SupportedArchetypes, err = r.loggedQueryInstances(mgr,
		fmt.Sprintf("?-archetype When steward-supports-archetype(%s, archetype).", stLit))
	if err != nil {
		return nil, fmt.Errorf("failed to query steward-supports-archetype: %w", err)
	}
	out.SupportedComputeProviders, err = r.loggedQueryInstances(mgr,
		fmt.Sprintf("?-compute-provider When steward-supports-compute-provider(%s, compute-provider).", stLit))
	if err != nil {
		return nil, fmt.Errorf("failed to query steward-supports-compute-provider: %w", err)
	}

	// Enumerate requesters that have a relation to this steward. Each
	// agreement file must explicitly ground requester(X) instances so that
	// this generative query can enumerate them — exactly as archetype,
	// dataset, and compute-provider instances are grounded.
	requesters, err := r.loggedQueryInstances(mgr,
		fmt.Sprintf("?-requester When has-relation(requester, %s).", stLit))
	if err != nil {
		return nil, fmt.Errorf("failed to query requesters with has-relation to %s: %w", params.Steward, err)
	}

	for _, requester := range requesters {
		reqLit := quoteEflintLiteral(requester)
		clauses := RequesterClauses{Requester: requester}

		clauses.RequestTypes, err = r.loggedQueryInstances(mgr,
			fmt.Sprintf("?-request-type When relation-allows-request-type(%s, %s, request-type).", reqLit, stLit))
		if err != nil {
			return nil, fmt.Errorf("failed to query relation-allows-request-type for %s: %w", requester, err)
		}
		clauses.Datasets, err = r.loggedQueryInstances(mgr,
			fmt.Sprintf("?-dataset When relation-allows-dataset(%s, %s, dataset).", reqLit, stLit))
		if err != nil {
			return nil, fmt.Errorf("failed to query relation-allows-dataset for %s: %w", requester, err)
		}
		clauses.Archetypes, err = r.loggedQueryInstances(mgr,
			fmt.Sprintf("?-archetype When relation-allows-archetype(%s, %s, archetype).", reqLit, stLit))
		if err != nil {
			return nil, fmt.Errorf("failed to query relation-allows-archetype for %s: %w", requester, err)
		}
		clauses.ComputeProviders, err = r.loggedQueryInstances(mgr,
			fmt.Sprintf("?-compute-provider When relation-allows-compute-provider(%s, %s, compute-provider).", reqLit, stLit))
		if err != nil {
			return nil, fmt.Errorf("failed to query relation-allows-compute-provider for %s: %w", requester, err)
		}

		out.Relations = append(out.Relations, clauses)
	}

	return out, nil
}
