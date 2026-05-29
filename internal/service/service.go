// Package service installs officetowd as a persistent background service so
// sync survives logout/reboot — the installer runs one sync, but without this
// the daemon never restarts and the user's files silently stop syncing.
//
// macOS: a per-user LaunchAgent (~/Library/LaunchAgents). Linux: a systemd
// --user unit. Other platforms: unsupported (caller falls back to "run
// officetowd start yourself").
package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

const label = "au.jezweb.officetowd"

// Plan is the OS-specific install plan: where the unit file goes, its content,
// and the commands that load/enable it. Pure (no I/O) so it's unit-testable.
type Plan struct {
	UnitPath string
	Content  string
	// LoadCmds run after the file is written (install); UnloadCmds before removal.
	LoadCmds   [][]string
	UnloadCmds [][]string
	Supported  bool
}

// PlanFor builds the install plan for an OS, the officetowd binary path, and the
// user's home dir. Exposed so tests can check both darwin and linux output.
func PlanFor(goos, execPath, home string) Plan {
	switch goos {
	case "darwin":
		unit := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
		log := filepath.Join(home, ".officetowd", "officetowd.log")
		content := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + label + `</string>
  <key>ProgramArguments</key>
  <array>
    <string>` + execPath + `</string>
    <string>start</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>` + log + `</string>
  <key>StandardErrorPath</key><string>` + log + `</string>
</dict>
</plist>
`
		return Plan{
			UnitPath:   unit,
			Content:    content,
			LoadCmds:   [][]string{{"launchctl", "unload", unit}, {"launchctl", "load", "-w", unit}},
			UnloadCmds: [][]string{{"launchctl", "unload", "-w", unit}},
			Supported:  true,
		}
	case "linux":
		unit := filepath.Join(home, ".config", "systemd", "user", "officetowd.service")
		content := `[Unit]
Description=Office Town sync daemon
After=network-online.target

[Service]
ExecStart=` + execPath + ` start
Restart=always
RestartSec=10

[Install]
WantedBy=default.target
`
		return Plan{
			UnitPath:   unit,
			Content:    content,
			LoadCmds:   [][]string{{"systemctl", "--user", "daemon-reload"}, {"systemctl", "--user", "enable", "--now", "officetowd.service"}},
			UnloadCmds: [][]string{{"systemctl", "--user", "disable", "--now", "officetowd.service"}},
			Supported:  true,
		}
	default:
		return Plan{Supported: false}
	}
}

func currentPlan() (Plan, error) {
	exe, err := os.Executable()
	if err != nil {
		return Plan{}, err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Plan{}, err
	}
	return PlanFor(runtime.GOOS, exe, home), nil
}

// Install writes the unit file and loads it. If printOnly, it writes the unit
// content to stdout and does nothing else (for inspection / dry runs).
func Install(printOnly bool) error {
	plan, err := currentPlan()
	if err != nil {
		return err
	}
	if !plan.Supported {
		return fmt.Errorf("auto-start service not supported on %s — run 'officetowd start' yourself (or via your own launcher)", runtime.GOOS)
	}
	if printOnly {
		fmt.Printf("# would write %s\n\n%s", plan.UnitPath, plan.Content)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(plan.UnitPath), 0o755); err != nil {
		return fmt.Errorf("create unit dir: %w", err)
	}
	if err := os.WriteFile(plan.UnitPath, []byte(plan.Content), 0o644); err != nil {
		return fmt.Errorf("write unit: %w", err)
	}
	for _, c := range plan.LoadCmds {
		// launchctl unload of a not-yet-loaded agent errors harmlessly; ignore.
		_ = exec.Command(c[0], c[1:]...).Run()
	}
	fmt.Printf("officetowd: installed background service (%s)\n", plan.UnitPath)
	fmt.Println("officetowd: it will sync automatically and restart on login/reboot.")
	return nil
}

// Uninstall unloads the service and removes the unit file.
func Uninstall() error {
	plan, err := currentPlan()
	if err != nil {
		return err
	}
	if !plan.Supported {
		return nil // nothing to do
	}
	for _, c := range plan.UnloadCmds {
		_ = exec.Command(c[0], c[1:]...).Run()
	}
	if err := os.Remove(plan.UnitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit: %w", err)
	}
	fmt.Println("officetowd: removed background service.")
	return nil
}
