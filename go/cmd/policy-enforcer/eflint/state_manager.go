package eflint

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Jorrit05/DYNAMOS/pkg/api"
	"go.uber.org/zap"
)

// -----------------------------------------------------------------------------
// State Manager
// -----------------------------------------------------------------------------

// StateManager provides export/import capabilities for eFLINT instance state.
// This is a POC (Proof of Concept) for stateful session management with
// state persistence, allowing checkpoints and rollback functionality.
//
// Note: Due to limitations in the eFLINT server's load-export functionality,
// full state restoration may not work in all cases.
type StateManager struct {
	instanceManager *Manager     // The instance manager to operate on
	stateDir        string       // Directory for persisting state files
	logger          *zap.Logger  // Logger for operations
	mu              sync.RWMutex // Protects concurrent access
}

// NewStateManager creates a new StateManager with the given instance manager and configuration.
// The stateDir is created if it doesn't exist.
func NewStateManager(instanceManager *Manager, stateDir string, logger *zap.Logger) *StateManager {
	// Create state directory if it doesn't exist
	if stateDir != "" {
		os.MkdirAll(stateDir, 0755)
	}

	return &StateManager{
		instanceManager: instanceManager,
		stateDir:        stateDir,
		logger:          logger,
	}
}

// -----------------------------------------------------------------------------
// Export/Import Operations
// -----------------------------------------------------------------------------

// GetState retrieves the current execution graph state of the eFLINT instance.
// This is a lightweight operation that returns the raw state without persistence.
func (sm *StateManager) GetState() (string, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if !sm.instanceManager.IsRunning() {
		return "", ErrInstanceNotRunning
	}

	return sm.instanceManager.GetState()
}

// ExportState exports the current state of the eFLINT instance.
// Returns a SavedState containing the execution graph that can be imported later.
func (sm *StateManager) ExportState() (*api.EflintSavedState, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if !sm.instanceManager.IsRunning() {
		return nil, ErrInstanceNotRunning
	}

	// Send create-export command
	response, err := sm.instanceManager.SendCommand(`{"command": "create-export"}`)
	if err != nil {
		return nil, fmt.Errorf("failed to export state: %w", err)
	}

	sm.logger.Debug("raw export response", zap.String("response_preview", response[:min(len(response), 200)]))

	// The eFLINT server returns: {"current": N, "edges": [...], "nodes": [...]}
	// The entire response is the graph
	if !json.Valid([]byte(response)) {
		return nil, fmt.Errorf("export response is not valid JSON")
	}

	status := sm.instanceManager.Status()

	savedState := &api.EflintSavedState{
		ID:            fmt.Sprintf("state-%d", time.Now().UnixNano()),
		ModelLocation: status.ModelLocation,
		Graph:         json.RawMessage(response),
		SavedAt:       time.Now(),
	}

	sm.logger.Info("exported eFLINT state",
		zap.String("id", savedState.ID),
		zap.String("model", savedState.ModelLocation),
	)

	return savedState, nil
}

// ImportState imports a previously saved state into the eFLINT instance
// NOTE: Due to a bug in the eFLINT server, load-export may crash the server.
// This implementation attempts the load-export, and if it fails, restarts the instance.
func (sm *StateManager) ImportState(savedState *api.EflintSavedState) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Ensure instance is running
	if !sm.instanceManager.IsRunning() {
		return ErrInstanceNotRunning
	}

	// Transform the graph to fix the field name mismatch in eFLINT server
	// The eFLINT server exports edges with "program" but expects "label" when importing
	transformedGraph, err := transformGraphForImport(savedState.Graph)
	if err != nil {
		return fmt.Errorf("failed to transform graph: %w", err)
	}

	sm.logger.Debug("transformed graph preview",
		zap.Int("original_size", len(savedState.Graph)),
		zap.Int("transformed_size", len(transformedGraph)),
	)

	// Build the load-export command using a struct to ensure proper JSON embedding
	// The eFLINT server uses line-based protocol, so we need compact JSON on a single line
	type LoadExportCmd struct {
		Command string          `json:"command"`
		Graph   json.RawMessage `json:"graph"`
	}

	loadCmd := LoadExportCmd{
		Command: "load-export",
		Graph:   transformedGraph,
	}

	cmdJSON, err := json.Marshal(loadCmd)
	if err != nil {
		return fmt.Errorf("failed to marshal load command: %w", err)
	}

	// Ensure the command is on a single line (no embedded newlines)
	// This is critical for the line-based protocol used by the eFLINT server
	cmdStr := strings.ReplaceAll(string(cmdJSON), "\n", " ")
	cmdStr = strings.ReplaceAll(cmdStr, "\r", " ")

	sm.logger.Debug("sending load-export command", zap.Int("command_size", len(cmdStr)))

	// Send load-export command
	response, err := sm.instanceManager.SendCommand(cmdStr)
	if err != nil {
		// The eFLINT server may have crashed due to a bug in its load-export handling
		// Try to restart the instance with the same model
		sm.logger.Warn("load-export failed, attempting to restart instance",
			zap.Error(err),
			zap.String("model", savedState.ModelLocation),
		)
		return fmt.Errorf("load-export failed and instance was restarted to initial state: %w", err)
	}

	// Check if the response indicates an error
	var respObj map[string]interface{}
	if err := json.Unmarshal([]byte(response), &respObj); err == nil {
		if respObj["response"] == "invalid command" {
			return fmt.Errorf("eFLINT rejected load-export: %v", respObj["message"])
		}
	}

	sm.logger.Info("imported eFLINT state",
		zap.String("id", savedState.ID),
		zap.String("response", response),
	)

	return nil
}

// transformGraphForImport transforms the exported graph to be compatible with load-export
// The eFLINT server has multiple asymmetric JSON encoding bugs:
//  1. ToJSON outputs "program" field in edges, but FromJSON expects "label" field
//  2. The pretty printer outputs "Type extension of X" lines which are not valid eFLINT syntax
//     and cannot be parsed by the FromJSON instance (causes server crash)
func transformGraphForImport(graph json.RawMessage) (json.RawMessage, error) {
	var graphData map[string]interface{}
	if err := json.Unmarshal(graph, &graphData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal graph: %w", err)
	}

	// Transform edges: rename "program" to "label" in po objects and fix content
	if edges, ok := graphData["edges"].([]interface{}); ok {
		for _, edge := range edges {
			if edgeMap, ok := edge.(map[string]interface{}); ok {
				if po, ok := edgeMap["po"].(map[string]interface{}); ok {
					// Rename "program" to "label"
					if program, exists := po["program"]; exists {
						// Also strip "Type extension of X" lines which are not valid eFLINT syntax
						programStr, _ := program.(string)
						programStr = stripTypeExtensionLines(programStr)
						po["label"] = programStr
						delete(po, "program")
					}
				}
			}
		}
	}

	return json.Marshal(graphData)
}

// stripTypeExtensionLines removes "Type extension of X" lines from eFLINT program strings.
// These lines are produced by the eFLINT server's pretty printer (ppTypeExt in Print.hs)
// but are not valid eFLINT syntax and cause the parser to fail when importing.
func stripTypeExtensionLines(program string) string {
	lines := strings.Split(program, "\n")
	var result []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "Type extension of ") {
			result = append(result, line)
		}
	}
	return strings.Join(result, "\n")
}

// Revert reverts the eFLINT server to its initial (empty) state by sending a revert command.
// The value 1 means "revert to node 1 in the execution graph" (the starting state).
// After reverting, it verifies the state is actually empty by checking that target_contents
// in the status response is an empty array.
func (sm *StateManager) Revert() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if !sm.instanceManager.IsRunning() {
		return ErrInstanceNotRunning
	}

	// Send revert command to go back to node 1 (initial empty state)
	type revertCmd struct {
		Command string `json:"command"`
		Value   int    `json:"value"`
	}

	cmd := revertCmd{
		Command: "revert",
		Value:   1,
	}

	cmdJSON, err := json.Marshal(cmd)
	if err != nil {
		return fmt.Errorf("failed to marshal revert command: %w", err)
	}

	response, err := sm.instanceManager.SendCommand(string(cmdJSON))
	if err != nil {
		return fmt.Errorf("failed to send revert command: %w", err)
	}

	// Verify the revert succeeded by checking the response
	var respObj map[string]interface{}
	if err := json.Unmarshal([]byte(response), &respObj); err != nil {
		return fmt.Errorf("failed to parse revert response: %w", err)
	}

	if respObj["response"] == "invalid command" {
		return fmt.Errorf("eFLINT rejected revert command: %v", respObj["message"])
	}

	sm.logger.Info("reverted eFLINT instance to initial state")
	return nil
}

// VerifyEmptyState checks whether the eFLINT server is in an empty state
// by querying its status and verifying that target_contents is an empty array.
func (sm *StateManager) VerifyEmptyState() (bool, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if !sm.instanceManager.IsRunning() {
		return false, ErrInstanceNotRunning
	}

	response, err := sm.instanceManager.GetEflintStatus()
	if err != nil {
		return false, fmt.Errorf("failed to get eFLINT status: %w", err)
	}

	var statusResp map[string]interface{}
	if err := json.Unmarshal([]byte(response), &statusResp); err != nil {
		return false, fmt.Errorf("failed to parse status response: %w", err)
	}

	// Check if target_contents is an empty array
	targetContents, ok := statusResp["target_contents"]
	if !ok {
		// If target_contents is not present, assume it might be empty
		sm.logger.Warn("target_contents field not found in status response")
		return false, nil
	}

	// Check if it's an array and if it's empty
	if arr, ok := targetContents.([]interface{}); ok {
		isEmpty := len(arr) == 0
		sm.logger.Debug("verified eFLINT state",
			zap.Bool("isEmpty", isEmpty),
			zap.Int("target_contents_length", len(arr)),
		)
		return isEmpty, nil
	}

	sm.logger.Warn("target_contents is not an array",
		zap.String("type", fmt.Sprintf("%T", targetContents)),
	)
	return false, nil
}

// SaveStateToFile saves the current state to a file
func (sm *StateManager) SaveStateToFile(filename string) (*api.EflintSavedState, error) {
	state, err := sm.ExportState()
	if err != nil {
		return nil, err
	}

	filePath := filepath.Join(sm.stateDir, filename+".json")

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal state: %w", err)
	}

	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return nil, fmt.Errorf("failed to write state file: %w", err)
	}

	sm.logger.Info("saved state to file",
		zap.String("file", filePath),
		zap.String("id", state.ID),
	)

	return state, nil
}

// LoadStateFromFile loads a state from a file and imports it
func (sm *StateManager) LoadStateFromFile(filename string) error {
	filePath := filepath.Join(sm.stateDir, filename+".json")

	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read state file: %w", err)
	}

	var state api.EflintSavedState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("failed to unmarshal state: %w", err)
	}

	sm.logger.Info("loading state from file",
		zap.String("file", filePath),
		zap.String("id", state.ID),
	)

	return sm.ImportState(&state)
}

// ListSavedStates lists all saved state files
func (sm *StateManager) ListSavedStates() ([]string, error) {
	if sm.stateDir == "" {
		return nil, fmt.Errorf("state directory not configured")
	}

	files, err := os.ReadDir(sm.stateDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read state directory: %w", err)
	}

	var states []string
	for _, file := range files {
		if !file.IsDir() && filepath.Ext(file.Name()) == ".json" {
			states = append(states, file.Name()[:len(file.Name())-5]) // Remove .json extension
		}
	}

	return states, nil
}

// DeleteSavedState deletes a saved state file
func (sm *StateManager) DeleteSavedState(filename string) error {
	filePath := filepath.Join(sm.stateDir, filename+".json")

	if err := os.Remove(filePath); err != nil {
		return fmt.Errorf("failed to delete state file: %w", err)
	}

	sm.logger.Info("deleted state file",
		zap.String("file", filePath),
	)

	return nil
}

// CreateCheckpoint creates a checkpoint of the current state that can be restored later
// This is useful for "what-if" scenarios where you want to test something and then rollback
func (sm *StateManager) CreateCheckpoint(name string) (*api.EflintSavedState, error) {
	return sm.SaveStateToFile("checkpoint-" + name)
}

// RestoreCheckpoint restores a previously created checkpoint
func (sm *StateManager) RestoreCheckpoint(name string) error {
	return sm.LoadStateFromFile("checkpoint-" + name)
}
