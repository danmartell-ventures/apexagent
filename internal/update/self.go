package update

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creativeprojects/go-selfupdate"

	"github.com/danmartell-ventures/apex-agent/internal/platform"
	"github.com/danmartell-ventures/apex-agent/pkg/version"
)

const (
	checkInterval = 6 * time.Hour
)

// Updater handles self-updates from GitHub releases.
// Background loop only detects updates; actual installation is user-initiated
// via the menubar or CLI.
type Updater struct {
	log     *slog.Logger
	enabled bool

	mu             sync.RWMutex
	pendingVersion string
	pendingRelease *selfupdate.Release
	pendingUpdater *selfupdate.Updater
}

func NewUpdater(enabled bool, log *slog.Logger) *Updater {
	return &Updater{
		log:     log.With("component", "updater"),
		enabled: enabled,
	}
}

// HasPendingUpdate returns the available version if an update was detected.
func (u *Updater) HasPendingUpdate() (newVersion string, available bool) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	if u.pendingVersion != "" {
		return u.pendingVersion, true
	}
	return "", false
}

// Run periodically checks for updates (detect only, no install).
func (u *Updater) Run(ctx context.Context) error {
	if !u.enabled {
		u.log.Info("auto-update disabled")
		return nil
	}

	// Check immediately on start, then every 6 hours
	u.detectUpdate(ctx)

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			u.detectUpdate(ctx)
		}
	}
}

// CheckNow performs an immediate detect + install (for CLI `apex-agent update`).
func (u *Updater) CheckNow(ctx context.Context) (updated bool, newVersion string, err error) {
	u.detectUpdate(ctx)

	ver, available := u.HasPendingUpdate()
	if !available {
		return false, "", nil
	}

	applied, appliedVer, err := u.ApplyUpdate(ctx)
	if err != nil {
		return false, "", err
	}
	if applied {
		return true, appliedVer, nil
	}
	_ = ver
	return false, "", nil
}

// ApplyUpdate installs a previously detected pending update.
// Tries direct write first; if permission denied (root-owned binary from PKG install),
// shows the native macOS admin password dialog to elevate.
func (u *Updater) ApplyUpdate(ctx context.Context) (updated bool, newVersion string, err error) {
	u.mu.RLock()
	release := u.pendingRelease
	updater := u.pendingUpdater
	ver := u.pendingVersion
	u.mu.RUnlock()

	if release == nil || updater == nil {
		return false, "", nil
	}

	u.log.Info("applying update", "version", ver)

	exe, err := selfupdate.ExecutablePath()
	if err != nil {
		return false, "", err
	}

	// Try direct update (works when binary is user-owned, e.g. Homebrew)
	if err := updater.UpdateTo(ctx, release, exe); err != nil {
		u.log.Warn("direct update failed, trying elevated install", "error", err)

		// Binary is likely root-owned (PKG install). Show admin password dialog.
		if elevErr := u.elevatedUpdate(ctx, release, exe); elevErr != nil {
			return false, "", fmt.Errorf("update failed: direct: %w, elevated: %v", err, elevErr)
		}
	}

	// Clear pending state
	u.mu.Lock()
	u.pendingVersion = ""
	u.pendingRelease = nil
	u.pendingUpdater = nil
	u.mu.Unlock()

	u.log.Info("update applied, restarting via launchd", "new_version", ver)
	if err := platform.RestartService(); err != nil {
		u.log.Error("failed to restart after update", "error", err)
	}

	return true, ver, nil
}

// detectUpdate checks GitHub for a newer release and stores it as pending.
func (u *Updater) detectUpdate(ctx context.Context) {
	if version.Version == "dev" {
		u.log.Debug("skipping update check in dev build")
		return
	}

	source, err := selfupdate.NewGitHubSource(selfupdate.GitHubConfig{})
	if err != nil {
		u.log.Error("update check failed", "error", err)
		return
	}

	updater, err := selfupdate.NewUpdater(selfupdate.Config{
		Source: source,
	})
	if err != nil {
		u.log.Error("update check failed", "error", err)
		return
	}

	latest, found, err := updater.DetectLatest(ctx, selfupdate.NewRepositorySlug("danmartell-ventures", "apex-agent"))
	if err != nil {
		u.log.Error("update check failed", "error", err)
		return
	}
	if !found {
		return
	}

	currentVer := version.Version
	if len(currentVer) > 0 && currentVer[0] == 'v' {
		currentVer = currentVer[1:]
	}

	if !latest.GreaterThan(currentVer) {
		u.log.Debug("already up to date", "current", version.Version, "latest", latest.Version())
		u.mu.Lock()
		u.pendingVersion = ""
		u.pendingRelease = nil
		u.pendingUpdater = nil
		u.mu.Unlock()
		return
	}

	u.log.Info("update available", "current", version.Version, "latest", latest.Version())
	u.mu.Lock()
	u.pendingVersion = latest.Version()
	u.pendingRelease = latest
	u.pendingUpdater = updater
	u.mu.Unlock()
}

// elevatedUpdate downloads the new binary to a temp file, then uses osascript
// with administrator privileges to move it into place. Shows the standard macOS
// admin password dialog.
func (u *Updater) elevatedUpdate(ctx context.Context, release *selfupdate.Release, destPath string) error {
	tmpFile, err := os.CreateTemp("", "apex-agent-update-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	u.log.Info("downloading update to temp file", "path", tmpPath)

	downloadURL := release.AssetURL
	if downloadURL == "" {
		return fmt.Errorf("no asset URL in release")
	}

	req, err := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
	if err != nil {
		tmpFile.Close()
		return fmt.Errorf("creating download request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		tmpFile.Close()
		return fmt.Errorf("downloading update: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		tmpFile.Close()
		return fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		tmpFile.Close()
		return fmt.Errorf("writing update: %w", err)
	}
	tmpFile.Close()

	if err := os.Chmod(tmpPath, 0755); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}

	script := fmt.Sprintf(
		`do shell script "mv -f %q %q && chmod 755 %q" with administrator privileges`,
		tmpPath, destPath, destPath,
	)

	u.log.Info("requesting admin privileges to install update")
	if err := exec.Command("osascript", "-e", script).Run(); err != nil {
		return fmt.Errorf("elevated move failed: %w", err)
	}

	u.log.Info("elevated update completed successfully")
	return nil
}
