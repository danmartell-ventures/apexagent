package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/danmartell-ventures/apex-agent/internal/agent"
	"github.com/danmartell-ventures/apex-agent/internal/config"
	"github.com/danmartell-ventures/apex-agent/internal/logging"
	"github.com/danmartell-ventures/apex-agent/internal/platform"
	"github.com/danmartell-ventures/apex-agent/internal/update"
	"github.com/danmartell-ventures/apex-agent/pkg/version"
)

var cfgPath string

func main() {
	root := &cobra.Command{
		Use:   "apex-agent",
		Short: "Apex Agent — unified daemon for BYOH Mac hosts",
	}

	root.PersistentFlags().StringVar(&cfgPath, "config", config.DefaultConfigPath(), "config file path")

	root.AddCommand(
		runCmd(),
		setupCmd(),
		statusCmd(),
		doctorCmd(),
		logsCmd(),
		restartCmd(),
		updateCmd(),
		uninstallCmd(),
		versionCmd(),
	)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func runCmd() *cobra.Command {
	var foreground, headless bool

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the agent daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				// Config doesn't exist or is invalid — trigger first-run setup
				if !headless {
					if setupErr := runFirstRunSetup(); setupErr != nil {
						// If user cancelled or setup failed, show error and exit
						platform.ShowErrorDialog("Apex Agent", fmt.Sprintf("Setup failed: %v", setupErr))
						return setupErr
					}
					// Reload config after successful setup
					cfg, err = config.Load(cfgPath)
					if err != nil {
						platform.ShowErrorDialog("Apex Agent", fmt.Sprintf("Config error after setup: %v", err))
						return fmt.Errorf("loading config after setup: %w", err)
					}
				} else {
					return fmt.Errorf("loading config: %w", err)
				}
			}

			log, err := logging.Setup(cfg.Agent.LogDir, cfg.Agent.LogLevel, foreground)
			if err != nil {
				return fmt.Errorf("setting up logging: %w", err)
			}

			log.Info("starting apex-agent", "version", version.Version)

			a := agent.New(cfg, log, headless || foreground)

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sigCh
				log.Info("received shutdown signal")
				a.Stop()
			}()

			return a.Run()
		},
	}

	cmd.Flags().BoolVar(&foreground, "foreground", false, "run in foreground with stderr logging")
	cmd.Flags().BoolVar(&headless, "headless", false, "run without menubar icon")

	return cmd
}

// runFirstRunSetup shows the native macOS setup dialog and runs the setup flow.
func runFirstRunSetup() error {
	token, server, ok := platform.ShowSetupDialog("https://app.apex.host")
	if !ok {
		return fmt.Errorf("setup cancelled by user")
	}
	return runSetup(token, server, false)
}

func setupCmd() *cobra.Command {
	var migrate bool
	var serverURL string

	cmd := &cobra.Command{
		Use:   "setup [TOKEN]",
		Short: "Bootstrap the agent with a setup token",
		Long:  "Bootstrap the agent with a setup token. If no token is provided, a native dialog will prompt for it.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				return runSetup(args[0], serverURL, migrate)
			}
			// No token provided — show GUI dialog
			token, server, ok := platform.ShowSetupDialog(serverURL)
			if !ok {
				return fmt.Errorf("setup cancelled")
			}
			return runSetup(token, server, migrate)
		},
	}

	cmd.Flags().BoolVar(&migrate, "migrate", false, "migrate from existing shell scripts")
	cmd.Flags().StringVar(&serverURL, "server", "https://app.apex.host", "server URL for bootstrap")
	return cmd
}

func statusCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show agent status",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}

			log, _ := logging.Setup(cfg.Agent.LogDir, "error", false)
			a := agent.New(cfg, log, true)

			status := a.Status()

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(status)
			}

			fmt.Printf("Tunnel:     %s\n", status.TunnelState)
			if status.TunnelUptime != "" {
				fmt.Printf("Uptime:     %s\n", status.TunnelUptime)
			}
			fmt.Printf("Forwards:   %d active\n", status.Forwards)
			fmt.Printf("Containers: %d\n", len(status.Containers))
			for _, c := range status.Containers {
				state := "stopped"
				if c.Running {
					state = fmt.Sprintf("running (%.1f%% CPU, %.0fMB)", c.CPU, c.MemMB)
				}
				fmt.Printf("  %s — %s\n", c.Name, state)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return cmd
}

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Run diagnostics",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}

			log, _ := logging.Setup(cfg.Agent.LogDir, "error", false)
			a := agent.New(cfg, log, true)

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			// Check SSH
			fmt.Print("SSH (port 22): ")
			if !platform.CheckSSH() {
				fmt.Println("FAIL — not accessible")
			} else {
				fmt.Println("OK")
			}

			// Check tunnel key
			fmt.Print("Tunnel key:    ")
			if _, err := os.Stat(cfg.Tunnel.KeyPath); err != nil {
				fmt.Printf("FAIL — %v\n", err)
			} else {
				fmt.Println("OK")
			}

			// Check Docker
			fmt.Print("Docker:        ")
			if _, err := os.Stat("/var/run/docker.sock"); err == nil {
				fmt.Println("OK (default socket)")
			} else {
				home, _ := os.UserHomeDir()
				colimaSocket := filepath.Join(home, ".colima", "default", "docker.sock")
				if _, err := os.Stat(colimaSocket); err == nil {
					fmt.Println("OK (Colima)")
				} else {
					fmt.Println("FAIL — no Docker socket found")
				}
			}

			// Container diagnostics
			results, err := a.RunDiagnostics(ctx)
			if err != nil {
				fmt.Printf("Container check: FAIL — %v\n", err)
			} else {
				for _, r := range results {
					fmt.Printf("\nContainer %s:\n", r.Name)
					if r.Running {
						fmt.Printf("  Status:  running\n")
						fmt.Printf("  Health:  %s\n", r.Health)
						if r.DoctorErr != nil {
							fmt.Printf("  Doctor:  FAIL — %v\n", r.DoctorErr)
						} else {
							fmt.Printf("  Doctor:  %s\n", r.DoctorOut)
						}
					} else {
						fmt.Printf("  Status:  STOPPED\n")
					}
				}
			}

			return nil
		},
	}
}

func logsCmd() *cobra.Command {
	var follow bool

	cmd := &cobra.Command{
		Use:   "logs",
		Short: "View agent logs",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}

			logPath := logging.LogPath(cfg.Agent.LogDir)
			tailArgs := []string{"-100", logPath}
			if follow {
				tailArgs = []string{"-f", logPath}
			}

			tailCmd := exec.Command("tail", tailArgs...)
			tailCmd.Stdout = os.Stdout
			tailCmd.Stderr = os.Stderr
			return tailCmd.Run()
		},
	}

	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow log output")
	return cmd
}

func restartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart [tunnel|container <name>]",
		Short: "Restart tunnel or a container",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				fmt.Println("Restarting agent via launchd...")
				return platform.RestartService()
			}

			switch args[0] {
			case "tunnel":
				fmt.Println("Restarting agent (tunnel will reconnect)...")
				return platform.RestartService()
			case "container":
				if len(args) < 2 {
					return fmt.Errorf("specify container name")
				}
				cfg, err := config.Load(cfgPath)
				if err != nil {
					return err
				}
				log, _ := logging.Setup(cfg.Agent.LogDir, "error", false)
				a := agent.New(cfg, log, true)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				return a.RestartContainer(ctx, args[1])
			default:
				return fmt.Errorf("unknown target: %s (use 'tunnel' or 'container <name>')", args[0])
			}
		},
	}
}

func updateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Check for and apply updates",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("Current version: %s\n", version.Version)
			fmt.Println("Checking for updates...")

			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			log, _ := logging.Setup(cfg.Agent.LogDir, "error", false)

			u := update.NewUpdater(true, log)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			updated, newVer, err := u.CheckNow(ctx)
			if err != nil {
				return fmt.Errorf("update check failed: %w", err)
			}
			if updated {
				fmt.Printf("Updated to %s. Restarting...\n", newVer)
				return platform.RestartService()
			}
			fmt.Println("Already up to date.")
			return nil
		},
	}
}

func uninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove agent service and cleanup",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Uninstalling Apex Agent...")

			if err := platform.UninstallService(); err != nil {
				fmt.Printf("Warning: %v\n", err)
			}

			platform.RemoveLegacyPlists()

			fmt.Println("Service removed. Config and data at ~/.apex/ preserved.")
			fmt.Println("To fully remove: rm -rf ~/.apex/")
			return nil
		},
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(version.Full())
		},
	}
}

// --- Setup flow ---

type bootstrapResponse struct {
	TokenID         string `json:"token_id"`
	HostName        string `json:"host_name"`
	AuthorizedKey   string `json:"authorized_key"`
	ManagementHost  string `json:"management_host"`
	ManagementURL   string `json:"management_url"`
	TunnelPort      int    `json:"tunnel_port"`
	TunnelKey       string `json:"tunnel_key"`
	ContainerPrefix string `json:"container_prefix"`
	Platform        string `json:"platform"`
	OwnerType       string `json:"owner_type"`
}

type phoneHomeResponse struct {
	OK              bool   `json:"ok"`
	HostID          string `json:"host_id"`
	ReportingToken  string `json:"reporting_token"`
}

func runSetup(token string, serverURL string, migrate bool) error {
	fmt.Println("Apex Agent Setup")
	fmt.Println("=================")

	// Step 1: Fetch bootstrap config
	fmt.Print("Fetching configuration... ")
	resp, err := fetchBootstrap(serverURL, token)
	if err != nil {
		return fmt.Errorf("\nfailed: %w", err)
	}
	fmt.Println("OK")

	// Step 2: Check SSH
	fmt.Print("Checking SSH access... ")
	if !platform.CheckSSH() {
		fmt.Println("not enabled")
		isGUI := !platform.IsInteractiveTerminal()
		if isGUI {
			// GUI mode (launched from launchd/PKG installer) — use native admin dialog
			fmt.Println("→ Enabling Remote Login via system dialog...")
			if !platform.EnableSSHGUI() {
				platform.ShowErrorDialog("Apex Agent Setup",
					"Could not enable Remote Login (SSH).\n\n"+
						"Please open System Settings → General → Sharing → Remote Login and toggle it ON, then restart Apex Agent.")
				return fmt.Errorf("SSH is not available — please enable Remote Login and try again")
			}
			fmt.Println("  SSH enabled")
		} else {
			// Terminal mode — use sudo prompts
			fmt.Println("→ Enabling Remote Login (you may be prompted for your password)...")
			if platform.EnableSSH() {
				fmt.Println("  SSH enabled")
			} else {
				fmt.Println()
				fmt.Println("  ┌─────────────────────────────────────────────────────────────┐")
				fmt.Println("  │                                                             │")
				fmt.Println("  │  Could not enable SSH automatically.                        │")
				fmt.Println("  │                                                             │")
				fmt.Println("  │  Open System Settings → General → Sharing → Remote Login    │")
				fmt.Println("  │  and toggle it ON.                                          │")
				fmt.Println("  │                                                             │")
				fmt.Println("  │  Press Enter here once it's enabled...                      │")
				fmt.Println("  │                                                             │")
				fmt.Println("  └─────────────────────────────────────────────────────────────┘")
				fmt.Println()
				fmt.Scanln()
				if !platform.CheckSSH() {
					return fmt.Errorf("SSH is still not available — please enable Remote Login and try again")
				}
			}
		}
	}
	fmt.Println("OK")

	// Step 3: Install authorized key
	fmt.Print("Installing SSH key... ")
	if err := platform.InstallAuthorizedKey(resp.AuthorizedKey); err != nil {
		return fmt.Errorf("\nfailed: %w", err)
	}
	fmt.Println("OK")

	// Step 4: Write tunnel key
	fmt.Print("Writing tunnel key... ")
	home, _ := os.UserHomeDir()
	apexDir := filepath.Join(home, ".apex")
	os.MkdirAll(apexDir, 0755)

	keyPath := filepath.Join(apexDir, "tunnel_key")
	if err := os.WriteFile(keyPath, []byte(resp.TunnelKey), 0600); err != nil {
		return fmt.Errorf("\nfailed: %w", err)
	}
	fmt.Println("OK")

	// Step 5: Write config (host_id and reporting_token filled after phone-home)
	fmt.Print("Writing config... ")
	cfg := &config.Config{
		Server: config.ServerConfig{
			URL: resp.ManagementURL,
		},
		Tunnel: config.TunnelConfig{
			KeyPath:        keyPath,
			ManagementHost: resp.ManagementHost,
			TunnelPort:     resp.TunnelPort,
		},
		Docker: config.DockerConfig{
			ContainerPrefix: resp.ContainerPrefix,
		},
		Agent: config.AgentConfig{
			AutoUpdate: true,
		},
		Backup: config.BackupConfig{
			Enabled: true,
		},
	}
	if err := cfg.Write(config.DefaultConfigPath()); err != nil {
		return fmt.Errorf("\nfailed: %w", err)
	}
	os.WriteFile(config.DefaultForwardsPath(), []byte(""), 0644)
	fmt.Println("OK")

	// Step 6: Detect hardware and phone home
	fmt.Print("Detecting hardware... ")
	cpuCores, ramMB, diskGB, _ := platform.DetectHardware()
	fmt.Printf("OK (%d cores, %dMB RAM, %dGB disk)\n", cpuCores, ramMB, diskGB)

	fmt.Print("Registering with mothership... ")
	phResp, err := phoneHome(resp, cpuCores, ramMB, diskGB)
	if err != nil {
		return fmt.Errorf("\nfailed: %w", err)
	}
	fmt.Println("OK")

	// Step 6b: Update config with host_id and reporting_token from phone-home
	cfg.Server.HostID = phResp.HostID
	cfg.Server.ReportingToken = phResp.ReportingToken
	if err := cfg.Write(config.DefaultConfigPath()); err != nil {
		return fmt.Errorf("updating config with host_id: %w", err)
	}

	// Step 7: Install launchd service
	fmt.Print("Installing service... ")
	binaryPath, _ := os.Executable()
	logDir := filepath.Join(apexDir, "logs")
	if err := platform.InstallService(binaryPath, logDir); err != nil {
		return fmt.Errorf("\nfailed: %w", err)
	}
	fmt.Println("OK")

	// Step 8: Remove legacy plists
	if migrate {
		fmt.Print("Removing legacy scripts... ")
		platform.RemoveLegacyPlists()
		fmt.Println("OK")
	}

	fmt.Println()
	fmt.Println("Setup complete! Apex Agent is running.")
	fmt.Printf("Host ID: %s\n", phResp.HostID)
	fmt.Println("Run 'apex-agent status' to verify.")

	return nil
}

func fetchBootstrap(serverURL, token string) (*bootstrapResponse, error) {
	url := fmt.Sprintf("%s/api/docker-hosts/bootstrap/%s?format=json", serverURL, token)
	httpResp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != 200 {
		body, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("server returned %d: %s", httpResp.StatusCode, strings.TrimSpace(string(body)))
	}

	var resp bootstrapResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func phoneHome(resp *bootstrapResponse, cpuCores, ramMB, diskGB int) (*phoneHomeResponse, error) {
	sshUser := os.Getenv("USER")
	payload := map[string]interface{}{
		"token":         resp.TokenID,
		"ssh_user":      sshUser,
		"hw_cpu_cores":  cpuCores,
		"hw_ram_mb":     ramMB,
		"hw_disk_gb":    diskGB,
		"agent_version": version.Version,
	}

	data, _ := json.Marshal(payload)
	httpResp, err := http.Post(
		resp.ManagementURL+"/api/docker-hosts/phone-home",
		"application/json",
		strings.NewReader(string(data)),
	)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != 200 && httpResp.StatusCode != 201 {
		body, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("phone-home returned %d: %s", httpResp.StatusCode, strings.TrimSpace(string(body)))
	}

	var phResp phoneHomeResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&phResp); err != nil {
		return nil, fmt.Errorf("parsing phone-home response: %w", err)
	}
	return &phResp, nil
}
