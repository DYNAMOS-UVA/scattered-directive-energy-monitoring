package eflint

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/httpapi"
	"github.com/Jorrit05/DYNAMOS/pkg/api"
	"go.uber.org/zap"
)

// -----------------------------------------------------------------------------
// State API Handler
// -----------------------------------------------------------------------------

// StateAPIHandler handles HTTP requests for eFLINT state management.
// This is a POC (Proof of Concept) for stateful session management with
// state persistence, allowing export/import of eFLINT execution graphs.
// When an instance_id is provided, it targets a specific pool instance.
type StateAPIHandler struct {
	stateManager *StateManager  // Default state manager
	pool         *InstancePool  // Pool for instance_id-based lookups
	logger       *zap.Logger
}

// NewStateAPIHandler creates a new StateAPIHandler with the given manager, pool, and logger.
func NewStateAPIHandler(stateManager *StateManager, pool *InstancePool, logger *zap.Logger) *StateAPIHandler {
	return &StateAPIHandler{
		stateManager: stateManager,
		pool:         pool,
		logger:       logger,
	}
}

// resolveStateManager returns the state manager for the given request.
// If instance_id query param is provided, it resolves the pool entry's state manager.
func (h *StateAPIHandler) resolveStateManager(r *http.Request) (*StateManager, error) {
	instanceID := r.URL.Query().Get("instance_id")
	if instanceID == "" {
		return h.stateManager, nil
	}

	if h.pool == nil {
		return h.stateManager, nil
	}

	entry, err := h.pool.GetByID(instanceID)
	if err != nil {
		return nil, err
	}
	return entry.StateManager, nil
}

// -----------------------------------------------------------------------------
// Request/Response Types
// -----------------------------------------------------------------------------

// ExportStateResponse represents the response for state export.
type ExportStateResponse struct {
	Success bool                  `json:"success"`         // Whether the export was successful
	State   *api.EflintSavedState `json:"state,omitempty"` // The exported state data
	Error   string                `json:"error,omitempty"` // Error message if failed
}

// ImportStateRequest represents the request body for importing state.
type ImportStateRequest struct {
	State *api.EflintSavedState `json:"state"` // The state to import
}

// CheckpointRequest represents a request for checkpoint operations.
type CheckpointRequest struct {
	Name string `json:"name"` // Name of the checkpoint
}

// CheckpointListResponse represents the list of available checkpoints.
type CheckpointListResponse struct {
	Checkpoints []string `json:"checkpoints"` // List of checkpoint names
}

// SuccessResponse represents a generic success response.
type SuccessResponse struct {
	Success bool        `json:"success"`           // Whether the operation succeeded
	Message string      `json:"message,omitempty"` // Optional success message
	Data    interface{} `json:"data,omitempty"`    // Optional additional data
}

// StateResponse represents the response for the GetState endpoint.
type StateResponse struct {
	State json.RawMessage `json:"state"` // The current execution graph state
}

// -----------------------------------------------------------------------------
// Handler Methods
// -----------------------------------------------------------------------------

// GetState retrieves the current execution graph state of the eFLINT instance.
// GET /eflint/state?instance_id=X
func (h *StateAPIHandler) GetState(w http.ResponseWriter, r *http.Request) {
	sm, err := h.resolveStateManager(r)
	if err != nil {
		httpapi.WriteError(w, http.StatusNotFound, err.Error())
		return
	}

	response, err := sm.GetState()
	if err != nil {
		if err == ErrInstanceNotRunning {
			httpapi.WriteError(w, http.StatusServiceUnavailable, "instance is not running")
			return
		}
		h.logger.Error("failed to get state", zap.Error(err))
		httpapi.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Parse the response as JSON
	var state json.RawMessage
	if json.Valid([]byte(response)) {
		state = json.RawMessage(response)
	} else {
		state = json.RawMessage(`{"raw": "` + response + `"}`)
	}

	httpapi.WriteJSON(w, http.StatusOK, StateResponse{
		State: state,
	})
}

// ExportState exports the current eFLINT state for persistence.
// POST /eflint/state/export
func (h *StateAPIHandler) ExportState(w http.ResponseWriter, r *http.Request) {
	state, err := h.stateManager.ExportState()
	if err != nil {
		if err == ErrInstanceNotRunning {
			httpapi.WriteError(w, http.StatusServiceUnavailable, "instance is not running")
			return
		}
		h.logger.Error("failed to export state", zap.Error(err))
		httpapi.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	httpapi.WriteJSON(w, http.StatusOK, ExportStateResponse{
		Success: true,
		State:   state,
	})
}

// ImportState imports a previously exported state.
// POST /eflint/state/import
func (h *StateAPIHandler) ImportState(w http.ResponseWriter, r *http.Request) {
	var req ImportStateRequest
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.State == nil {
		httpapi.WriteError(w, http.StatusBadRequest, "state is required")
		return
	}

	if err := h.stateManager.ImportState(req.State); err != nil {
		if err == ErrInstanceNotRunning {
			httpapi.WriteError(w, http.StatusServiceUnavailable, "instance is not running")
			return
		}
		h.logger.Error("failed to import state", zap.Error(err))
		httpapi.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	httpapi.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "state imported successfully",
	})
}

// CreateCheckpoint creates a named checkpoint of the current state
// POST /eflint/state/checkpoint
func (h *StateAPIHandler) CreateCheckpoint(w http.ResponseWriter, r *http.Request) {
	var req CheckpointRequest
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name == "" {
		httpapi.WriteError(w, http.StatusBadRequest, "name is required")
		return
	}

	state, err := h.stateManager.CreateCheckpoint(req.Name)
	if err != nil {
		if err == ErrInstanceNotRunning {
			httpapi.WriteError(w, http.StatusServiceUnavailable, "instance is not running")
			return
		}
		h.logger.Error("failed to create checkpoint", zap.Error(err))
		httpapi.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	httpapi.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"success":    true,
		"checkpoint": req.Name,
		"state_id":   state.ID,
		"saved_at":   state.SavedAt,
	})
}

// RestoreCheckpoint restores a previously created checkpoint
// POST /eflint/state/checkpoint/restore
// NOTE: Due to a bug in the eFLINT server, full state restoration may not work.
// In that case, the instance will be restarted to the initial model state.
func (h *StateAPIHandler) RestoreCheckpoint(w http.ResponseWriter, r *http.Request) {
	var req CheckpointRequest
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name == "" {
		httpapi.WriteError(w, http.StatusBadRequest, "name is required")
		return
	}

	if err := h.stateManager.RestoreCheckpoint(req.Name); err != nil {
		if err == ErrInstanceNotRunning {
			httpapi.WriteError(w, http.StatusServiceUnavailable, "instance is not running")
			return
		}

		// Check if the error indicates the instance was restarted
		errStr := err.Error()
		if strings.Contains(errStr, "restarted to initial state") {
			h.logger.Warn("checkpoint restore failed, instance restarted to initial state", zap.Error(err))
			httpapi.WriteJSON(w, http.StatusOK, map[string]interface{}{
				"success":  false,
				"warning":  "eFLINT server does not support load-export; instance was restarted to initial model state instead",
				"restored": "initial",
				"note":     "This is a limitation of the eFLINT server's load-export functionality",
			})
			return
		}

		h.logger.Error("failed to restore checkpoint", zap.Error(err))
		httpapi.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	httpapi.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"message":  "checkpoint restored successfully",
		"restored": req.Name,
	})
}

// ListCheckpoints lists all available checkpoints
// GET /eflint/state/checkpoints
func (h *StateAPIHandler) ListCheckpoints(w http.ResponseWriter, r *http.Request) {
	states, err := h.stateManager.ListSavedStates()
	if err != nil {
		h.logger.Error("failed to list checkpoints", zap.Error(err))
		httpapi.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Filter only checkpoints
	var checkpoints []string
	for _, s := range states {
		if len(s) > 11 && s[:11] == "checkpoint-" {
			checkpoints = append(checkpoints, s[11:])
		}
	}

	httpapi.WriteJSON(w, http.StatusOK, CheckpointListResponse{
		Checkpoints: checkpoints,
	})
}

// DeleteCheckpoint deletes a named checkpoint
// DELETE /eflint/state/checkpoint/{name}
func (h *StateAPIHandler) DeleteCheckpoint(w http.ResponseWriter, r *http.Request) {
	const prefix = "/eflint/state/checkpoint/"
	name := strings.TrimPrefix(r.URL.Path, prefix)
	if name == "" || name == "restore" {
		httpapi.WriteError(w, http.StatusBadRequest, "checkpoint name is required")
		return
	}

	if err := h.stateManager.DeleteSavedState("checkpoint-" + name); err != nil {
		h.logger.Error("failed to delete checkpoint", zap.Error(err))
		httpapi.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	httpapi.WriteJSON(w, http.StatusOK, SuccessResponse{
		Success: true,
		Message: "checkpoint deleted successfully",
	})
}
