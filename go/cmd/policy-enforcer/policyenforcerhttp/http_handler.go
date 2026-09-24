// Package policyenforcerhttp implements the HTTP endpoints used by policy
// engineers to test the effects of a Layer-2 agreement without going through
// the RabbitMQ request-approval flow.
//
// Both endpoints share the same code path the request-approval flow uses, so
// they exercise the production layered eFLINT pipeline:
//
//	GET  /policy-enforcer/allowed-clauses?steward=...[&requester=...]
//	     → Loads Layer 1 + Layer 2 (shared + per-steward) onto a clean pool
//	       instance and returns the steward-supports-* and relation-allows-*
//	       facts for the steward (optionally narrowed to one requester).
//
//	POST /policy-enforcer/validate
//	     → Body matches the relevant fields of pb.RequestApproval (user +
//	       data_providers). Runs ValidationService.ValidateRequest and
//	       returns the resulting pb.ValidationResponse. The pool restarts the
//	       eFLINT process when the entry is released, so the simulated
//	       submit-data-request acts have no persistent side effects.
package policyenforcerhttp

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/httpapi"
	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/service"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
)

// HTTPHandler exposes the policy-engineer testing endpoints over HTTP. It
// delegates to the same ValidationService that processes RabbitMQ requests
// so the HTTP path and the production path stay in lock-step.
type HTTPHandler struct {
	validationService *service.ValidationService
	logger            *zap.Logger
}

// NewHTTPHandler creates a new HTTP handler for the policy enforcer.
func NewHTTPHandler(svc *service.ValidationService, logger *zap.Logger) *HTTPHandler {
	return &HTTPHandler{
		validationService: svc,
		logger:            logger,
	}
}

// GetAllowedClauses handles
//
//	GET /policy-enforcer/allowed-clauses?steward=VU[&requester=user@example.com]
//
// `steward` is required and identifies the data steward to introspect.
// `requester` is optional; when set, the response is narrowed to only that
// requester's relation.
func (h *HTTPHandler) GetAllowedClauses(w http.ResponseWriter, r *http.Request) {
	steward := r.URL.Query().Get("steward")
	if steward == "" {
		httpapi.WriteError(w, http.StatusBadRequest, "steward query parameter is required")
		return
	}
	requester := r.URL.Query().Get("requester")
	start := time.Now()

	clauses, timing, err := h.validationService.GetAllowedClausesForStewardWithTiming(r.Context(), steward, requester)
	if err != nil {
		h.logger.Error("policy enforcer: GetAllowedClauses failed",
			zap.String("steward", steward),
			zap.Error(err),
		)
		httpapi.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if clauses == nil {
		httpapi.WriteError(w, http.StatusNotFound, "no agreement registered for steward")
		return
	}

	execMs := time.Since(start).Milliseconds()
	w.Header().Set("X-Execution-Time-Ms", strconv.FormatInt(execMs, 10))
	w.Header().Set("X-Stage-Resolve-Provider-Ms", strconv.FormatInt(timing.ResolveProviderMs, 10))
	w.Header().Set("X-Stage-Load-Steward-Phrases-Ms", strconv.FormatInt(timing.LoadStewardPhrasesMs, 10))
	w.Header().Set("X-Stage-Load-Shared-Rules-Ms", strconv.FormatInt(timing.LoadSharedRulesMs, 10))
	w.Header().Set("X-Stage-Reasoner-Introspect-Ms", strconv.FormatInt(timing.ReasonerIntrospectMs, 10))
	w.Header().Set("X-Stage-Filter-Relations-Ms", strconv.FormatInt(timing.FilterRelationsMs, 10))
	w.Header().Set("X-Stage-Service-Total-Ms", strconv.FormatInt(timing.ServiceTotalMs, 10))

	serverTiming := strings.Join([]string{
		fmt.Sprintf("allowed-clauses-total;dur=%d", execMs),
		fmt.Sprintf("service-total;dur=%d", timing.ServiceTotalMs),
		fmt.Sprintf("resolve-provider;dur=%d", timing.ResolveProviderMs),
		fmt.Sprintf("load-steward-phrases;dur=%d", timing.LoadStewardPhrasesMs),
		fmt.Sprintf("load-shared-rules;dur=%d", timing.LoadSharedRulesMs),
		fmt.Sprintf("reasoner-introspect;dur=%d", timing.ReasonerIntrospectMs),
		fmt.Sprintf("filter-relations;dur=%d", timing.FilterRelationsMs),
	}, ", ")
	w.Header().Set("Server-Timing", serverTiming)
	httpapi.WriteJSON(w, http.StatusOK, clauses)
}

// ValidateRequestBody mirrors the relevant fields of pb.RequestApproval. We do
// not unmarshal directly into pb.RequestApproval because the protobuf-generated
// JSON shape (snake_case + extra wrapping) is awkward to author by hand.
type ValidateRequestBody struct {
	User struct {
		ID       string `json:"id"`
		UserName string `json:"user_name"`
	} `json:"user"`
	DataProviders []string        `json:"data_providers"`
	Type          string          `json:"type,omitempty"`
	Options       map[string]bool `json:"options,omitempty"`
}

// validationResponseDTO is a custom JSON representation of pb.ValidationResponse.
// Protobuf-generated structs tag every field with omitempty, which drops zero
// values such as request_approved=false and empty maps. This DTO controls the
// serialisation explicitly so all semantically meaningful fields are always
// present in the response body.
type validationResponseDTO struct {
	Type                 string                      `json:"type"`
	RequestType          string                      `json:"request_type"`
	ValidDataproviders   map[string]*dataProviderDTO `json:"valid_dataproviders"`
	InvalidDataproviders []string                    `json:"invalid_dataproviders"`
	Auth                 *authDTO                    `json:"auth,omitempty"`
	User                 *userDTO                    `json:"user,omitempty"`
	RequestApproved      bool                        `json:"request_approved"`
	ValidArchetypes      *userArchetypesDTO          `json:"valid_archetypes,omitempty"`
}

type dataProviderDTO struct {
	Archetypes       []string `json:"archetypes"`
	ComputeProviders []string `json:"compute_providers"`
}

type authDTO struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

type userDTO struct {
	ID       string `json:"id"`
	UserName string `json:"user_name"`
}

type userArchetypesDTO struct {
	UserName   string                           `json:"user_name"`
	Archetypes map[string]*allowedArchetypesDTO `json:"archetypes,omitempty"`
}

type allowedArchetypesDTO struct {
	Archetypes []string `json:"archetypes"`
}

// toValidationResponseDTO maps a pb.ValidationResponse to a validationResponseDTO
// so that zero-value fields (false bool, empty map) are preserved in JSON output.
func toValidationResponseDTO(r *pb.ValidationResponse) validationResponseDTO {
	dto := validationResponseDTO{
		Type:                 r.Type,
		RequestType:          r.RequestType,
		ValidDataproviders:   make(map[string]*dataProviderDTO),
		InvalidDataproviders: r.InvalidDataproviders,
		RequestApproved:      r.RequestApproved,
	}

	if dto.InvalidDataproviders == nil {
		dto.InvalidDataproviders = []string{}
	}

	for k, v := range r.ValidDataproviders {
		dp := &dataProviderDTO{
			Archetypes:       v.Archetypes,
			ComputeProviders: v.ComputeProviders,
		}
		if dp.Archetypes == nil {
			dp.Archetypes = []string{}
		}
		if dp.ComputeProviders == nil {
			dp.ComputeProviders = []string{}
		}
		dto.ValidDataproviders[k] = dp
	}

	if r.Auth != nil {
		dto.Auth = &authDTO{
			AccessToken:  r.Auth.AccessToken,
			RefreshToken: r.Auth.RefreshToken,
		}
	}

	if r.User != nil {
		dto.User = &userDTO{
			ID:       r.User.Id,
			UserName: r.User.UserName,
		}
	}

	if r.ValidArchetypes != nil {
		archetypes := &userArchetypesDTO{
			UserName:   r.ValidArchetypes.UserName,
			Archetypes: make(map[string]*allowedArchetypesDTO),
		}
		for k, v := range r.ValidArchetypes.Archetypes {
			aa := &allowedArchetypesDTO{Archetypes: v.Archetypes}
			if aa.Archetypes == nil {
				aa.Archetypes = []string{}
			}
			archetypes.Archetypes[k] = aa
		}
		if len(archetypes.Archetypes) == 0 {
			archetypes.Archetypes = nil
		}
		dto.ValidArchetypes = archetypes
	}

	return dto
}

// ValidateRequest handles
//
//	POST /policy-enforcer/validate
//	Body: { "user": {"id":"1","user_name":"..."}, "data_providers": ["VU","UVA"], ... }
//
// The body is mapped to a pb.RequestApproval and run through the same
// ValidationService used by the RabbitMQ handler. The response is the
// resulting pb.ValidationResponse encoded as JSON.
func (h *HTTPHandler) ValidateRequest(w http.ResponseWriter, r *http.Request) {
	var body ValidateRequestBody
	if err := httpapi.DecodeJSON(r, &body); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.User.UserName == "" {
		httpapi.WriteError(w, http.StatusBadRequest, "user.user_name is required")
		return
	}
	if len(body.DataProviders) == 0 {
		httpapi.WriteError(w, http.StatusBadRequest, "data_providers must contain at least one steward")
		return
	}

	requestType := body.Type
	if requestType == "" {
		requestType = "requestApproval"
	}

	req := &pb.RequestApproval{
		Type: requestType,
		User: &pb.User{
			Id:       body.User.ID,
			UserName: body.User.UserName,
		},
		DataProviders: body.DataProviders,
		Options:       body.Options,
	}

	resp := h.validationService.ValidateRequest(r.Context(), req)
	httpapi.WriteJSON(w, http.StatusOK, toValidationResponseDTO(resp))
}
