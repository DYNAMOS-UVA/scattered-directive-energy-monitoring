package eflint

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// -----------------------------------------------------------------------------
// Pool Configuration
// -----------------------------------------------------------------------------

// PoolConfig holds configuration for the InstancePool.
type PoolConfig struct {
	TargetSize          int           // Desired number of healthy instances
	ManagerConfig       *ManagerConfig // Configuration for each Manager instance
	EmptyModelPath      string        // Path to the empty.eflint model for bootstrapping
	HealthCheckInterval time.Duration // How often the health monitor runs
	AcquireTimeout      time.Duration // Max time to wait when acquiring an instance
}

// DefaultPoolConfig returns sensible default pool configuration values.
func DefaultPoolConfig() *PoolConfig {
	return &PoolConfig{
		TargetSize:          3,
		ManagerConfig:       DefaultManagerConfig(),
		EmptyModelPath:      "",
		HealthCheckInterval: 10 * time.Second,
		AcquireTimeout:      30 * time.Second,
	}
}

// -----------------------------------------------------------------------------
// Pool Entry
// -----------------------------------------------------------------------------

// InstanceState represents the current state of a pool entry.
type InstanceState string

const (
	InstanceStateIdle      InstanceState = "idle"
	InstanceStateInUse     InstanceState = "in_use"
	InstanceStateUnhealthy InstanceState = "unhealthy"
)

// PoolEntry wraps a Manager with pool metadata.
type PoolEntry struct {
	ID           string        // Stable identifier (e.g., "instance-0")
	Manager      *Manager      // The underlying eFLINT server manager
	StateManager *StateManager // State manager for revert/verify operations
	State        InstanceState // Current pool state
	mu           sync.RWMutex  // Protects State field
}

// GetState returns the current state of the pool entry (thread-safe).
func (pe *PoolEntry) GetState() InstanceState {
	pe.mu.RLock()
	defer pe.mu.RUnlock()
	return pe.State
}

// SetState sets the state of the pool entry (thread-safe).
func (pe *PoolEntry) SetState(state InstanceState) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	pe.State = state
}

// -----------------------------------------------------------------------------
// Instance Info (for API responses)
// -----------------------------------------------------------------------------

// InstanceInfo represents the public information about a pool instance.
type InstanceInfo struct {
	ID      string        `json:"id"`
	Status  InstanceState `json:"status"`
	Running bool          `json:"running"`
	Port    int           `json:"port,omitempty"`
}

// -----------------------------------------------------------------------------
// Instance Pool
// -----------------------------------------------------------------------------

// InstancePool manages a pool of pre-started eFLINT server instances.
// It provides Acquire/Release semantics for stateless validation,
// automatic health monitoring, and dynamic resizing.
type InstancePool struct {
	config     *PoolConfig
	logger     *zap.Logger
	available  chan *PoolEntry      // Channel of idle entries ready to be acquired
	registry   []*PoolEntry        // All entries (for ID-based lookup and health checks)
	registryMu sync.RWMutex        // Protects registry slice
	targetSize int32               // Atomic target size for dynamic resizing
	nextID     int32               // Atomic counter for generating unique IDs
	cancelFunc context.CancelFunc  // Cancel function for health monitor
	stateDir   string              // Directory for state files
}

// NewInstancePool creates and initializes a pool of eFLINT server instances.
// It starts config.TargetSize instances, each booted with the empty model.
func NewInstancePool(config *PoolConfig, stateDir string, logger *zap.Logger) (*InstancePool, error) {
	if config == nil {
		config = DefaultPoolConfig()
	}

	if config.EmptyModelPath == "" {
		return nil, fmt.Errorf("EmptyModelPath is required in PoolConfig")
	}

	pool := &InstancePool{
		config:    config,
		logger:    logger,
		available: make(chan *PoolEntry, config.TargetSize*2), // Buffer for some headroom
		registry:  make([]*PoolEntry, 0, config.TargetSize),
		stateDir:  stateDir,
	}
	atomic.StoreInt32(&pool.targetSize, int32(config.TargetSize))

	// Start the initial instances in parallel
	var wg sync.WaitGroup
	results := make(chan *PoolEntry, config.TargetSize)

	for i := 0; i < config.TargetSize; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()

			entry, err := pool.createInstance()
			if err != nil {
				logger.Error("failed to create pool instance during initialization",
					zap.Int("index", index),
					zap.Error(err),
				)
				// Continue starting remaining instances
				return
			}

			results <- entry
		}(i)
	}

	wg.Wait()
	close(results)

	for entry := range results {
		pool.registryMu.Lock()
		pool.registry = append(pool.registry, entry)
		pool.registryMu.Unlock()

		pool.available <- entry
	}

	// Start the health monitor
	ctx, cancel := context.WithCancel(context.Background())
	pool.cancelFunc = cancel
	go pool.healthMonitor(ctx)

	logger.Info("instance pool initialized",
		zap.Int("target_size", config.TargetSize),
		zap.Int("started", len(pool.registry)),
	)

	return pool, nil
}

// createInstance creates a new eFLINT Manager instance with the empty model.
func (p *InstancePool) createInstance() (*PoolEntry, error) {
	id := fmt.Sprintf("instance-%d", atomic.AddInt32(&p.nextID, 1)-1)

	manager := NewManager(p.config.ManagerConfig, p.logger.With(zap.String("instance_id", id)))

	if err := manager.Start(p.config.EmptyModelPath); err != nil {
		return nil, fmt.Errorf("failed to start instance %s: %w", id, err)
	}

	stateManager := NewStateManager(manager, p.stateDir, p.logger.With(zap.String("instance_id", id)))

	entry := &PoolEntry{
		ID:           id,
		Manager:      manager,
		StateManager: stateManager,
		State:        InstanceStateIdle,
	}

	p.logger.Info("created pool instance",
		zap.String("id", id),
		zap.Int("port", manager.Status().Port),
	)

	return entry, nil
}

// -----------------------------------------------------------------------------
// Acquire / Release
// -----------------------------------------------------------------------------

// Acquire returns an idle instance from the pool, marking it as in_use.
// Blocks until an instance is available or the AcquireTimeout is reached.
func (p *InstancePool) Acquire() (*PoolEntry, error) {
	timer := time.NewTimer(p.config.AcquireTimeout)
	defer timer.Stop()

	select {
	case entry := <-p.available:
		entry.SetState(InstanceStateInUse)
		p.logger.Debug("acquired pool instance",
			zap.String("id", entry.ID),
		)
		return entry, nil
	case <-timer.C:
		return nil, fmt.Errorf("timed out waiting for available instance (timeout: %s)", p.config.AcquireTimeout)
	}
}

// Release restarts an instance with the empty model and returns it to the pool.
// If the restart fails, the instance is marked unhealthy and will be replaced
// by the health monitor. This method is designed to be called asynchronously
// via a goroutine.
func (p *InstancePool) Release(entry *PoolEntry) {
	if entry == nil {
		return
	}

	// Restart the underlying eFLINT server process with the empty model to
	// provide process-level isolation between uses.
	if err := entry.Manager.Start(p.config.EmptyModelPath); err != nil {
		p.logger.Error("failed to restart instance, marking as unhealthy",
			zap.String("id", entry.ID),
			zap.Error(err),
		)
		entry.SetState(InstanceStateUnhealthy)
		return
	}

	// Instance has a fresh process, return to pool as idle
	entry.SetState(InstanceStateIdle)

	// Non-blocking send to available channel; if full, the health monitor will pick it up
	select {
	case p.available <- entry:
		p.logger.Debug("released pool instance back to pool",
			zap.String("id", entry.ID),
		)
	default:
		p.logger.Warn("available channel full, instance will be picked up by health monitor",
			zap.String("id", entry.ID),
		)
	}
}

// -----------------------------------------------------------------------------
// Lookup and Listing
// -----------------------------------------------------------------------------

// GetByID returns a specific pool entry by its ID.
// This is for HTTP API use and does NOT change the entry's status.
func (p *InstancePool) GetByID(id string) (*PoolEntry, error) {
	p.registryMu.RLock()
	defer p.registryMu.RUnlock()

	for _, entry := range p.registry {
		if entry.ID == id {
			return entry, nil
		}
	}
	return nil, fmt.Errorf("instance %q not found in pool", id)
}

// ListInstances returns information about all instances in the pool.
func (p *InstancePool) ListInstances() []InstanceInfo {
	p.registryMu.RLock()
	defer p.registryMu.RUnlock()

	infos := make([]InstanceInfo, 0, len(p.registry))
	for _, entry := range p.registry {
		status := entry.Manager.Status()
		infos = append(infos, InstanceInfo{
			ID:      entry.ID,
			Status:  entry.GetState(),
			Running: status.Running,
			Port:    status.Port,
		})
	}
	return infos
}

// PoolStats returns summary statistics about the pool.
func (p *InstancePool) PoolStats() (total, idle, inUse, unhealthy int) {
	p.registryMu.RLock()
	defer p.registryMu.RUnlock()

	total = len(p.registry)
	for _, entry := range p.registry {
		switch entry.GetState() {
		case InstanceStateIdle:
			idle++
		case InstanceStateInUse:
			inUse++
		case InstanceStateUnhealthy:
			unhealthy++
		}
	}
	return
}

// GetTargetSize returns the current target pool size.
func (p *InstancePool) GetTargetSize() int {
	return int(atomic.LoadInt32(&p.targetSize))
}

// -----------------------------------------------------------------------------
// Resize
// -----------------------------------------------------------------------------

// Resize adjusts the target pool size at runtime.
// New instances are spun up by the health monitor; excess idle instances are stopped.
func (p *InstancePool) Resize(newTargetSize int) error {
	if newTargetSize < 1 {
		return fmt.Errorf("target size must be at least 1, got %d", newTargetSize)
	}

	old := atomic.SwapInt32(&p.targetSize, int32(newTargetSize))

	p.logger.Info("pool target size updated",
		zap.Int("old_target", int(old)),
		zap.Int("new_target", newTargetSize),
	)

	return nil
}

// -----------------------------------------------------------------------------
// Health Monitor
// -----------------------------------------------------------------------------

// healthMonitor runs in the background, checking instance health and enforcing target size.
func (p *InstancePool) healthMonitor(ctx context.Context) {
	ticker := time.NewTicker(p.config.HealthCheckInterval)
	defer ticker.Stop()

	p.logger.Info("health monitor started",
		zap.Duration("interval", p.config.HealthCheckInterval),
	)

	for {
		select {
		case <-ctx.Done():
			p.logger.Info("health monitor stopped")
			return
		case <-ticker.C:
			p.runHealthCheck()
		}
	}
}

// runHealthCheck performs a single health check cycle.
func (p *InstancePool) runHealthCheck() {
	p.registryMu.Lock()
	defer p.registryMu.Unlock()

	target := int(atomic.LoadInt32(&p.targetSize))
	healthyCount := 0
	var unhealthyEntries []*PoolEntry

	// 1. Check all instances for health
	for _, entry := range p.registry {
		state := entry.GetState()

		// Check if the process is still alive for non-unhealthy instances
		if state != InstanceStateUnhealthy && !entry.Manager.IsRunning() {
			p.logger.Warn("instance process died, marking as unhealthy",
				zap.String("id", entry.ID),
			)
			entry.SetState(InstanceStateUnhealthy)
			state = InstanceStateUnhealthy
		}

		if state == InstanceStateUnhealthy {
			unhealthyEntries = append(unhealthyEntries, entry)
		} else {
			healthyCount++
		}
	}

	// 2. Replace unhealthy instances
	for _, unhealthy := range unhealthyEntries {
		// Only replace if we're below target
		if healthyCount >= target {
			break
		}

		p.logger.Info("replacing unhealthy instance",
			zap.String("id", unhealthy.ID),
		)

		// Kill the old process if still lingering
		if unhealthy.Manager.IsRunning() {
			_ = unhealthy.Manager.Stop()
		}

		// Start a fresh instance
		if err := unhealthy.Manager.Start(p.config.EmptyModelPath); err != nil {
			p.logger.Error("failed to restart unhealthy instance",
				zap.String("id", unhealthy.ID),
				zap.Error(err),
			)
			continue
		}

		unhealthy.SetState(InstanceStateIdle)
		healthyCount++

		// Return to available pool
		select {
		case p.available <- unhealthy:
			p.logger.Info("replaced and returned instance to pool",
				zap.String("id", unhealthy.ID),
			)
		default:
			p.logger.Warn("available channel full after replacing instance",
				zap.String("id", unhealthy.ID),
			)
		}
	}

	// 3. Enforce target size - spin up new instances if needed
	if healthyCount < target {
		needed := target - healthyCount

		var wg sync.WaitGroup
		results := make(chan *PoolEntry, needed)

		for i := 0; i < needed; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()

				entry, err := p.createInstance()
				if err != nil {
					p.logger.Error("failed to create new instance for target enforcement",
						zap.Error(err),
					)
					return
				}

				results <- entry
			}()
		}

		wg.Wait()
		close(results)

		for entry := range results {
			p.registry = append(p.registry, entry)
			healthyCount++

			select {
			case p.available <- entry:
			default:
				p.logger.Warn("available channel full after creating new instance",
					zap.String("id", entry.ID),
				)
			}
		}
	}

	// 4. Shrink if above target - remove excess idle instances
	if healthyCount > target {
		excess := healthyCount - target
		removed := 0
		newRegistry := make([]*PoolEntry, 0, len(p.registry))

		for _, entry := range p.registry {
			if removed < excess && entry.GetState() == InstanceStateIdle {
				// Try to drain from available channel
				drained := false
				select {
				case drained_entry := <-p.available:
					if drained_entry.ID == entry.ID {
						drained = true
					} else {
						// Put it back, it's not the one we want
						p.available <- drained_entry
					}
				default:
					// Not in channel, might still be idle but in use elsewhere
				}

				if drained || entry.GetState() == InstanceStateIdle {
					p.logger.Info("stopping excess instance",
						zap.String("id", entry.ID),
					)
					_ = entry.Manager.Stop()
					entry.SetState(InstanceStateUnhealthy)
					removed++
					continue // Don't add to new registry
				}
			}
			newRegistry = append(newRegistry, entry)
		}

		if removed > 0 {
			p.registry = newRegistry
		}
	}

	// 5. Log pool health
	total, idle, inUse, unhealthyCount := 0, 0, 0, 0
	for _, entry := range p.registry {
		total++
		switch entry.GetState() {
		case InstanceStateIdle:
			idle++
		case InstanceStateInUse:
			inUse++
		case InstanceStateUnhealthy:
			unhealthyCount++
		}
	}

	p.logger.Debug("pool health check completed",
		zap.Int("target", target),
		zap.Int("total", total),
		zap.Int("idle", idle),
		zap.Int("in_use", inUse),
		zap.Int("unhealthy", unhealthyCount),
	)
}

// -----------------------------------------------------------------------------
// Shutdown
// -----------------------------------------------------------------------------

// Shutdown stops all instances and the health monitor.
func (p *InstancePool) Shutdown() {
	// Stop the health monitor
	if p.cancelFunc != nil {
		p.cancelFunc()
	}

	p.registryMu.Lock()
	defer p.registryMu.Unlock()

	for _, entry := range p.registry {
		if entry.Manager.IsRunning() {
			if err := entry.Manager.Stop(); err != nil {
				p.logger.Error("failed to stop instance during shutdown",
					zap.String("id", entry.ID),
					zap.Error(err),
				)
			}
		}
	}

	// Drain the available channel
	close(p.available)

	p.logger.Info("instance pool shut down",
		zap.Int("instances_stopped", len(p.registry)),
	)
}
