package platform

import (
	"fmt"
	"os/exec"
	"strings"
)

// ShowSetupDialog displays a native macOS dialog asking for the setup token.
// Returns the token, server URL, and whether the user confirmed (ok=true) or cancelled (ok=false).
func ShowSetupDialog(defaultServer string) (token string, server string, ok bool) {
	script := fmt.Sprintf(`
set tokenResult to display dialog "Welcome to Apex Agent!" & return & return & "Paste your setup token from the Apex dashboard:" default answer "" buttons {"Cancel", "Connect"} default button "Connect" with title "Apex Agent Setup" with icon note
set tokenText to text returned of tokenResult

if tokenText is "" then
	display dialog "Token cannot be empty." buttons {"OK"} default button "OK" with title "Apex Agent Setup" with icon stop
	error number -128
end if

set serverURL to "%s"

return tokenText & "|" & serverURL
`, defaultServer)

	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		return "", "", false
	}

	result := strings.TrimSpace(string(out))
	parts := strings.SplitN(result, "|", 2)
	if len(parts) != 2 {
		return "", "", false
	}

	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), true
}

// ShowErrorDialog displays a native macOS error alert.
func ShowErrorDialog(title, message string) {
	script := fmt.Sprintf(
		`display dialog %q buttons {"OK"} default button "OK" with title %q with icon stop`,
		message, title,
	)
	exec.Command("osascript", "-e", script).Run()
}

// ShowInfoDialog displays a native macOS informational alert.
func ShowInfoDialog(title, message string) {
	script := fmt.Sprintf(
		`display dialog %q buttons {"OK"} default button "OK" with title %q with icon note`,
		message, title,
	)
	exec.Command("osascript", "-e", script).Run()
}

// IsInteractiveTerminal returns true if stdin is a terminal (TTY).
// Used to decide between CLI prompts vs GUI dialogs.
func IsInteractiveTerminal() bool {
	cmd := exec.Command("test", "-t", "0")
	return cmd.Run() == nil
}
