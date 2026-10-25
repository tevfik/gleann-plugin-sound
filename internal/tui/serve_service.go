package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// serveService is the background plugin server, registered with the OS so it
// starts at login: a systemd user unit on Linux, a LaunchAgent on macOS and a
// logon task on Windows. The TUI's Serve item toggles it instead of running the
// server in (and locking) the terminal.
type serveService struct {
	unit  string // systemd unit / scheduled task name
	label string // launchd label
	desc  string
}

var serveSvc = serveService{
	unit:  "gleann-plugin-sound-serve",
	label: "com.gleann.sound.serve",
	desc:  "gleann-plugin-sound plugin server",
}

// ServeEnabled reports whether the background server is installed and active.
func ServeEnabled() bool { return serveSvc.active() }

// EnableServe installs the background server so it starts now and at login.
func EnableServe(args []string) error {
	bin, err := serveBinary()
	if err != nil {
		return err
	}
	return serveSvc.install(bin, args)
}

// DisableServe stops the background server and removes it from login.
func DisableServe() error { return serveSvc.remove() }

// serveBinary is the binary that is running now: a saved install path goes
// stale when the project moves, and the service would then fail forever.
func serveBinary() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot determine the binary path: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if _, err := os.Stat(exe); err != nil {
		return "", fmt.Errorf("binary %s not found: %w", exe, err)
	}
	return exe, nil
}

func (s serveService) active() bool {
	switch runtime.GOOS {
	case "linux":
		out, err := exec.Command("systemctl", "--user", "is-active", s.unit+".service").Output()
		return err == nil && strings.TrimSpace(string(out)) == "active"
	case "darwin":
		return exec.Command("launchctl", "list", s.label).Run() == nil
	case "windows":
		return exec.Command("schtasks", "/Query", "/TN", s.unit).Run() == nil
	}
	return false
}

func (s serveService) install(bin string, args []string) error {
	switch runtime.GOOS {
	case "linux":
		return s.installSystemd(bin, args)
	case "darwin":
		return s.installLaunchd(bin, args)
	case "windows":
		return s.installWindows(bin, args)
	}
	return fmt.Errorf("background server not supported on %s", runtime.GOOS)
}

func (s serveService) remove() error {
	switch runtime.GOOS {
	case "linux":
		return s.removeSystemd()
	case "darwin":
		return s.removeLaunchd()
	case "windows":
		return s.removeWindows()
	}
	return fmt.Errorf("background server not supported on %s", runtime.GOOS)
}

// ── Linux ──────────────────────────────────────────────────────

func (s serveService) unitPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user", s.unit+".service")
}

func quoteSystemd(a string) string {
	if strings.ContainsAny(a, " \t\"\\") {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(a) + `"`
	}
	return a
}

func (s serveService) installSystemd(bin string, args []string) error {
	if err := os.MkdirAll(filepath.Dir(s.unitPath()), 0o755); err != nil {
		return fmt.Errorf("create systemd dir: %w", err)
	}
	execStart := quoteSystemd(bin)
	for _, a := range args {
		execStart += " " + quoteSystemd(a)
	}
	unit := fmt.Sprintf(`[Unit]
Description=%s
StartLimitIntervalSec=120
StartLimitBurst=5

[Service]
Type=simple
ExecStart=%s
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`, s.desc, execStart)
	if err := os.WriteFile(s.unitPath(), []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write service file: %w", err)
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	if err := exec.Command("systemctl", "--user", "enable", s.unit+".service").Run(); err != nil {
		return fmt.Errorf("enable service: %w", err)
	}
	// Stop first so that "start" launches the new binary rather than no-op.
	_ = exec.Command("systemctl", "--user", "stop", s.unit+".service").Run()
	if err := exec.Command("systemctl", "--user", "start", s.unit+".service").Run(); err != nil {
		return fmt.Errorf("start service: %w", err)
	}
	return nil
}

func (s serveService) removeSystemd() error {
	_ = exec.Command("systemctl", "--user", "stop", s.unit+".service").Run()
	_ = exec.Command("systemctl", "--user", "disable", s.unit+".service").Run()
	if err := os.Remove(s.unitPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove service file: %w", err)
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	return nil
}

// ── macOS ──────────────────────────────────────────────────────

func (s serveService) plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", s.label+".plist")
}

func xmlEscape(a string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(a)
}

func (s serveService) installLaunchd(bin string, args []string) error {
	if err := os.MkdirAll(filepath.Dir(s.plistPath()), 0o755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}
	var progArgs string
	for _, a := range append([]string{bin}, args...) {
		progArgs += fmt.Sprintf("\t\t<string>%s</string>\n", xmlEscape(a))
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
%s	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>StandardOutPath</key>
	<string>/tmp/%s.log</string>
	<key>StandardErrorPath</key>
	<string>/tmp/%s.err</string>
</dict>
</plist>`, s.label, progArgs, s.unit, s.unit)
	// Unload first so a reinstall picks up the new binary and arguments.
	_ = exec.Command("launchctl", "unload", s.plistPath()).Run()
	if err := os.WriteFile(s.plistPath(), []byte(plist), 0o644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}
	if err := exec.Command("launchctl", "load", s.plistPath()).Run(); err != nil {
		return fmt.Errorf("load plist: %w", err)
	}
	return nil
}

func (s serveService) removeLaunchd() error {
	_ = exec.Command("launchctl", "unload", s.plistPath()).Run()
	if err := os.Remove(s.plistPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove plist: %w", err)
	}
	return nil
}

// ── Windows ────────────────────────────────────────────────────

func (s serveService) installWindows(bin string, args []string) error {
	cmdline := `"` + bin + `"`
	for _, a := range args {
		if strings.ContainsAny(a, " \t") {
			a = `"` + a + `"`
		}
		cmdline += " " + a
	}
	if err := exec.Command("schtasks", "/Create", "/TN", s.unit, "/TR", cmdline,
		"/SC", "ONLOGON", "/RL", "LIMITED", "/F").Run(); err != nil {
		return fmt.Errorf("create scheduled task: %w", err)
	}
	_ = exec.Command("schtasks", "/End", "/TN", s.unit).Run()
	_ = exec.Command("schtasks", "/Run", "/TN", s.unit).Run()
	return nil
}

func (s serveService) removeWindows() error {
	_ = exec.Command("schtasks", "/End", "/TN", s.unit).Run()
	if err := exec.Command("schtasks", "/Delete", "/TN", s.unit, "/F").Run(); err != nil {
		return fmt.Errorf("delete scheduled task: %w", err)
	}
	return nil
}
