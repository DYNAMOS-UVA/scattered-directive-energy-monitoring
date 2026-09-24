package eflint

import (
	"encoding/json"
	"net/http"

	"github.com/Jorrit05/DYNAMOS/cmd/policy-enforcer/httpapi"
	"go.uber.org/zap"
)

// -----------------------------------------------------------------------------
// Instance API Handler
// -----------------------------------------------------------------------------

// InstanceAPIHandler handles HTTP requests for eFLINT instance lifecycle management.
// It provides endpoints for starting, stopping, and sending commands to the eFLINT server.
// When an instance_id is provided, the handler targets a specific pool instance;
// otherwise, it falls back to the default manager.
type InstanceAPIHandler struct {
	manager *Manager      // Default manager for backward compatibility
	pool    *InstancePool // Pool for instance_id-based lookups
	logger  *zap.Logger
}

// NewInstanceAPIHandler creates a new instance API handler with the given manager, pool, and logger.
func NewInstanceAPIHandler(manager *Manager, pool *InstancePool, logger *zap.Logger) *InstanceAPIHandler {
	return &InstanceAPIHandler{
		manager: manager,
		pool:    pool,
		logger:  logger,
	}
}

// resolveManagerByID returns the manager for a specific pool instance ID.
func (h *InstanceAPIHandler) resolveManagerByID(instanceID string) (*Manager, error) {
	if h.pool == nil {
		return h.manager, nil
	}

	entry, err := h.pool.GetByID(instanceID)
	if err != nil {
		return nil, err
	}
	return entry.Manager, nil
}

// resolveManager returns the manager for the given request.
// If instance_id is provided (query param), it looks up the pool entry.
// Otherwise, it returns the default manager.
func (h *InstanceAPIHandler) resolveManager(r *http.Request) (*Manager, error) {
	instanceID := r.URL.Query().Get("instance_id")
	if instanceID == "" {
		return h.manager, nil
	}

	return h.resolveManagerByID(instanceID)
}

// requireManager validates that instanceID is non-empty, resolves the corresponding
// pool manager, and writes an HTTP error response if anything fails.
// Returns the manager and true on success, or nil and false if an error was written.
func (h *InstanceAPIHandler) requireManager(w http.ResponseWriter, instanceID string) (*Manager, bool) {
	if instanceID == "" {
		httpapi.WriteError(w, http.StatusBadRequest, "instance_id is required")
		return nil, false
	}

	if h.pool == nil {
		httpapi.WriteError(w, http.StatusServiceUnavailable, "instance pool not configured")
		return nil, false
	}

	mgr, err := h.resolveManagerByID(instanceID)
	if err != nil {
		httpapi.WriteError(w, http.StatusNotFound, err.Error())
		return nil, false
	}
	return mgr, true
}

// -----------------------------------------------------------------------------
// Request/Response Types
// -----------------------------------------------------------------------------

// StatusResponse represents the response for status-related endpoints.
type StatusResponse struct {
	Running       bool            `json:"running"`                  // Whether the instance is running
	Port          int             `json:"port,omitempty"`           // The port the instance is listening on
	ModelLocation string          `json:"model_location,omitempty"` // Path to the loaded model
	EflintStatus  json.RawMessage `json:"eflint_status,omitempty"`  // Status response from the eFLINT server
}

// StartRequest represents the request body for starting an instance.
type StartRequest struct {
	ModelLocation string `json:"model_location"`        // Path to the eFLINT model file
	InstanceID    string `json:"instance_id,omitempty"` // Optional: target a specific pool instance
	Force         bool   `json:"force,omitempty"`       // Deprecated: no longer required; start always replaces an existing process
}

// CommandRequest represents the request body for sending a command.
// The Command field can be either:
// - A string containing the JSON command (for backward compatibility)
// - A JSON object that will be serialized before sending to eFLINT
type CommandRequest struct {
	Command    json.RawMessage `json:"command"`               // The JSON command to send to eFLINT (string or object)
	InstanceID string          `json:"instance_id,omitempty"` // Optional: target a specific pool instance
}

// InstanceRequest represents a request body that targets a specific pool instance.
// Used by stop, restart, and other instance-targeted endpoints.
type InstanceRequest struct {
	InstanceID string `json:"instance_id"` // Target pool instance
}

// CommandResponse represents the response from a command execution.
type CommandResponse struct {
	Parsed json.RawMessage `json:"response"` // The parsed JSON response from eFLINT
}

// AllowedArchetypesResponse represents the response for querying allowed archetypes.
type AllowedArchetypesResponse struct {
	Organization string   `json:"organization"` // The organization/steward
	Requester    string   `json:"requester"`    // The user/requester
	Archetypes   []string `json:"archetypes"`   // List of allowed archetypes
}

// InstanceListResponse represents the response for listing all pool instances.
type InstanceListResponse struct {
	Instances []InstanceInfo `json:"instances"`
	Total     int            `json:"total"`
	Available int            `json:"available"`
}

// PoolSizeResponse represents the response for pool size queries.
type PoolSizeResponse struct {
	TargetSize int `json:"target_size"`
	Total      int `json:"total"`
	Idle       int `json:"idle"`
	InUse      int `json:"in_use"`
	Unhealthy  int `json:"unhealthy"`
}

// PoolSizeRequest represents the request body for changing pool size.
type PoolSizeRequest struct {
	TargetSize int `json:"target_size"`
}

// -----------------------------------------------------------------------------
// Utility Functions
// -----------------------------------------------------------------------------

// mustMarshal marshals a value to JSON, returning empty bytes on error.
// Used for simple string wrapping in error cases.
func mustMarshal(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}

// parseCommandToString converts a json.RawMessage command to a string suitable for eFLINT.
// It handles two cases:
//  1. The command is a JSON string (e.g., `"{"command": "status"}"`) - returns the unquoted string
//  2. The command is a JSON object (e.g., `{"command": "status"}`) - re-marshals to compact single-line JSON
//
// This allows clients to send commands either as:
//   - {"command": "{\"command\": \"phrase\", \"text\": \"+fact(\\\"val\\\").\"}"}  (string, double-escaping)
//   - {"command": {"command": "phrase", "text": "+fact(\"val\")."}}                (object, standard escaping)
//
// The object format is recommended because quotes in eFLINT phrases only need standard JSON escaping,
// whereas the string format requires double-escaping (escaping the escape characters).
//
// Note: Object commands are always re-marshaled to compact JSON (no newlines) because the eFLINT
// server expects single-line JSON input.
func parseCommandToString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}

	// Trim whitespace to check the first character
	trimmed := raw
	for len(trimmed) > 0 && (trimmed[0] == ' ' || trimmed[0] == '\t' || trimmed[0] == '\n' || trimmed[0] == '\r') {
		trimmed = trimmed[1:]
	}

	if len(trimmed) == 0 {
		return "", nil
	}

	// Check if the raw message is a JSON string (starts with a quote)
	if trimmed[0] == '"' {
		// It's a string - unmarshal to get the actual string value
		var str string
		if err := json.Unmarshal(raw, &str); err != nil {
			return "", err
		}
		return str, nil
	}

	// It's an object or other JSON value - unmarshal and re-marshal to ensure compact format
	// The eFLINT server expects single-line JSON without newlines
	var obj interface{}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", err
	}

	// Re-marshal with compact encoding (no newlines, no indentation)
	compactJSON, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}

	return string(compactJSON), nil
}

// -----------------------------------------------------------------------------
// Handler Methods
// -----------------------------------------------------------------------------

// GetStatus returns the current status of the eFLINT instance.
// If instance_id query param is provided, returns status of that pool instance.
// GET /eflint/status?instance_id=X
func (h *InstanceAPIHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	mgr, err := h.resolveManager(r)
	if err != nil {
		httpapi.WriteError(w, http.StatusNotFound, err.Error())
		return
	}

	status := mgr.Status()

	response := StatusResponse{
		Running:       status.Running,
		Port:          status.Port,
		ModelLocation: status.ModelLocation,
	}

	// If the instance is running, query the eFLINT server for its status
	if status.Running {
		eflintStatus, err := mgr.GetEflintStatus()
		if err != nil {
			h.logger.Warn("failed to get eFLINT server status", zap.Error(err))
			// Continue without the eFLINT status - the instance might still be starting up
		} else if json.Valid([]byte(eflintStatus)) {
			response.EflintStatus = json.RawMessage(eflintStatus)
		}
	}

	httpapi.WriteJSON(w, http.StatusOK, response)
}

// Start starts or restarts an eFLINT instance with the given model.
// If instance_id is provided, it targets that pool instance.
// Otherwise, it uses the default manager.
// POST /eflint/start
func (h *InstanceAPIHandler) Start(w http.ResponseWriter, r *http.Request) {
	var req StartRequest
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.ModelLocation == "" {
		httpapi.WriteError(w, http.StatusBadRequest, "model_location is required")
		return
	}

	mgr := h.manager
	if req.InstanceID != "" {
		var ok bool
		mgr, ok = h.requireManager(w, req.InstanceID)
		if !ok {
			return
		}
	}

	if err := mgr.Start(req.ModelLocation); err != nil {
		h.logger.Error("failed to start instance", zap.Error(err))
		httpapi.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	status := mgr.Status()
	httpapi.WriteJSON(w, http.StatusOK, StatusResponse{
		Running:       status.Running,
		Port:          status.Port,
		ModelLocation: status.ModelLocation,
	})
}

// Stop stops the running eFLINT instance.
// POST /eflint/stop
func (h *InstanceAPIHandler) Stop(w http.ResponseWriter, r *http.Request) {
	var req InstanceRequest
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	mgr, ok := h.requireManager(w, req.InstanceID)
	if !ok {
		return
	}

	if err := mgr.Stop(); err != nil {
		if err == ErrInstanceNotFound {
			httpapi.WriteError(w, http.StatusNotFound, "no instance running")
			return
		}
		h.logger.Error("failed to stop instance", zap.Error(err))
		httpapi.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	httpapi.WriteJSON(w, http.StatusOK, StatusResponse{Running: false})
}

// Restart restarts the eFLINT instance.
// POST /eflint/restart
func (h *InstanceAPIHandler) Restart(w http.ResponseWriter, r *http.Request) {
	var req InstanceRequest
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	mgr, ok := h.requireManager(w, req.InstanceID)
	if !ok {
		return
	}

	if err := mgr.Restart(); err != nil {
		if err == ErrInstanceNotFound {
			httpapi.WriteError(w, http.StatusNotFound, "no instance running")
			return
		}
		h.logger.Error("failed to restart instance", zap.Error(err))
		httpapi.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	status := mgr.Status()
	httpapi.WriteJSON(w, http.StatusOK, StatusResponse{
		Running:       status.Running,
		Port:          status.Port,
		ModelLocation: status.ModelLocation,
	})
}

// SendCommand sends a command to the eFLINT instance.
// POST /eflint/command
//
// The command field can be either:
//   - A string containing the JSON command: {"command": "{\"command\": \"status\"}"}
//   - A JSON object that will be serialized: {"command": {"command": "status"}}
//
// Optionally specify instance_id to target a specific pool instance.
func (h *InstanceAPIHandler) SendCommand(w http.ResponseWriter, r *http.Request) {
	var req CommandRequest
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if len(req.Command) == 0 {
		httpapi.WriteError(w, http.StatusBadRequest, "command is required")
		return
	}

	mgr, ok := h.requireManager(w, req.InstanceID)
	if !ok {
		return
	}

	// Convert the command to a string that can be sent to eFLINT
	commandStr, err := parseCommandToString(req.Command)
	if err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid command format: "+err.Error())
		return
	}

	response, err := mgr.SendCommand(commandStr)
	if err != nil {
		if err == ErrInstanceNotFound {
			httpapi.WriteError(w, http.StatusNotFound, "no instance running")
			return
		}
		if err == ErrInstanceNotRunning {
			httpapi.WriteError(w, http.StatusServiceUnavailable, "instance is not running")
			return
		}
		h.logger.Error("failed to send command", zap.Error(err))
		httpapi.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Parse the response as JSON
	var parsed json.RawMessage
	if json.Valid([]byte(response)) {
		parsed = json.RawMessage(response)
	} else {
		parsed = json.RawMessage(`{"raw": ` + string(mustMarshal(response)) + `}`)
	}

	httpapi.WriteJSON(w, http.StatusOK, CommandResponse{
		Parsed: parsed,
	})
}

// -----------------------------------------------------------------------------
// Pool Management Endpoints
// -----------------------------------------------------------------------------

// ListInstances returns a list of all pool instances with their statuses.
// GET /eflint/instances
func (h *InstanceAPIHandler) ListInstances(w http.ResponseWriter, r *http.Request) {
	if h.pool == nil {
		httpapi.WriteError(w, http.StatusServiceUnavailable, "instance pool not configured")
		return
	}

	instances := h.pool.ListInstances()
	_, idle, _, _ := h.pool.PoolStats()

	httpapi.WriteJSON(w, http.StatusOK, InstanceListResponse{
		Instances: instances,
		Total:     len(instances),
		Available: idle,
	})
}

// GetPoolSize returns the current pool target size and actual counts.
// GET /eflint/instances/pool-size
func (h *InstanceAPIHandler) GetPoolSize(w http.ResponseWriter, r *http.Request) {
	if h.pool == nil {
		httpapi.WriteError(w, http.StatusServiceUnavailable, "instance pool not configured")
		return
	}

	total, idle, inUse, unhealthy := h.pool.PoolStats()

	httpapi.WriteJSON(w, http.StatusOK, PoolSizeResponse{
		TargetSize: h.pool.GetTargetSize(),
		Total:      total,
		Idle:       idle,
		InUse:      inUse,
		Unhealthy:  unhealthy,
	})
}

// SetPoolSize adjusts the pool target size at runtime.
// PUT /eflint/instances/pool-size
func (h *InstanceAPIHandler) SetPoolSize(w http.ResponseWriter, r *http.Request) {
	if h.pool == nil {
		httpapi.WriteError(w, http.StatusServiceUnavailable, "instance pool not configured")
		return
	}

	var req PoolSizeRequest
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.TargetSize < 1 {
		httpapi.WriteError(w, http.StatusBadRequest, "target_size must be at least 1")
		return
	}

	if err := h.pool.Resize(req.TargetSize); err != nil {
		h.logger.Error("failed to resize pool", zap.Error(err))
		httpapi.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	total, idle, inUse, unhealthy := h.pool.PoolStats()

	httpapi.WriteJSON(w, http.StatusOK, PoolSizeResponse{
		TargetSize: h.pool.GetTargetSize(),
		Total:      total,
		Idle:       idle,
		InUse:      inUse,
		Unhealthy:  unhealthy,
	})
}
