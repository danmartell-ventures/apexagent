package menubar

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/energye/systray"

	"github.com/danmartell-ventures/apex-agent/internal/container"
	"github.com/danmartell-ventures/apex-agent/internal/tunnel"
	"github.com/danmartell-ventures/apex-agent/pkg/version"
)

// StatusProvider supplies current agent state to the menubar.
type StatusProvider interface {
	TunnelState() tunnel.State
	TunnelConnectedAt() time.Time
	Containers() []container.ContainerStatus
	IsRemoved() bool
}

// UpdateProvider supplies update state to the menubar.
type UpdateProvider interface {
	HasPendingUpdate() (newVersion string, available bool)
}

// Actions the menubar can trigger.
type Actions struct {
	RestartTunnel  func()
	CheckUpdate    func()
	ApplyUpdate    func()
	OpenDashboard  func()
	ViewLogs       func()
	Quit           func()
}

// App manages the macOS menubar icon.
type App struct {
	log      *slog.Logger
	status   StatusProvider
	updates  UpdateProvider
	actions  Actions
	done     chan struct{}
}

func NewApp(status StatusProvider, updates UpdateProvider, actions Actions, log *slog.Logger) *App {
	return &App{
		log:     log.With("component", "menubar"),
		status:  status,
		updates: updates,
		actions: actions,
		done:    make(chan struct{}),
	}
}

// Run starts the menubar. Must be called from the main thread.
func (a *App) Run() {
	systray.Run(a.onReady, a.onExit)
}

// Quit requests the menubar to exit.
func (a *App) Quit() {
	systray.Quit()
}

func (a *App) onReady() {
	systray.SetIcon(iconGreen)
	systray.SetTooltip("Apex Agent")

	mVersion := systray.AddMenuItem("Apex Agent "+version.Version, "")
	mVersion.Disable()

	systray.AddSeparator()

	mTunnel := systray.AddMenuItem("Tunnel: checking...", "")
	mTunnel.Disable()

	mContainers := systray.AddMenuItem("Containers: checking...", "")
	mContainers.Disable()

	systray.AddSeparator()

	mDashboard := systray.AddMenuItem("Open Dashboard", "Open the Apex dashboard in your browser")
	mDashboard.Click(func() {
		if a.actions.OpenDashboard != nil {
			a.actions.OpenDashboard()
		}
	})

	mLogs := systray.AddMenuItem("View Logs", "Open the agent log file")
	mLogs.Click(func() {
		if a.actions.ViewLogs != nil {
			a.actions.ViewLogs()
		}
	})

	systray.AddSeparator()

	mRestartTunnel := systray.AddMenuItem("Restart Tunnel", "Force tunnel reconnection")
	mRestartTunnel.Click(func() {
		if a.actions.RestartTunnel != nil {
			a.actions.RestartTunnel()
		}
	})

	mUpdate := systray.AddMenuItem("Check for Updates", "Check for a newer agent version")
	mUpdate.Click(func() {
		// If there's a pending update, apply it; otherwise just check
		if ver, ok := a.updates.HasPendingUpdate(); ok {
			mUpdate.SetTitle(fmt.Sprintf("Installing v%s...", ver))
			mUpdate.Disable()
			if a.actions.ApplyUpdate != nil {
				go a.actions.ApplyUpdate()
			}
		} else {
			if a.actions.CheckUpdate != nil {
				a.actions.CheckUpdate()
			}
		}
	})

	mQuit := systray.AddMenuItem("Quit", "Stop Apex Agent")
	mQuit.Click(func() {
		if a.actions.Quit != nil {
			a.actions.Quit()
		}
		systray.Quit()
	})

	// Wire up the menu to the icon click
	systray.CreateMenu()

	// Update loop
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			a.updateMenu(mTunnel, mContainers, mUpdate)
		}
	}()
}

func (a *App) updateMenu(mTunnel, mContainers, mUpdate *systray.MenuItem) {
	// Check for fleet removal first
	if a.status.IsRemoved() {
		systray.SetIcon(iconRed)
		systray.SetTooltip("Apex Agent — Removed from Fleet")
		mTunnel.SetTitle("Removed from Fleet")
		mContainers.SetTitle("This Mac was removed by an admin")
		return
	}

	state := a.status.TunnelState()
	containers := a.status.Containers()

	// Update icon color
	switch {
	case state != tunnel.StateConnected:
		systray.SetIcon(iconRed)
	case hasUnhealthy(containers):
		systray.SetIcon(iconYellow)
	default:
		systray.SetIcon(iconGreen)
	}

	// Update tunnel status
	switch state {
	case tunnel.StateConnected:
		since := a.status.TunnelConnectedAt()
		duration := time.Since(since)
		mTunnel.SetTitle(fmt.Sprintf("Tunnel: Connected (%s)", formatDuration(duration)))
	case tunnel.StateConnecting:
		mTunnel.SetTitle("Tunnel: Connecting...")
	default:
		mTunnel.SetTitle("Tunnel: Disconnected")
	}

	// Update containers
	running := 0
	for _, c := range containers {
		if c.Running {
			running++
		}
	}
	mContainers.SetTitle(fmt.Sprintf("Containers: %d running", running))

	// Update available indicator
	if ver, ok := a.updates.HasPendingUpdate(); ok {
		mUpdate.SetTitle(fmt.Sprintf("Update to v%s", ver))
		mUpdate.Enable()
	}
}

func (a *App) onExit() {
	close(a.done)
}

func hasUnhealthy(containers []container.ContainerStatus) bool {
	for _, c := range containers {
		if !c.Running {
			return true
		}
	}
	return false
}

func formatDuration(d time.Duration) string {
	days := int(d.Hours() / 24)
	hours := int(d.Hours()) % 24
	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}
