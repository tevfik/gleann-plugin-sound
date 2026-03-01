package tui

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tevfik/gleann-sound/internal/config"
)

// ── Install Model ──────────────────────────────────────────────

type installPhase int

const (
	installConfirm installPhase = iota
	installRunning
	installDone
)

type InstallModel struct {
	phase     installPhase
	width     int
	height    int
	cancelled bool
	done      bool
	uninstall bool

	// Options (toggleable).
	options []installOpt
	cursor  int

	// Result log.
	log []string
	err string
}

type installOpt struct {
	label    string
	desc     string
	selected bool
}

func NewInstallModel() InstallModel {
	// Pre-select daemon option if it's already installed/running.
	daemonPreSelected := isDaemonRunning()

	return InstallModel{
		phase: installConfirm,
		options: []installOpt{
			{label: "Copy binary to ~/.local/bin", desc: "Install gleann-sound to PATH", selected: true},
			{label: "Shell completions", desc: "bash, zsh, fish autocompletion", selected: true},
			{label: "Setup input group", desc: "udev rules for evdev keyboard access (requires sudo)", selected: true},
			{label: "Start dictate daemon at login", desc: "Auto-start push-to-talk on login (systemd/launchd/schtasks)", selected: daemonPreSelected},
		},
	}
}

func NewUninstallModel() InstallModel {
	return InstallModel{
		phase:     installConfirm,
		uninstall: true,
		options: []installOpt{
			{label: "Remove binary from ~/.local/bin", desc: "Uninstall gleann-sound", selected: true},
			{label: "Remove shell completions", desc: "Remove bash, zsh, fish completions", selected: true},
			{label: "Stop & remove dictate daemon", desc: "Remove auto-start service", selected: true},
			{label: "Remove config", desc: "Delete ~/.gleann/sound.json", selected: false},
			{label: "Remove models", desc: "Delete ~/.gleann/models/ (frees disk space)", selected: false},
		},
	}
}

func (m InstallModel) Init() tea.Cmd {
	return nil
}

func (m InstallModel) Done() bool      { return m.done }
func (m InstallModel) Cancelled() bool { return m.cancelled }

// ── Update ─────────────────────────────────────────────────────

func (m InstallModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.cancelled = true
			return m, tea.Quit
		}
		if msg.String() == "esc" {
			if m.phase == installDone {
				m.done = true
				return m, tea.Quit
			}
			m.cancelled = true
			return m, tea.Quit
		}
		return m.handleKey(msg)
	}
	return m, nil
}

func (m InstallModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.phase {
	case installConfirm:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.options)-1 {
				m.cursor++
			}
		case " ":
			m.options[m.cursor].selected = !m.options[m.cursor].selected
		case "enter":
			if m.uninstall {
				m.runUninstall()
			} else {
				m.runInstall()
			}
			m.phase = installDone
		}
	case installDone:
		switch msg.String() {
		case "enter", "q":
			m.done = true
			return m, tea.Quit
		}
	}
	return m, nil
}

// ── Install logic ──────────────────────────────────────────────

func (m *InstallModel) runInstall() {
	// Option 0: Copy binary.
	if m.options[0].selected {
		if err := installBinary(); err != nil {
			m.log = append(m.log, fmt.Sprintf("✗ Binary install: %v", err))
		} else {
			m.log = append(m.log, "✓ Binary installed to ~/.local/bin/gleann-sound")
		}
	}

	// Option 1: Shell completions.
	if m.options[1].selected {
		results := installCompletions()
		for _, r := range results {
			m.log = append(m.log, "✓ "+r)
		}
		if len(results) == 0 {
			m.log = append(m.log, "✗ No shell completions installed")
		}
	}

	// Option 2: Input group.
	if m.options[2].selected {
		if err := setupInputGroup(); err != nil {
			m.log = append(m.log, fmt.Sprintf("✗ Input group: %v", err))
		} else {
			m.log = append(m.log, "✓ Input group configured (re-login to activate)")
		}
	}

	// Option 3: Dictate daemon.
	if m.options[3].selected {
		if err := installDaemon(); err != nil {
			m.log = append(m.log, fmt.Sprintf("✗ Daemon: %v", err))
		} else {
			m.log = append(m.log, "✓ Dictate daemon installed & started")
		}
	}

	// Update config.
	cfg := config.Load()
	if cfg == nil {
		cfg = &config.Config{}
	}
	cfg.InstallPath = filepath.Join(expandPath("~/.local/bin"), "gleann-sound")
	cfg.CompletionsInstalled = m.options[1].selected
	cfg.InputGroupSetup = m.options[2].selected
	cfg.DaemonEnabled = m.options[3].selected
	_ = config.Save(cfg)
}

func (m *InstallModel) runUninstall() {
	// Option 0: Remove binary.
	if m.options[0].selected {
		path := filepath.Join(expandPath("~/.local/bin"), "gleann-sound")
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			m.log = append(m.log, fmt.Sprintf("✗ Remove binary: %v", err))
		} else {
			m.log = append(m.log, "✓ Removed binary")
		}
	}

	// Option 1: Shell completions.
	if m.options[1].selected {
		removed := removeCompletions()
		for _, r := range removed {
			m.log = append(m.log, "✓ "+r)
		}
	}

	// Option 2: Stop & remove daemon.
	if m.options[2].selected {
		if err := removeDaemon(); err != nil {
			m.log = append(m.log, fmt.Sprintf("✗ Remove daemon: %v", err))
		} else {
			m.log = append(m.log, "✓ Stopped & removed dictate daemon")
		}
	}

	// Option 3: Remove config.
	if m.options[3].selected {
		path := config.ConfigPath()
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			m.log = append(m.log, fmt.Sprintf("✗ Remove config: %v", err))
		} else {
			m.log = append(m.log, "✓ Removed config")
		}
	}

	// Option 4: Remove models.
	if m.options[4].selected {
		dir := config.ModelsDir()
		if err := os.RemoveAll(dir); err != nil {
			m.log = append(m.log, fmt.Sprintf("✗ Remove models: %v", err))
		} else {
			m.log = append(m.log, "✓ Removed models directory")
		}
	}
}

// ── Binary install ─────────────────────────────────────────────

func installBinary() error {
	targetDir := expandPath("~/.local/bin")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("create target dir: %w", err)
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("resolve symlinks: %w", err)
	}

	dst := filepath.Join(targetDir, "gleann-sound")
	if runtime.GOOS == "windows" {
		dst += ".exe"
	}

	// Don't copy onto itself.
	if abs, _ := filepath.Abs(dst); abs == exe {
		return nil
	}

	src, err := os.Open(exe)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer src.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("create destination: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, src); err != nil {
		return fmt.Errorf("copy binary: %w", err)
	}
	return nil
}

// ── Shell completions ──────────────────────────────────────────

func installCompletions() []string {
	var installed []string
	home, _ := os.UserHomeDir()

	// Bash
	bashDir := filepath.Join(home, ".local", "share", "bash-completion", "completions")
	if err := os.MkdirAll(bashDir, 0o755); err == nil {
		path := filepath.Join(bashDir, "gleann-sound")
		if err := os.WriteFile(path, []byte(bashCompletion()), 0o644); err == nil {
			installed = append(installed, "bash → "+path)
		}
	}

	// Zsh
	zshDir := filepath.Join(home, ".local", "share", "zsh", "site-functions")
	if err := os.MkdirAll(zshDir, 0o755); err == nil {
		path := filepath.Join(zshDir, "_gleann-sound")
		if err := os.WriteFile(path, []byte(zshCompletion()), 0o644); err == nil {
			installed = append(installed, "zsh  → "+path)
		}
	}

	// Fish
	fishDir := filepath.Join(home, ".config", "fish", "completions")
	if err := os.MkdirAll(fishDir, 0o755); err == nil {
		path := filepath.Join(fishDir, "gleann-sound.fish")
		if err := os.WriteFile(path, []byte(fishCompletion()), 0o644); err == nil {
			installed = append(installed, "fish → "+path)
		}
	}

	return installed
}

func removeCompletions() []string {
	var removed []string
	home, _ := os.UserHomeDir()

	paths := []struct{ shell, path string }{
		{"bash", filepath.Join(home, ".local", "share", "bash-completion", "completions", "gleann-sound")},
		{"zsh", filepath.Join(home, ".local", "share", "zsh", "site-functions", "_gleann-sound")},
		{"fish", filepath.Join(home, ".config", "fish", "completions", "gleann-sound.fish")},
	}

	for _, p := range paths {
		if err := os.Remove(p.path); err == nil {
			removed = append(removed, fmt.Sprintf("Removed %s completion", p.shell))
		}
	}
	return removed
}

// ── Input group setup ──────────────────────────────────────────

func setupInputGroup() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("input group setup is only supported on Linux")
	}

	// Create udev rule.
	rule := `KERNEL=="event*", SUBSYSTEM=="input", MODE="0660", GROUP="input", TAG+="uaccess"`
	cmd := exec.Command("sudo", "tee", "/etc/udev/rules.d/99-gleann-sound-input.rules")
	cmd.Stdin = strings.NewReader(rule)
	cmd.Stdout = nil
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("create udev rule: %w", err)
	}

	// Reload udev.
	_ = exec.Command("sudo", "udevadm", "control", "--reload-rules").Run()
	_ = exec.Command("sudo", "udevadm", "trigger", "--subsystem-match=input").Run()

	// Add user to input group.
	user := os.Getenv("USER")
	if user == "" {
		user = os.Getenv("LOGNAME")
	}
	if user != "" {
		_ = exec.Command("sudo", "usermod", "-aG", "input", user).Run()
	}

	return nil
}

// ── Daemon management ──────────────────────────────────────────

func installDaemon() error {
	cfg := config.Load()
	if cfg == nil {
		return fmt.Errorf("config not found — run setup first")
	}

	binPath := cfg.InstallPath
	if binPath == "" {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("resolve executable: %w", err)
		}
		binPath = exe
	}

	// The dictate command reads from config automatically.
	// We only pass --verbose for daemon logging.
	args := []string{"dictate", "--verbose"}

	switch runtime.GOOS {
	case "linux":
		return installSystemdService(binPath, args)
	case "darwin":
		return installLaunchdPlist(binPath, args)
	case "windows":
		return installWindowsTask(binPath, args)
	default:
		return fmt.Errorf("daemon not supported on %s", runtime.GOOS)
	}
}

func removeDaemon() error {
	switch runtime.GOOS {
	case "linux":
		return removeSystemdService()
	case "darwin":
		return removeLaunchdPlist()
	case "windows":
		return removeWindowsTask()
	default:
		return fmt.Errorf("daemon not supported on %s", runtime.GOOS)
	}
}

// isDaemonRunning checks if the dictate daemon is currently active.
func isDaemonRunning() bool {
	switch runtime.GOOS {
	case "linux":
		out, err := exec.Command("systemctl", "--user", "is-active", "gleann-sound-dictate.service").Output()
		return err == nil && strings.TrimSpace(string(out)) == "active"
	case "darwin":
		err := exec.Command("launchctl", "list", "com.gleann.sound.dictate").Run()
		return err == nil
	case "windows":
		err := exec.Command("schtasks", "/Query", "/TN", "gleann-sound-dictate").Run()
		return err == nil
	}
	return false
}

// ── Linux: systemd user service ────────────────────────────────

func installSystemdService(binPath string, args []string) error {
	serviceDir := expandPath("~/.config/systemd/user")
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		return fmt.Errorf("create systemd dir: %w", err)
	}

	execStart := binPath
	for _, a := range args {
		execStart += " " + a
	}

	// Capture current session environment for X11 display access.
	// Without these, robotgo (which uses X11/XTest) will SIGSEGV.
	display := os.Getenv("DISPLAY")
	if display == "" {
		display = ":0"
	}
	xauth := os.Getenv("XAUTHORITY")
	xdgRuntime := os.Getenv("XDG_RUNTIME_DIR")

	var envLines string
	envLines += fmt.Sprintf("Environment=DISPLAY=%s\n", display)
	if xauth != "" {
		envLines += fmt.Sprintf("Environment=XAUTHORITY=%s\n", xauth)
	}
	if xdgRuntime != "" {
		envLines += fmt.Sprintf("Environment=XDG_RUNTIME_DIR=%s\n", xdgRuntime)
	}

	unit := fmt.Sprintf(`[Unit]
Description=gleann-sound dictation daemon
After=graphical-session.target
PartOf=graphical-session.target

[Service]
Type=simple
ExecStart=%s
Restart=on-failure
RestartSec=5
%s
[Install]
WantedBy=default.target
`, execStart, envLines)

	servicePath := filepath.Join(serviceDir, "gleann-sound-dictate.service")
	if err := os.WriteFile(servicePath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write service file: %w", err)
	}

	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	if err := exec.Command("systemctl", "--user", "enable", "gleann-sound-dictate.service").Run(); err != nil {
		return fmt.Errorf("enable service: %w", err)
	}
	// Stop first so that "start" actually launches the new binary.
	// Without this, "start" is a no-op on an already-active service.
	_ = exec.Command("systemctl", "--user", "stop", "gleann-sound-dictate.service").Run()
	if err := exec.Command("systemctl", "--user", "start", "gleann-sound-dictate.service").Run(); err != nil {
		return fmt.Errorf("start service: %w", err)
	}

	return nil
}

func removeSystemdService() error {
	_ = exec.Command("systemctl", "--user", "stop", "gleann-sound-dictate.service").Run()
	_ = exec.Command("systemctl", "--user", "disable", "gleann-sound-dictate.service").Run()

	servicePath := expandPath("~/.config/systemd/user/gleann-sound-dictate.service")
	if err := os.Remove(servicePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove service file: %w", err)
	}

	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	return nil
}

// ── macOS: launchd ─────────────────────────────────────────────

func installLaunchdPlist(binPath string, args []string) error {
	agentsDir := expandPath("~/Library/LaunchAgents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}

	allArgs := append([]string{binPath}, args...)
	var progArgs string
	for _, a := range allArgs {
		progArgs += fmt.Sprintf("\t\t<string>%s</string>\n", a)
	}

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.gleann.sound.dictate</string>
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
	<string>/tmp/gleann-sound-dictate.log</string>
	<key>StandardErrorPath</key>
	<string>/tmp/gleann-sound-dictate.err</string>
</dict>
</plist>`, progArgs)

	plistPath := filepath.Join(agentsDir, "com.gleann.sound.dictate.plist")
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}

	if err := exec.Command("launchctl", "load", plistPath).Run(); err != nil {
		return fmt.Errorf("load plist: %w", err)
	}

	return nil
}

func removeLaunchdPlist() error {
	plistPath := expandPath("~/Library/LaunchAgents/com.gleann.sound.dictate.plist")
	_ = exec.Command("launchctl", "unload", plistPath).Run()

	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove plist: %w", err)
	}
	return nil
}

// ── Windows: Task Scheduler ────────────────────────────────────

func installWindowsTask(binPath string, args []string) error {
	fullCmd := binPath
	for _, a := range args {
		fullCmd += " " + a
	}

	cmd := exec.Command("schtasks", "/Create",
		"/TN", "gleann-sound-dictate",
		"/TR", fullCmd,
		"/SC", "ONLOGON",
		"/RL", "LIMITED",
		"/F")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("create scheduled task: %w", err)
	}

	_ = exec.Command("schtasks", "/Run", "/TN", "gleann-sound-dictate").Run()
	return nil
}

func removeWindowsTask() error {
	_ = exec.Command("schtasks", "/End", "/TN", "gleann-sound-dictate").Run()
	cmd := exec.Command("schtasks", "/Delete", "/TN", "gleann-sound-dictate", "/F")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("delete scheduled task: %w", err)
	}
	return nil
}

// ── Completion scripts ─────────────────────────────────────────

func bashCompletion() string {
	return `# gleann-sound bash completion
_gleann_sound() {
    local cur prev commands
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"
    commands="transcribe listen serve dictate tui help"

    case "${prev}" in
        gleann-sound)
            COMPREPLY=( $(compgen -W "${commands}" -- "${cur}") )
            return 0
            ;;
        transcribe)
            COMPREPLY=( $(compgen -W "--model --file --language --verbose --help" -- "${cur}") )
            return 0
            ;;
        listen)
            COMPREPLY=( $(compgen -W "--model --language --verbose --help" -- "${cur}") )
            return 0
            ;;
        serve)
            COMPREPLY=( $(compgen -W "--model --addr --language --verbose --help" -- "${cur}") )
            return 0
            ;;
        dictate)
            COMPREPLY=( $(compgen -W "--model --key --language --verbose --help" -- "${cur}") )
            return 0
            ;;
        --model|--file)
            COMPREPLY=( $(compgen -f -- "${cur}") )
            return 0
            ;;
    esac

    if [[ "${cur}" == -* ]]; then
        COMPREPLY=( $(compgen -W "--model --verbose --help" -- "${cur}") )
    fi
}
complete -F _gleann_sound gleann-sound
`
}

func zshCompletion() string {
	return `#compdef gleann-sound
# gleann-sound zsh completion

_gleann_sound() {
    local -a commands
    commands=(
        'transcribe:Transcribe an audio file to text'
        'listen:Continuous real-time transcription from microphone'
        'serve:Start gRPC server for gleann plugin'
        'dictate:Push-to-talk dictation mode with hotkey'
        'tui:Open interactive setup & configuration UI'
        'help:Help about any command'
    )

    _arguments -C \
        '--model[Path to whisper model]:file:_files' \
        '--verbose[Enable verbose logging]' \
        '--help[Show help]' \
        '1:command:->cmds' \
        '*::arg:->args'

    case "$state" in
        cmds)
            _describe -t commands 'gleann-sound command' commands
            ;;
        args)
            case "${words[1]}" in
                transcribe)
                    _arguments \
                        '--model[Path to whisper model]:file:_files' \
                        '--file[Audio file to transcribe]:file:_files' \
                        '--language[Whisper language code]:language:' \
                        '--verbose[Enable verbose logging]'
                    ;;
                listen)
                    _arguments \
                        '--model[Path to whisper model]:file:_files' \
                        '--language[Whisper language code]:language:' \
                        '--verbose[Enable verbose logging]'
                    ;;
                serve)
                    _arguments \
                        '--model[Path to whisper model]:file:_files' \
                        '--addr[gRPC listen address]:address:' \
                        '--language[Whisper language code]:language:' \
                        '--verbose[Enable verbose logging]'
                    ;;
                dictate)
                    _arguments \
                        '--model[Path to whisper model]:file:_files' \
                        '--key[Hotkey combination]:key:' \
                        '--language[Whisper language code]:language:' \
                        '--verbose[Enable verbose logging]'
                    ;;
            esac
            ;;
    esac
}

_gleann_sound "$@"
`
}

func fishCompletion() string {
	return `# gleann-sound fish completion
complete -c gleann-sound -n '__fish_use_subcommand' -a transcribe -d 'Transcribe an audio file to text'
complete -c gleann-sound -n '__fish_use_subcommand' -a listen -d 'Continuous real-time transcription'
complete -c gleann-sound -n '__fish_use_subcommand' -a serve -d 'Start gRPC server for gleann plugin'
complete -c gleann-sound -n '__fish_use_subcommand' -a dictate -d 'Push-to-talk dictation with hotkey'
complete -c gleann-sound -n '__fish_use_subcommand' -a tui -d 'Open interactive setup & config'
complete -c gleann-sound -n '__fish_use_subcommand' -a help -d 'Help about any command'

# Global flags
complete -c gleann-sound -l model -d 'Path to whisper model' -r -F
complete -c gleann-sound -l verbose -d 'Enable verbose logging'
complete -c gleann-sound -l help -d 'Show help'

# transcribe
complete -c gleann-sound -n '__fish_seen_subcommand_from transcribe' -l file -d 'Audio file to transcribe' -r -F
complete -c gleann-sound -n '__fish_seen_subcommand_from transcribe' -l language -d 'Whisper language code' -r

# listen
complete -c gleann-sound -n '__fish_seen_subcommand_from listen' -l language -d 'Whisper language code' -r

# serve
complete -c gleann-sound -n '__fish_seen_subcommand_from serve' -l addr -d 'gRPC listen address' -r

# dictate
complete -c gleann-sound -n '__fish_seen_subcommand_from dictate' -l key -d 'Hotkey combination' -r
complete -c gleann-sound -n '__fish_seen_subcommand_from dictate' -l language -d 'Whisper language code' -r
`
}

// ── Helpers ────────────────────────────────────────────────────

func expandPath(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}

// ── View ───────────────────────────────────────────────────────

func (m InstallModel) View() string {
	var b strings.Builder

	title := "Install"
	if m.uninstall {
		title = "Uninstall"
	}
	b.WriteString(TitleStyle.Render(fmt.Sprintf(" gleann-sound %s ", title)))
	b.WriteString("\n\n")

	switch m.phase {
	case installConfirm:
		b.WriteString(m.viewConfirm())
	case installDone:
		b.WriteString(m.viewDone())
	default:
		// installRunning handled synchronously — we skip to installDone.
	}

	return b.String()
}

func (m InstallModel) viewConfirm() string {
	var b strings.Builder
	verb := "install"
	if m.uninstall {
		verb = "uninstall"
	}
	b.WriteString(fmt.Sprintf("  Select components to %s:\n", verb))
	b.WriteString("  " + lipgloss.NewStyle().Foreground(ColorDimFg).Render("(space to toggle, enter to proceed)") + "\n\n")

	for i, opt := range m.options {
		cursor := "  "
		if i == m.cursor {
			cursor = "▸ "
		}

		check := "○"
		checkStyle := CheckboxUnchecked
		if opt.selected {
			check = "●"
			checkStyle = CheckboxChecked
		}

		label := opt.label
		desc := "  " + DescStyle.Render(opt.desc)

		if i == m.cursor {
			b.WriteString(ActiveItemStyle.Render(fmt.Sprintf("%s%s %s", cursor, checkStyle.Render(check), label)))
		} else {
			b.WriteString(NormalItemStyle.Render(fmt.Sprintf("%s%s %s", cursor, checkStyle.Render(check), label)))
		}
		b.WriteString(desc)
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(StatusBarStyle.Render("  ↑/↓ navigate • space toggle • enter proceed • esc cancel"))
	return b.String()
}

func (m InstallModel) viewDone() string {
	var b strings.Builder

	for _, line := range m.log {
		if strings.HasPrefix(line, "✓") {
			b.WriteString("  " + SuccessBadge.Render(line) + "\n")
		} else {
			b.WriteString("  " + ErrorBadge.Render(line) + "\n")
		}
	}

	if m.err != "" {
		b.WriteString("\n" + ErrorBadge.Render("  Error: "+m.err) + "\n")
	}

	b.WriteString("\n")
	b.WriteString(StatusBarStyle.Render("  Press enter or q to return"))
	return b.String()
}
