package eflint

import (
	"os/exec"
	"sync"
)

// -----------------------------------------------------------------------------
// Instance
// -----------------------------------------------------------------------------

// Instance represents a running eFLINT server process.
// It encapsulates the process handle and metadata about the instance.
// Thread-safe for concurrent access.
type Instance struct {
	Port          int       // TCP port the server is listening on
	Process       *exec.Cmd // Handle to the running process
	ModelLocation string    // Path to the eFLINT model file

	mu sync.RWMutex // Protects concurrent access to instance fields
}

// NewInstance creates a new Instance with the given parameters.
func NewInstance(port int, process *exec.Cmd, modelLocation string) *Instance {
	return &Instance{
		Port:          port,
		Process:       process,
		ModelLocation: modelLocation,
	}
}

// IsAlive checks if the eFLINT server process is still running.
// Returns true if the process exists and has not exited.
func (i *Instance) IsAlive() bool {
	i.mu.RLock()
	defer i.mu.RUnlock()

	if i.Process == nil {
		return false
	}

	// Process.ProcessState is nil until the process exits
	// So if ProcessState is nil, the process is still running
	return i.Process.ProcessState == nil
}

// Kill terminates the eFLINT server process.
// Returns nil if the process was successfully killed or was already terminated.
func (i *Instance) Kill() error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if i.Process == nil || i.Process.Process == nil {
		return nil
	}

	return i.Process.Process.Kill()
}

// GetPort returns the TCP port the instance is listening on.
func (i *Instance) GetPort() int {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.Port
}

// GetModelLocation returns the path to the eFLINT model file.
func (i *Instance) GetModelLocation() string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.ModelLocation
}
