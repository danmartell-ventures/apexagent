package platform

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// CheckSSH verifies that local SSH is accessible on port 22.
func CheckSSH() bool {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:22", 3*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// EnableSSH tries to enable Remote Login programmatically (requires sudo).
// Returns true if SSH becomes available. Uses terminal sudo prompts.
func EnableSSH() bool {
	// Try systemsetup (works on most macOS versions)
	cmd := exec.Command("sudo", "systemsetup", "-setremotelogin", "on")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err == nil {
		time.Sleep(time.Second)
		if CheckSSH() {
			return true
		}
	}

	// Fallback: launchctl
	cmd = exec.Command("sudo", "launchctl", "load", "-w", "/System/Library/LaunchDaemons/ssh.plist")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err == nil {
		time.Sleep(time.Second)
		if CheckSSH() {
			return true
		}
	}

	return false
}

// EnableSSHGUI tries to enable Remote Login using the native macOS admin password dialog.
// Shows the standard macOS lock icon / admin authentication prompt (no Terminal needed).
// Returns true if SSH becomes available.
func EnableSSHGUI() bool {
	// osascript "do shell script ... with administrator privileges" triggers the macOS
	// authentication dialog with the lock icon — same UX as System Settings.
	script := `do shell script "systemsetup -setremotelogin on" with administrator privileges`
	if err := exec.Command("osascript", "-e", script).Run(); err == nil {
		time.Sleep(time.Second)
		if CheckSSH() {
			return true
		}
	}

	// Fallback: try launchctl via GUI elevation
	script = `do shell script "launchctl load -w /System/Library/LaunchDaemons/ssh.plist" with administrator privileges`
	if err := exec.Command("osascript", "-e", script).Run(); err == nil {
		time.Sleep(time.Second)
		if CheckSSH() {
			return true
		}
	}

	return false
}

// InstallAuthorizedKey adds a public key to ~/.ssh/authorized_keys.
func InstallAuthorizedKey(pubKey string) error {
	home, _ := os.UserHomeDir()
	sshDir := filepath.Join(home, ".ssh")

	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return err
	}

	authKeysPath := filepath.Join(sshDir, "authorized_keys")

	// Read existing
	existing, _ := os.ReadFile(authKeysPath)

	// Check if key already present
	if len(existing) > 0 {
		for _, line := range splitLines(string(existing)) {
			if line == pubKey {
				return nil // Already installed
			}
		}
	}

	// Append
	f, err := os.OpenFile(authKeysPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = fmt.Fprintf(f, "%s\n", pubKey)
	return err
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// DetectHardware returns system specs.
func DetectHardware() (cpuCores int, ramMB int, diskGB int, err error) {
	// CPU cores
	out, err := exec.Command("sysctl", "-n", "hw.ncpu").Output()
	if err == nil {
		fmt.Sscanf(string(out), "%d", &cpuCores)
	}

	// RAM
	out, err = exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err == nil {
		var bytes int64
		fmt.Sscanf(string(out), "%d", &bytes)
		ramMB = int(bytes / 1024 / 1024)
	}

	// Disk
	out, err = exec.Command("df", "-g", "/").Output()
	if err == nil {
		lines := splitLines(string(out))
		if len(lines) >= 2 {
			var total int
			fmt.Sscanf(lines[1], "%*s %d", &total)
			diskGB = total
		}
	}

	return cpuCores, ramMB, diskGB, nil
}
