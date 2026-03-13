package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/danmartell-ventures/apexagent/internal/container"
	"github.com/danmartell-ventures/apexagent/internal/introspect"
	"github.com/danmartell-ventures/apexagent/pkg/version"
)

// HostInfo matches the shell script's "host" object.
type HostInfo struct {
	Load1m       string `json:"load_1m"`
	Load5m       string `json:"load_5m"`
	Load15m      string `json:"load_15m"`
	DiskPercent  string `json:"disk_percent"`
	CPUCores     int    `json:"cpu_cores"`
	UptimeSecs   int64  `json:"uptime_seconds"`
	MemTotalMB   int    `json:"mem_total_mb"`
	ImageVersion string `json:"image_version"`
	AgentVersion string `json:"agent_version"`
}

// ContainerStats matches the shell script's per-container object.
type ContainerStats struct {
	State           string `json:"state"`
	CPUPercent      string `json:"cpu_percent"`
	MemUsage        string `json:"mem_usage"`
	MemLimit        string `json:"mem_limit"`
	MemPercent      string `json:"mem_percent"`
	NetIO           string `json:"net_io"`
	BlockIO         string `json:"block_io"`
	PIDs            string `json:"pids"`
	OpenClawVersion string `json:"openclaw_version"`

	// Introspection fields — gateway health from inside the container
	GatewayUp        *bool            `json:"gateway_up,omitempty"`
	GatewayLatencyMs *int             `json:"gateway_latency_ms,omitempty"`
	SessionCount     *int             `json:"session_count,omitempty"`
	HeartbeatSecs    *int             `json:"heartbeat_seconds,omitempty"`
	GatewayUptimeMs  *int64           `json:"gateway_uptime_ms,omitempty"`
	Channels         json.RawMessage  `json:"channels,omitempty"`
}

// Payload is the telemetry report sent to the mothership.
// Wire-compatible with the existing shell telemetry script.
type Payload struct {
	Token         string                    `json:"reporting_token"`
	ScriptVersion int                       `json:"script_version"`
	Host          HostInfo                  `json:"host"`
	Containers    map[string]ContainerStats `json:"containers"`
}

// Collect gathers system and container metrics, merging introspection data when available.
func Collect(ctx context.Context, token string, containers []container.ContainerStatus, health map[string]*introspect.ContainerHealth) Payload {
	host := collectHostInfo()

	cMap := make(map[string]ContainerStats)
	for _, c := range containers {
		state := "exited"
		if c.Running {
			state = "running"
		}
		stats := ContainerStats{
			State:      state,
			CPUPercent: fmt.Sprintf("%.2f%%", c.CPU),
			MemUsage:   fmt.Sprintf("%.0fMiB", c.MemMB),
			MemLimit:   "3GiB",
			MemPercent: fmt.Sprintf("%.1f%%", c.MemMB/3072*100),
			PIDs:       "0",
		}

		// Merge introspection data if available
		if h, ok := health[c.Name]; ok {
			if h.Liveness != nil {
				stats.GatewayUp = &h.Liveness.Up
				stats.GatewayLatencyMs = &h.Liveness.LatencyMs
			}
			if h.HealthDetail != nil {
				stats.SessionCount = &h.HealthDetail.SessionCount
				stats.HeartbeatSecs = &h.HealthDetail.HeartbeatSecs
				stats.GatewayUptimeMs = &h.HealthDetail.UptimeMs
				if h.HealthDetail.Channels != nil {
					channelBytes, _ := json.Marshal(h.HealthDetail.Channels)
					stats.Channels = channelBytes
				}
			}
		}

		cMap[c.Name] = stats
	}

	return Payload{
		Token:         token,
		ScriptVersion: 100,
		Host:          host,
		Containers:    cMap,
	}
}

func collectHostInfo() HostInfo {
	info := HostInfo{
		CPUCores:     runtime.NumCPU(),
		AgentVersion: version.Version,
	}

	// Load average (macOS: sysctl vm.loadavg returns "{ 1.23 4.56 7.89 }")
	if out, err := execCmd("sysctl", "-n", "vm.loadavg"); err == nil {
		s := strings.Trim(strings.TrimSpace(out), "{ }")
		parts := strings.Fields(s)
		if len(parts) >= 3 {
			info.Load1m = parts[0]
			info.Load5m = parts[1]
			info.Load15m = parts[2]
		}
	}

	// Disk percent
	if out, err := execCmd("df", "-h", "/"); err == nil {
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) >= 2 {
			fields := strings.Fields(lines[1])
			if len(fields) >= 5 {
				info.DiskPercent = fields[4] // e.g. "45%"
			}
		}
	}

	// Uptime seconds (macOS: sysctl kern.boottime)
	if out, err := execCmd("sysctl", "-n", "kern.boottime"); err == nil {
		// Format: "{ sec = 1710000000, usec = 0 } ..."
		var bootSec int64
		fmt.Sscanf(out, "{ sec = %d", &bootSec)
		if bootSec > 0 {
			info.UptimeSecs = time.Now().Unix() - bootSec
		}
	}

	// Memory
	if out, err := execCmd("sysctl", "-n", "hw.memsize"); err == nil {
		var bytes int64
		fmt.Sscanf(strings.TrimSpace(out), "%d", &bytes)
		info.MemTotalMB = int(bytes / 1024 / 1024)
	}

	// Docker image version
	if out, err := execCmd("docker", "image", "inspect", "openclaw:latest",
		"--format", `{{index .Config.Labels "apex.image_version"}}`); err == nil {
		info.ImageVersion = strings.TrimSpace(out)
	}

	return info
}

func execCmd(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	return string(out), err
}
