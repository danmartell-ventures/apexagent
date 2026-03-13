# CLAUDE.md — Apex Agent

## Context

This agent runs on remote BYOH (Bring Your Own Hardware) Macs. It is a tunnel that allows **apexhost** (`app.apex.host`) to configure and manage those machines remotely. The agent is installed and configured by apexhost via SSH during host preparation (`apexhost/src/lib/host-preparation.ts`).

**When debugging issues with the agent, fix the root cause in apexhost or in this repo's source code. Do NOT fix things locally on the host machine — the point is that everything is managed remotely.**

## Overview

macOS background daemon. Shows a menu bar icon with tunnel status and container count. Monitors Docker containers, maintains a reverse SSH tunnel to the management server, sends telemetry, and performs auto-recovery.

## Commands

```bash
make build           # Build binary to bin/apex-agent
make run             # Build and run in foreground
make test            # Run tests
make pkg VERSION=x.y.z  # Build signed PKG installer locally
```

## Release & Distribution

Releases are distributed as **signed and notarized macOS PKG installers** via GitHub Releases.

**To release:**
1. Commit to `main`, push
2. `git tag v0.2.7 && git push origin v0.2.7`
3. GitHub Actions builds, codesigns, notarizes, and uploads `ApexAgent-{version}.pkg`
4. Download PKG from https://github.com/danmartell-ventures/apexagent/releases

**Do NOT create pull requests.** Commit directly to main and tag.

**CI pipeline** (`.github/workflows/release.yml`):
- GoReleaser builds universal macOS binary (amd64 + arm64)
- Binary is codesigned with Developer ID Application cert
- PKG is built with `pkgbuild` + `productbuild`, signed with Developer ID Installer cert
- PKG is notarized with Apple and stapled
- PKG is uploaded to the GitHub Release

**Apple signing:** Certs for `everydev, LLC (G262XC7MP2)` are stored as GitHub Actions secrets. The signing identity, app-specific password, and team ID are all in the repo secrets — no local machine dependency. Any machine with git push access can trigger a release.

**Key packaging files:**
- `packaging/distribution.xml` — Installer UI config
- `packaging/welcome.html` — Installer welcome text
- `packaging/entitlements.plist` — Binary entitlements
- `packaging/scripts/postinstall` — Post-install script (starts launchd service)
- `.goreleaser.yml` — GoReleaser config (binary build + GitHub release)

## Architecture

- `cmd/apex-agent/main.go` — CLI entry point (run, setup, status, doctor, restart)
- `internal/agent/agent.go` — Top-level orchestrator, wires all subsystems
- `internal/menubar/app.go` — macOS menu bar icon (systray)
- `internal/container/monitor.go` — Polls Docker every 10s, auto-recovers stopped containers
- `internal/container/docker.go` — Docker CLI wrapper with launchd PATH resolution
- `internal/tunnel/manager.go` — Reverse SSH tunnel to management server
- `internal/telemetry/` — Reports container/host metrics to apexhost
- `internal/config/config.go` — TOML config at `~/.apex/agent.toml`
- `internal/update/self.go` — Self-update via GitHub releases
- `internal/platform/` — macOS-specific: launchd, power events, network events

## Key Behaviors

- **Docker API version:** Pinned to `DOCKER_API_VERSION=1.44` to handle client/engine version skew (Colima)
- **Docker path resolution:** `findDocker()` checks `/usr/local/bin`, `/opt/homebrew/bin`, Docker.app, OrbStack paths — launchd has minimal PATH
- **Network events:** Debounced (5s after last event) — macOS fires many events during network changes
- **Container prefix:** `apex-` by default, configurable in `agent.toml`
- **Docker runtime:** BYOHs use Colima
- **The agent does NOT create containers.** It only monitors, recovers, and reports on them. Containers are provisioned from the apexhost management server via SSH to the host.

## Known Issues

- **Homebrew tap push:** GoReleaser fails to push to `danmartell-ventures/homebrew-tap` (403). `continue-on-error: true` on the GoReleaser step so PKG build still runs. Fix by giving `HOMEBREW_TAP_GITHUB_TOKEN` write access to that repo.
- **Go module rename:** Module was renamed from `github.com/danmartell-ventures/apex-agent` to `github.com/danmartell-ventures/apexagent`. The `.goreleaser.yml` release target must match the repo name.
- **PKG codesign in CI:** The binary MUST be signed AFTER copying to `pkg-root/` — `cp` strips codesign signatures. The CI workflow mirrors the `make pkg` target exactly for this reason. Do not refactor one without the other.
- **Installer background image:** Removed. The macOS installer `<background>` element doesn't render well. Don't add it back.
