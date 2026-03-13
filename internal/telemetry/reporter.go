package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/danmartell-ventures/apexagent/internal/config"
	"github.com/danmartell-ventures/apexagent/internal/container"
	"github.com/danmartell-ventures/apexagent/internal/introspect"
)

const reportInterval = 15 * time.Second

// removedThreshold is the number of consecutive 401s before we consider
// this host removed from the fleet. At 15s intervals, 3 = 45s.
const removedThreshold = 3

// Reporter sends telemetry to the mothership.
type Reporter struct {
	cfg     config.ServerConfig
	monitor *container.Monitor
	checker *introspect.Checker
	log     *slog.Logger
	client  *http.Client

	onRemoved      func()
	consecutive401 int
}

func NewReporter(cfg config.ServerConfig, monitor *container.Monitor, checker *introspect.Checker, log *slog.Logger) *Reporter {
	return &Reporter{
		cfg:     cfg,
		monitor: monitor,
		checker: checker,
		log:     log.With("component", "telemetry"),
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// OnRemoved registers a callback invoked when the server confirms this host
// has been removed from the fleet (consecutive 401 responses).
func (r *Reporter) OnRemoved(fn func()) {
	r.onRemoved = fn
}

// Run starts the telemetry loop.
func (r *Reporter) Run(ctx context.Context) error {
	ticker := time.NewTicker(reportInterval)
	defer ticker.Stop()

	// Initial report
	r.report(ctx)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			r.report(ctx)
		}
	}
}

func (r *Reporter) report(ctx context.Context) {
	containers := r.monitor.Containers()

	// Run introspection checks (concurrent, with per-container timeouts)
	r.checker.CheckAll(ctx, containers)
	health := r.checker.Results()

	payload := Collect(ctx, r.cfg.ReportingToken, containers, health)

	data, err := json.Marshal(payload)
	if err != nil {
		r.log.Error("failed to marshal telemetry", "error", err)
		return
	}

	url := fmt.Sprintf("%s/api/docker-hosts/telemetry", r.cfg.URL)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	if err != nil {
		r.log.Error("failed to create request", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		r.log.Debug("telemetry report failed", "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		r.consecutive401++
		r.log.Warn("telemetry rejected: host not recognized", "status", resp.StatusCode, "consecutive", r.consecutive401)
		if r.consecutive401 >= removedThreshold && r.onRemoved != nil {
			r.log.Error("host appears to have been removed from the fleet")
			r.onRemoved()
			r.onRemoved = nil // fire only once
		}
		return
	}

	// Any successful or other response resets the counter
	r.consecutive401 = 0

	if resp.StatusCode != http.StatusOK {
		r.log.Warn("telemetry report rejected", "status", resp.StatusCode)
	}
}
