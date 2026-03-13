package container

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Info represents a running container.
type Info struct {
	ID      string
	Name    string
	Status  string
	Running bool
	Health  string // healthy, unhealthy, starting, none
	CPU     float64
	MemMB   float64
}

// Docker wraps docker CLI commands.
type Docker struct {
	prefix string
}

func NewDocker(prefix string) *Docker {
	return &Docker{prefix: prefix}
}

// ListContainers returns containers matching the prefix.
func (d *Docker) ListContainers(ctx context.Context) ([]Info, error) {
	out, err := d.run(ctx, "ps", "-a", "--filter", "name=^"+d.prefix,
		"--format", `{"id":"{{.ID}}","name":"{{.Names}}","status":"{{.Status}}","state":"{{.State}}"}`)
	if err != nil {
		return nil, fmt.Errorf("docker ps: %w", err)
	}

	if strings.TrimSpace(out) == "" {
		return nil, nil
	}

	var containers []Info
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var raw struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			State string `json:"state"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		containers = append(containers, Info{
			ID:      raw.ID,
			Name:    raw.Name,
			Status:  raw.Status,
			Running: raw.State == "running",
		})
	}

	return containers, nil
}

// InspectHealth returns the health status of a container.
func (d *Docker) InspectHealth(ctx context.Context, name string) (string, error) {
	out, err := d.run(ctx, "inspect", "--format", "{{.State.Health.Status}}", name)
	if err != nil {
		// Container may not have a health check
		return "none", nil
	}
	return strings.TrimSpace(out), nil
}

// InspectLabel returns the value of a Docker label on a container.
func (d *Docker) InspectLabel(ctx context.Context, name, label string) string {
	out, err := d.run(ctx, "inspect", "--format", fmt.Sprintf("{{index .Config.Labels %q}}", label), name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// ContainerStats returns CPU and memory usage.
func (d *Docker) ContainerStats(ctx context.Context, name string) (cpu float64, memMB float64, err error) {
	out, err := d.run(ctx, "stats", "--no-stream", "--format",
		`{"cpu":"{{.CPUPerc}}","mem":"{{.MemUsage}}"}`, name)
	if err != nil {
		return 0, 0, err
	}

	var raw struct {
		CPU string `json:"cpu"`
		Mem string `json:"mem"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &raw); err != nil {
		return 0, 0, err
	}

	// Parse "12.34%"
	cpuStr := strings.TrimSuffix(raw.CPU, "%")
	fmt.Sscanf(cpuStr, "%f", &cpu)

	// Parse "1.2GiB / 3GiB" or "800MiB / 3GiB"
	parts := strings.Split(raw.Mem, "/")
	if len(parts) >= 1 {
		memMB = parseMemory(strings.TrimSpace(parts[0]))
	}

	return cpu, memMB, nil
}

// Exec runs a command inside a container.
func (d *Docker) Exec(ctx context.Context, name string, cmd ...string) (string, error) {
	args := append([]string{"exec", name}, cmd...)
	return d.run(ctx, args...)
}

// Restart restarts a container.
func (d *Docker) Restart(ctx context.Context, name string) error {
	_, err := d.run(ctx, "restart", name)
	return err
}

// StopAndStart does a stop followed by start.
func (d *Docker) StopAndStart(ctx context.Context, name string) error {
	if _, err := d.run(ctx, "stop", name); err != nil {
		return fmt.Errorf("stop: %w", err)
	}
	if _, err := d.run(ctx, "start", name); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	return nil
}

// InspectPort returns the host-side published port for a given container port.
func (d *Docker) InspectPort(ctx context.Context, name string, containerPort int) (int, error) {
	format := fmt.Sprintf(`{{(index (index .NetworkSettings.Ports "%d/tcp") 0).HostPort}}`, containerPort)
	out, err := d.run(ctx, "inspect", "--format", format, name)
	if err != nil {
		return 0, err
	}
	var port int
	fmt.Sscanf(strings.TrimSpace(out), "%d", &port)
	return port, nil
}

// InspectEnv reads a specific environment variable from a container's config.
func (d *Docker) InspectEnv(ctx context.Context, name, envVar string) (string, error) {
	out, err := d.run(ctx, "inspect", "--format", "{{range .Config.Env}}{{println .}}{{end}}", name)
	if err != nil {
		return "", err
	}
	prefix := envVar + "="
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix), nil
		}
	}
	return "", fmt.Errorf("env var %s not found", envVar)
}

// IsRunning checks if a container is running.
func (d *Docker) IsRunning(ctx context.Context, name string) (bool, error) {
	out, err := d.run(ctx, "inspect", "--format", "{{.State.Running}}", name)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "true", nil
}

// detectDockerHost finds the Docker socket, checking common locations.
func detectDockerHost() string {
	// Already set in environment
	if h := os.Getenv("DOCKER_HOST"); h != "" {
		return h
	}

	// Default Docker Desktop socket
	if _, err := os.Stat("/var/run/docker.sock"); err == nil {
		return ""
	}

	// Colima
	home, _ := os.UserHomeDir()
	colima := filepath.Join(home, ".colima", "default", "docker.sock")
	if _, err := os.Stat(colima); err == nil {
		return "unix://" + colima
	}

	// OrbStack
	orbstack := filepath.Join(home, ".orbstack", "run", "docker.sock")
	if _, err := os.Stat(orbstack); err == nil {
		return "unix://" + orbstack
	}

	return ""
}

func (d *Docker) run(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	dockerPath := findDocker()
	cmd := exec.CommandContext(ctx, dockerPath, args...)
	cmd.Env = os.Environ()
	if host := detectDockerHost(); host != "" {
		cmd.Env = append(cmd.Env, "DOCKER_HOST="+host)
	}
	// Auto-negotiate API version so minor client/engine skew doesn't break monitoring.
	// Setting DOCKER_CLI_HINTS=false suppresses upgrade nags; the empty check
	// avoids overriding an explicit user-set version.
	if os.Getenv("DOCKER_API_VERSION") == "" {
		cmd.Env = append(cmd.Env, "DOCKER_API_VERSION=1.44")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// findDocker returns the full path to the docker binary.
// Under launchd the PATH is minimal, so we check common locations.
func findDocker() string {
	if p, err := exec.LookPath("docker"); err == nil {
		return p
	}
	for _, p := range []string{
		"/usr/local/bin/docker",
		"/opt/homebrew/bin/docker",
		"/Applications/Docker.app/Contents/Resources/bin/docker",
		"/Applications/OrbStack.app/Contents/MacOS/xbin/docker",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "docker" // fall back, let it fail with a clear error
}

func parseMemory(s string) float64 {
	s = strings.TrimSpace(s)
	var val float64
	if strings.HasSuffix(s, "GiB") {
		fmt.Sscanf(strings.TrimSuffix(s, "GiB"), "%f", &val)
		return val * 1024
	}
	if strings.HasSuffix(s, "MiB") {
		fmt.Sscanf(strings.TrimSuffix(s, "MiB"), "%f", &val)
		return val
	}
	if strings.HasSuffix(s, "KiB") {
		fmt.Sscanf(strings.TrimSuffix(s, "KiB"), "%f", &val)
		return val / 1024
	}
	return 0
}
