package container

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const (
	pollInterval    = 10 * time.Second
	recoveryWait    = 8 * time.Second
	cooldownPeriod  = 5 * time.Minute
	maxRecoveryAttempts = 3
)

// ContainerStatus tracks status visible to external consumers.
type ContainerStatus struct {
	Name        string
	PersonaName string
	Running     bool
	CPU         float64
	MemMB       float64
	Health      string
}

// Monitor watches containers and performs auto-recovery.
type Monitor struct {
	docker *Docker
	prefix string
	log    *slog.Logger

	mu         sync.RWMutex
	containers []ContainerStatus
	cooldowns  map[string]time.Time
}

func NewMonitor(prefix string, log *slog.Logger) *Monitor {
	return &Monitor{
		docker:    NewDocker(prefix),
		prefix:    prefix,
		log:       log.With("component", "container"),
		cooldowns: make(map[string]time.Time),
	}
}

// Docker returns the underlying Docker wrapper for use by introspection.
func (m *Monitor) Docker() *Docker {
	return m.docker
}

// Containers returns the latest container statuses.
func (m *Monitor) Containers() []ContainerStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]ContainerStatus, len(m.containers))
	copy(result, m.containers)
	return result
}

// Run starts the monitoring loop.
func (m *Monitor) Run(ctx context.Context) error {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// Initial poll
	m.poll(ctx)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			m.poll(ctx)
		}
	}
}

func (m *Monitor) poll(ctx context.Context) {
	containers, err := m.docker.ListContainers(ctx)
	if err != nil {
		m.log.Error("failed to list containers", "error", err)
		return
	}

	var statuses []ContainerStatus
	for _, c := range containers {
		status := ContainerStatus{
			Name:        c.Name,
			PersonaName: m.docker.InspectLabel(ctx, c.Name, "apex.persona_name"),
			Running:     c.Running,
		}

		if c.Running {
			cpu, mem, err := m.docker.ContainerStats(ctx, c.Name)
			if err == nil {
				status.CPU = cpu
				status.MemMB = mem
			}
			health, _ := m.docker.InspectHealth(ctx, c.Name)
			status.Health = health
		} else {
			// Container not running — attempt recovery
			m.recover(ctx, c.Name)
		}

		statuses = append(statuses, status)
	}

	m.mu.Lock()
	m.containers = statuses
	m.mu.Unlock()
}

func (m *Monitor) recover(ctx context.Context, name string) {
	m.mu.RLock()
	cooldownUntil, inCooldown := m.cooldowns[name]
	m.mu.RUnlock()

	if inCooldown && time.Now().Before(cooldownUntil) {
		return
	}

	m.log.Warn("container not running, starting recovery", "container", name)

	for attempt := 1; attempt <= maxRecoveryAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return
		default:
		}

		m.log.Info("recovery attempt", "container", name, "attempt", attempt)

		switch attempt {
		case 1:
			// Try openclaw doctor --fix
			out, err := m.docker.Exec(ctx, name, "openclaw", "doctor", "--fix")
			if err != nil {
				m.log.Debug("doctor --fix failed", "container", name, "error", err, "output", out)
			}
		case 2:
			// Docker restart
			if err := m.docker.Restart(ctx, name); err != nil {
				m.log.Error("restart failed", "container", name, "error", err)
			}
		case 3:
			// Stop + start
			if err := m.docker.StopAndStart(ctx, name); err != nil {
				m.log.Error("stop+start failed", "container", name, "error", err)
			}
		}

		time.Sleep(recoveryWait)

		running, err := m.docker.IsRunning(ctx, name)
		if err == nil && running {
			m.log.Info("recovery succeeded", "container", name, "attempt", attempt)
			return
		}
	}

	// All attempts failed — enter cooldown
	m.log.Error("recovery failed after max attempts, entering cooldown",
		"container", name, "cooldown", cooldownPeriod)
	m.mu.Lock()
	m.cooldowns[name] = time.Now().Add(cooldownPeriod)
	m.mu.Unlock()
}

// RestartContainer manually restarts a specific container.
func (m *Monitor) RestartContainer(ctx context.Context, name string) error {
	return m.docker.Restart(ctx, name)
}
