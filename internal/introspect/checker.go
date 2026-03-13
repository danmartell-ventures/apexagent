package introspect

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/danmartell-ventures/apexagent/internal/container"
)

const (
	deepInterval    = 60 * time.Second
	perCheckTimeout = 3 * time.Second
)

// ContainerEndpoint holds resolved connection info for a container.
type ContainerEndpoint struct {
	Name  string
	Host  string
	Port  int
	Token string
}

// Checker performs periodic health introspection of containers.
type Checker struct {
	docker *container.Docker
	log    *slog.Logger

	mu       sync.RWMutex
	results  map[string]*ContainerHealth
	lastDeep time.Time

	// Cache port/token per container (these don't change during container lifetime)
	epMu      sync.RWMutex
	epCache   map[string]*ContainerEndpoint
}

func NewChecker(docker *container.Docker, log *slog.Logger) *Checker {
	return &Checker{
		docker:  docker,
		log:     log.With("component", "introspect"),
		results: make(map[string]*ContainerHealth),
		epCache: make(map[string]*ContainerEndpoint),
	}
}

// Results returns the latest health results for all containers.
func (c *Checker) Results() map[string]*ContainerHealth {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]*ContainerHealth, len(c.results))
	for k, v := range c.results {
		cp := *v
		out[k] = &cp
	}
	return out
}

// CheckAll runs liveness on all running containers, and deep health every 60s.
// Called from the telemetry reporter before each report.
func (c *Checker) CheckAll(ctx context.Context, containers []container.ContainerStatus) {
	doDeep := time.Since(c.lastDeep) >= deepInterval

	endpoints := c.resolveEndpoints(ctx, containers)
	if len(endpoints) == 0 {
		c.mu.Lock()
		c.results = make(map[string]*ContainerHealth)
		c.mu.Unlock()
		return
	}

	var wg sync.WaitGroup
	results := make(map[string]*ContainerHealth, len(endpoints))
	var mu sync.Mutex

	for _, ep := range endpoints {
		wg.Add(1)
		go func(ep ContainerEndpoint) {
			defer wg.Done()
			checkCtx, cancel := context.WithTimeout(ctx, perCheckTimeout)
			defer cancel()

			health := &ContainerHealth{}

			// Always do liveness
			liveness := CheckLiveness(checkCtx, ep.Host, ep.Port)
			health.Liveness = &liveness

			// Deep health every 60s, only if gateway is up and token available
			if doDeep && liveness.Up && ep.Token != "" {
				detail, err := FetchHealth(checkCtx, ep.Host, ep.Port, ep.Token)
				if err != nil {
					c.log.Debug("deep health check failed", "container", ep.Name, "error", err)
				} else {
					health.HealthDetail = detail
				}
			} else if !doDeep {
				// Carry forward previous deep health result
				c.mu.RLock()
				if prev, ok := c.results[ep.Name]; ok && prev.HealthDetail != nil {
					health.HealthDetail = prev.HealthDetail
				}
				c.mu.RUnlock()
			}

			mu.Lock()
			results[ep.Name] = health
			mu.Unlock()
		}(ep)
	}

	wg.Wait()

	if doDeep {
		c.lastDeep = time.Now()
	}

	c.mu.Lock()
	c.results = results
	c.mu.Unlock()
}

// resolveEndpoints discovers gateway port and token for each running container.
// Results are cached per container name since port/token don't change during lifetime.
func (c *Checker) resolveEndpoints(ctx context.Context, containers []container.ContainerStatus) []ContainerEndpoint {
	// Prune cache for containers that no longer exist
	activeNames := make(map[string]bool, len(containers))
	for _, ct := range containers {
		activeNames[ct.Name] = true
	}
	c.epMu.Lock()
	for name := range c.epCache {
		if !activeNames[name] {
			delete(c.epCache, name)
		}
	}
	c.epMu.Unlock()

	var endpoints []ContainerEndpoint
	for _, ct := range containers {
		if !ct.Running {
			continue
		}

		// Check cache first
		c.epMu.RLock()
		cached, ok := c.epCache[ct.Name]
		c.epMu.RUnlock()
		if ok {
			endpoints = append(endpoints, *cached)
			continue
		}

		// Resolve port and token via docker inspect
		port, err := c.docker.InspectPort(ctx, ct.Name, 18789)
		if err != nil || port == 0 {
			c.log.Debug("no gateway port for container", "container", ct.Name, "error", err)
			continue
		}
		token, _ := c.docker.InspectEnv(ctx, ct.Name, "OPENCLAW_GATEWAY_TOKEN")

		ep := ContainerEndpoint{
			Name:  ct.Name,
			Host:  "127.0.0.1",
			Port:  port,
			Token: token,
		}

		// Cache it
		c.epMu.Lock()
		c.epCache[ct.Name] = &ep
		c.epMu.Unlock()

		endpoints = append(endpoints, ep)
	}
	return endpoints
}

// InvalidateCache clears the endpoint cache for a container (e.g., after token rotation).
func (c *Checker) InvalidateCache(name string) {
	c.epMu.Lock()
	delete(c.epCache, name)
	c.epMu.Unlock()
}
