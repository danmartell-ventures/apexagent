package agent

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/danmartell-ventures/apex-agent/internal/backup"
	"github.com/danmartell-ventures/apex-agent/internal/config"
	"github.com/danmartell-ventures/apex-agent/internal/container"
	"github.com/danmartell-ventures/apex-agent/internal/menubar"
	"github.com/danmartell-ventures/apex-agent/internal/platform"
	"github.com/danmartell-ventures/apex-agent/internal/telemetry"
	"github.com/danmartell-ventures/apex-agent/internal/tunnel"
	"github.com/danmartell-ventures/apex-agent/internal/update"
)

// Agent is the top-level orchestrator.
type Agent struct {
	cfg      *config.Config
	log      *slog.Logger
	headless bool

	tunnel    *tunnel.Manager
	monitor   *container.Monitor
	reporter  *telemetry.Reporter
	backup    *backup.Agent
	updater   *update.Updater
	menuApp   *menubar.App
	power     *platform.PowerMonitor
	network   *platform.NetworkMonitor

	cancel  context.CancelFunc
	wg      sync.WaitGroup
	removed sync.Once
	isRemoved bool
}

// New creates an Agent from config.
func New(cfg *config.Config, log *slog.Logger, headless bool) *Agent {
	tunnelMgr := tunnel.NewManager(cfg.Tunnel, cfg.Server.HostID, log)
	monitor := container.NewMonitor(cfg.Docker.ContainerPrefix, log)
	reporter := telemetry.NewReporter(cfg.Server, monitor, log)
	backupAgent := backup.NewAgent(*cfg, log)
	updater := update.NewUpdater(cfg.Agent.AutoUpdate, log)

	a := &Agent{
		cfg:      cfg,
		log:      log,
		headless: headless,
		tunnel:   tunnelMgr,
		monitor:  monitor,
		reporter: reporter,
		backup:   backupAgent,
		updater:  updater,
	}

	if !headless {
		a.power = platform.NewPowerMonitor(log)
		a.network = platform.NewNetworkMonitor(log)
		a.menuApp = menubar.NewApp(a, updater, menubar.Actions{
			RestartTunnel: func() { tunnelMgr.Reconnect() },
			CheckUpdate: func() {
				go func() {
					_, _, err := updater.CheckNow(context.Background())
					if err != nil {
						log.Error("manual update check failed", "error", err)
					}
				}()
			},
			ApplyUpdate: func() {
				_, _, err := updater.ApplyUpdate(context.Background())
				if err != nil {
					log.Error("update install failed", "error", err)
				}
			},
			OpenDashboard: func() {
				exec.Command("open", cfg.Server.URL).Start()
			},
			ViewLogs: func() {
				logPath := cfg.Agent.LogDir + "/agent.log"
				exec.Command("open", "-a", "Console", logPath).Start()
			},
			Reregister: func() {
				// Launch setup in a new process (it shows a native dialog),
				// then restart the service to pick up the new config.
				self, _ := os.Executable()
				setupCmd := exec.Command(self, "setup")
				if err := setupCmd.Start(); err != nil {
					log.Error("failed to launch setup", "error", err)
					return
				}
				setupCmd.Wait()
				platform.RestartService()
			},
			Quit: func() { a.Stop() },
		}, log)
	}

	// Wire up fleet removal detection
	reporter.OnRemoved(a.handleRemoved)

	return a
}

// handleRemoved is called when the server confirms this host has been removed.
func (a *Agent) handleRemoved() {
	a.removed.Do(func() {
		a.log.Error("this host has been removed from the fleet — shutting down subsystems")
		a.isRemoved = true
		// Cancel all subsystems (tunnel, backup, etc.) but keep menubar alive
		// so the user can see the removal notice.
		if a.cancel != nil {
			a.cancel()
		}
	})
}

// IsRemoved returns true if the server has confirmed this host was removed.
func (a *Agent) IsRemoved() bool {
	return a.isRemoved
}

// Run starts all subsystems. If not headless, the menubar runs on the main thread.
func (a *Agent) Run() error {
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel

	// Load forwards before starting tunnel
	if err := a.tunnel.LoadForwards(config.DefaultForwardsPath()); err != nil {
		a.log.Warn("failed to load forwards", "error", err)
	}

	// Start subsystems in goroutines
	a.startSubsystem(ctx, "tunnel", func(ctx context.Context) error {
		return a.tunnel.Run(ctx)
	})
	a.startSubsystem(ctx, "monitor", func(ctx context.Context) error {
		return a.monitor.Run(ctx)
	})
	a.startSubsystem(ctx, "telemetry", func(ctx context.Context) error {
		return a.reporter.Run(ctx)
	})
	a.startSubsystem(ctx, "backup", func(ctx context.Context) error {
		return a.backup.Run(ctx)
	})
	a.startSubsystem(ctx, "updater", func(ctx context.Context) error {
		return a.updater.Run(ctx)
	})

	// Platform event handlers
	if a.power != nil {
		a.startSubsystem(ctx, "power", func(ctx context.Context) error {
			return a.power.Run(ctx)
		})
		a.startSubsystem(ctx, "power-handler", func(ctx context.Context) error {
			return a.handlePowerEvents(ctx)
		})
	}
	if a.network != nil {
		a.startSubsystem(ctx, "network", func(ctx context.Context) error {
			return a.network.Run(ctx)
		})
		a.startSubsystem(ctx, "network-handler", func(ctx context.Context) error {
			return a.handleNetworkEvents(ctx)
		})
	}

	a.log.Info("agent started", "host_id", a.cfg.Server.HostID)

	// Menubar must run on main thread (or block if headless)
	if a.menuApp != nil {
		a.menuApp.Run() // Blocks until quit
	} else {
		// Headless: wait for context cancel
		<-ctx.Done()
		// If removed from fleet, stay alive so launchd doesn't restart us
		// in a crash loop. Just idle until SIGTERM.
		if a.isRemoved {
			a.log.Info("removed from fleet, idling until manually stopped")
			select {}
		}
	}

	a.wg.Wait()
	return nil
}

// Stop gracefully shuts down all subsystems.
func (a *Agent) Stop() {
	if a.cancel != nil {
		a.cancel()
	}
	if a.menuApp != nil {
		a.menuApp.Quit()
	}
}

func (a *Agent) startSubsystem(ctx context.Context, name string, fn func(context.Context) error) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		if err := fn(ctx); err != nil && ctx.Err() == nil {
			a.log.Error("subsystem failed", "name", name, "error", err)
		}
	}()
}

func (a *Agent) handlePowerEvents(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event := <-a.power.Events:
			switch event {
			case platform.PowerWake:
				a.log.Info("system woke from sleep, reconnecting tunnel")
				// Brief delay for network to come up
				time.Sleep(2 * time.Second)
				a.tunnel.Reconnect()
			case platform.PowerSleep:
				a.log.Info("system going to sleep")
			}
		}
	}
}

func (a *Agent) handleNetworkEvents(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-a.network.Events:
			a.log.Info("network change detected, reconnecting tunnel")
			time.Sleep(time.Second) // Brief delay for interface to stabilize
			a.tunnel.Reconnect()
		}
	}
}

// StatusProvider interface for menubar
func (a *Agent) TunnelState() tunnel.State       { return a.tunnel.State() }
func (a *Agent) TunnelConnectedAt() time.Time     { return a.tunnel.ConnectedAt() }
func (a *Agent) Containers() []container.ContainerStatus { return a.monitor.Containers() }

// Status returns a summary of agent state.
type Status struct {
	TunnelState    string              `json:"tunnel_state"`
	TunnelUptime   string              `json:"tunnel_uptime,omitempty"`
	Containers     []container.ContainerStatus `json:"containers"`
	Forwards       int                 `json:"active_forwards"`
}

func (a *Agent) Status() Status {
	s := Status{
		TunnelState: a.tunnel.State().String(),
		Containers:  a.monitor.Containers(),
		Forwards:    len(a.tunnel.ActiveForwards()),
	}
	if a.tunnel.State() == tunnel.StateConnected {
		s.TunnelUptime = time.Since(a.tunnel.ConnectedAt()).Truncate(time.Second).String()
	}
	return s
}

// RunDiagnostics runs doctor checks.
func (a *Agent) RunDiagnostics(ctx context.Context) ([]container.DiagnosticResult, error) {
	return a.monitor.RunDiagnostics(ctx)
}

// RestartContainer restarts a specific container by name.
func (a *Agent) RestartContainer(ctx context.Context, name string) error {
	return a.monitor.RestartContainer(ctx, name)
}
