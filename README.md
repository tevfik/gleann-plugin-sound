# gleann-sound

[![CI](https://github.com/tevfik/gleann-sound/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/tevfik/gleann-sound/actions/workflows/ci.yml)
[![Release](https://github.com/tevfik/gleann-sound/actions/workflows/release.yml/badge.svg?event=push)](https://github.com/tevfik/gleann-sound/actions/workflows/release.yml)

Audio processing companion daemon/plugin for the [gleann](https://github.com/tevfik/gleann) vector database. Captures audio, runs local [whisper.cpp](https://github.com/ggerganov/whisper.cpp) or [ONNX Runtime](https://onnxruntime.ai/) inference, and delivers transcriptions — as CLI output, streaming gRPC events, or injected keystrokes for voice dictation.

All audio is processed **locally** — no cloud APIs required.

## Key Features

- **Dual Backend** — whisper.cpp (CPU, default) or ONNX Runtime (CPU/GPU), selectable via `--backend` flag or TUI
- **Local Whisper Inference** — CGO-backed whisper.cpp, CPU-only (no GPU required)
- **ONNX Runtime Backend** — GPU-accelerated inference via CUDA/DirectML/CoreML when available
- **6 Execution Modes** — File transcription, live streaming, gRPC daemon, voice dictation, interactive TUI, diagnostic test
- **Push-to-Talk Dictation** — Global hotkey captures speech and injects text as keystrokes
- **Async Pipeline** — Transcription runs in background; start a new recording immediately while previous one is being transcribed
- **Auto-Chunking** — Long recordings (>30 s) are split and transcribed in streaming fashion
- **Anti-Repetition** — Decoder loop prevention via max_tokens, entropy threshold, and pattern detection
- **Hallucination Filtering** — Multi-layer filtering: no_speech_prob, pattern matching, and pre-VAD silence detection
- **gRPC Alongside Dictation** — Optionally run gRPC server alongside dictation mode for gleann integration
- **Quantized Models** — Q5/Q8 quantized models for 2-3× faster inference with minimal quality loss
- **Interactive TUI** — Setup wizard for model download, configuration, backend selection, gRPC toggle, daemon install, and diagnostics
- **Multilingual** — Supports 99+ languages via multilingual Whisper models
- **Energy-Based VAD** — Voice Activity Detection with EMA smoothing, skips silence automatically
- **Cross-Platform Audio** — MiniAudio (malgo) with PulseAudio + ALSA fallback / WASAPI / CoreAudio
- **Cross-Platform Hotkeys** — evdev on Linux (X11 + Wayland), Carbon on macOS, Win32 on Windows
- **Daemon Mode** — systemd (Linux), launchd (macOS), schtasks (Windows) auto-start at login
- **gRPC Plugin** — Background daemon mode for integration with the main gleann application
- **Config System** — Persistent settings in `~/.gleann/sound.json`, CLI flags as override/fallback
- **Shell Completions** — bash, zsh, and fish autocompletion
- **Stub Mode** — Build and develop without whisper.cpp installed (no-op transcriber)
- **File Output** — Transcribe and listen modes save JSONL output to file (default: `<input>.jsonl`)

## Architecture

```
┌──────────────────────────────────────────────────────────┐
│                CLI  (cobra)                               │
│  transcribe │ listen │ serve │ dictate │ tui │ test       │
├─────────────┴────────┴───────┴─────────┴─────┴───────────┤
│                Backend Registry                           │
│  core.NewTranscriber(backend, model)                      │
├──────────────┬───────────────┬───────────────────────────┤
│  whisper.cpp │  ONNX Runtime │    malgo      │  robotgo  │
│  (CGO)       │  (onnxruntime)│  (MiniAudio)  │  (X11)    │
├──────────────┴───────────────┴───────────────┴───────────┤
│  Audio Pipeline                                           │
│  Capture → VAD → Resample → [Backend] → Output           │
├──────────────────────────────────────────────────────────┤
│  Whisper Safety Layers                                    │
│  max_tokens │ entropy_thold │ isRepetitive │ VAD          │
│  no_speech_prob │ hallucination patterns │ suppress       │
├──────────────────────────────────────────────────────────┤
│  Dictation Pipeline (async)                               │
│  Hotkey → Record → [auto-chunk] → Transcribe → Inject    │
│  Optional: + gRPC server (--addr)                         │
├──────────────────────────────────────────────────────────┤
│  Config (~/.gleann/sound.json)                            │
│  TUI (bubbletea) │ gRPC Plugin (go-plugin)                │
│  Daemon (systemd / launchd / schtasks)                    │
└──────────────────────────────────────────────────────────┘
```

## Quick Start

### 1. Install Prerequisites

```bash
# Ubuntu/Debian
sudo apt-get update
sudo apt-get install -y build-essential cmake git curl \
    libx11-dev libxtst-dev libxinerama-dev xdotool

# macOS
xcode-select --install
brew install cmake git curl
```

Requires **Go 1.24+** and **ffmpeg** (for file transcription mode).

### 2. Clone & Build

```bash
git clone https://github.com/tevfik/gleann-sound.git
cd gleann-sound

# Build whisper.cpp (CPU-only, ~2 min)
make whisper-setup

# Download a Whisper model
make whisper-model                         # default: base.en (~142 MB)
make whisper-model MODEL_SIZE=small-q5_1   # quantized, ~181 MB ★★
make whisper-model MODEL_SIZE=large-v3-turbo-q5_0  # best quantized, ~547 MB

# Build gleann-sound
make build

# Or build with ONNX Runtime support
make build-onnx

# Or build with both backends
make build-all
```

### 3. Interactive Setup (Recommended)

```bash
./build/gleann-sound tui
```

The TUI wizard guides you through:
1. **Model Selection** — Choose and download Whisper models (full + quantized)
2. **Default Model** — Set the model used when `--model` is not specified
3. **Language** — Set default language (or auto-detect)
4. **Hotkey** — Configure the push-to-talk hotkey for dictate mode
5. **gRPC Server** — Enable/disable gRPC server alongside dictation
6. **Backend** — Choose transcription backend (whisper or onnx)
7. **Output Directory** — Set where transcription files are saved
8. **Save** — Configuration saved to `~/.gleann/sound.json`

### 4. Setup Input Device Access (Linux)

For dictation mode, gleann-sound reads keyboard events via evdev. You need to be in the `input` group:

```bash
# Option A: Use the TUI installer
./build/gleann-sound tui
# → Select "Install" → Enable "Setup input group"

# Option B: Use make
make setup-input

# Option C: Manual
sudo usermod -aG input $USER
# Log out and back in to activate
```

### 5. Diagnose with Test Mode

Run the built-in diagnostic to verify all components work:

```bash
gleann-sound test
```

Tests: microphone capture → hotkey detection → whisper transcription → keyboard injection.

### 6. Start Dictating

```bash
# With config (uses saved defaults from ~/.gleann/sound.json)
gleann-sound dictate

# With explicit flags (overrides config)
gleann-sound dictate --key "ctrl+shift+space" --model ~/.gleann/models/ggml-small-q5_1.bin --language tr

# With gRPC server alongside dictation
gleann-sound dictate --key "ctrl+shift+space" --addr localhost:50051

# As a background daemon (auto-starts at login)
gleann-sound tui → Install → "Start dictate daemon at login"
```

## Execution Modes

| Mode | Command | Description |
|------|---------|-------------|
| **Transcribe** | `gleann-sound transcribe` | On-demand file transcription via ffmpeg → Whisper |
| **Listen** | `gleann-sound listen` | Live microphone streaming with VAD, JSON output |
| **Serve** | `gleann-sound serve` | Background gRPC daemon for gleann integration |
| **Dictate** | `gleann-sound dictate` | Push-to-talk with async transcription + keystroke injection |
| **Dictate+gRPC** | `gleann-sound dictate --addr :50051` | Dictation + gRPC server in same process |
| **TUI** | `gleann-sound tui` | Interactive setup, install, daemon management, and diagnostics |
| **Test** | `gleann-sound test` | Diagnostic: mic, hotkey, whisper, keyboard |

## Usage

### File Transcription

Transcribe any audio/video file to timestamped JSONL (requires ffmpeg):

```bash
gleann-sound transcribe --file recording.mp3 --model models/ggml-base.en.bin
```

Output is written to both stdout and a file (default: `<input>.jsonl`):
```bash
# Custom output path
gleann-sound transcribe --file recording.mp3 -o result.jsonl

# Using ONNX backend
gleann-sound transcribe --file recording.mp3 --backend onnx --model models/whisper-base.en-onnx/
```

Output:
```json
{"start":"0s","end":"3.5s","text":"Hello, this is a test recording."}
{"start":"3.5s","end":"7.2s","text":"It demonstrates file transcription."}
```

### Live Streaming

Stream live microphone transcription to stdout with VAD:

```bash
gleann-sound listen --model models/ggml-base.en.bin --language tr

# Save output to file (also prints to stdout)
gleann-sound listen --model models/ggml-base.en.bin -o transcription.jsonl
```

### gRPC Daemon

Run as a background daemon for gleann integration:

```bash
gleann-sound serve --model models/ggml-base.en.bin --addr localhost:50051
```

### Voice Dictation

Push-to-talk dictation — hold the hotkey to speak, release to transcribe and inject as keystrokes:

```bash
gleann-sound dictate --key "ctrl+shift+space" --model models/ggml-small-q5_1.bin --language tr
```

**With gRPC server**: Add `--addr` to run a gRPC server alongside dictation. This allows the main gleann application to connect over gRPC while dictation continues normally:

```bash
gleann-sound dictate --key "ctrl+shift+space" --addr localhost:50051
```

**Async pipeline**: Transcription and injection run in the background. You can press the hotkey again immediately while the previous recording is still being transcribed.

**Auto-chunking**: Recordings longer than 30 seconds are automatically split and transcribed in streaming fashion — no need to release the key for long dictations.

Supported modifier keys: `ctrl`, `alt`, `shift`, `super`/`win`/`cmd`
Supported trigger keys: `space`, `a-z`, `0-9`, `f1-f12`

### Diagnostic Test

Run a quick check of all components:

```bash
gleann-sound test --key "ctrl+shift+space" --model ~/.gleann/models/ggml-base.bin
```

Tests:
1. **Microphone** — 3-second recording, shows peak audio level
2. **Hotkey** — Waits for press/release within 10 seconds
3. **Whisper** — Transcribes the microphone recording
4. **Keyboard** — Injects a test string as keystrokes

### Interactive TUI

```bash
gleann-sound tui
```

The TUI provides these screens:
- **Setup** — Download models (full + quantized), configure language, hotkey, backend, output directory, gRPC server, and default model
- **Install** — Copy binary to `~/.local/bin`, install shell completions, setup input group, install daemon
- **Uninstall** — Remove binary, daemon, completions, config, and downloaded models
- **Dictate** — Launch dictation mode from the TUI
- **Serve** — Launch gRPC daemon mode from the TUI
- **Test** — Run the diagnostic test from the TUI

## Daemon Management

gleann-sound can run as a background daemon that starts automatically at login:

```bash
# Install via TUI
gleann-sound tui → Install → "Start dictate daemon at login"

# Check status (Linux)
systemctl --user status gleann-sound-dictate.service

# View logs
journalctl --user -u gleann-sound-dictate.service -f

# Manual control
systemctl --user stop gleann-sound-dictate.service
systemctl --user start gleann-sound-dictate.service
systemctl --user restart gleann-sound-dictate.service
```

| OS | Backend | Service Location |
|----|---------|-----------------|
| Linux | systemd user service | `~/.config/systemd/user/gleann-sound-dictate.service` |
| macOS | launchd agent | `~/Library/LaunchAgents/com.gleann.sound.dictate.plist` |
| Windows | Scheduled Task | `gleann-sound-dictate` task |

The daemon reads all settings from `~/.gleann/sound.json` — no command-line flags needed (except `--verbose` for debug logging).

## Configuration

Configuration is stored in `~/.gleann/sound.json` and created by the TUI setup wizard.

```json
{
  "default_model": "/home/user/.gleann/models/ggml-small-q5_1.bin",
  "language": "tr",
  "hotkey": "ctrl+shift+space",
  "backend": "whisper",
  "output_dir": "~/transcriptions",
  "grpc_addr": "localhost:50051",
  "models": [
    {
      "name": "small-q5_1",
      "path": "/home/user/.gleann/models/ggml-small-q5_1.bin",
      "size": "181 MB",
      "language": "multilingual"
    }
  ],
  "daemon_enabled": true,
  "completed": true
}
```

**Priority**: CLI flags override config values. If no config exists, hardcoded defaults are used.

```bash
# Uses config defaults
gleann-sound dictate

# Override model from config
gleann-sound dictate --model /path/to/other-model.bin

# Override language
gleann-sound listen --language en
```

### Shell Completions

Install via the TUI installer, or manually:

```bash
# The TUI "Install" screen handles bash, zsh, and fish automatically.
# Or use: gleann-sound tui → Install → Shell completions
```

## Available Whisper Models

### Full-Precision (f16)

| Model | Size | Language | Notes |
|-------|------|----------|-------|
| `tiny` | 75 MB | Multilingual | Fastest, lower accuracy |
| `tiny.en` | 75 MB | English only | Fastest for English |
| `base` | 142 MB | Multilingual | Fast, good for real-time |
| `base.en` | 142 MB | English only | Default for English |
| `small` | 466 MB | Multilingual | Good balance ★ |
| `small.en` | 466 MB | English only | Good for English |
| `medium` | 1.5 GB | Multilingual | High quality |
| `medium.en` | 1.5 GB | English only | High quality English |
| `large-v3-turbo` | 1.6 GB | Multilingual | Best quality |

### Quantized (Q5/Q8) — Recommended

Quantized models are **2-3× smaller** and **significantly faster** with minimal quality loss (< 1% WER degradation).

| Model | Size | vs Full | Language | Notes |
|-------|------|---------|----------|-------|
| `tiny-q5_1` | 31 MB | -59% | Multilingual | Fastest quantized |
| `tiny.en-q5_1` | 31 MB | -59% | English only | |
| `base-q5_1` | 57 MB | -60% | Multilingual | Fast quantized |
| `base.en-q5_1` | 57 MB | -60% | English only | |
| `small-q5_1` | 181 MB | -61% | Multilingual | Great balance ★★ |
| `small.en-q5_1` | 181 MB | -61% | English only | |
| `medium-q5_0` | 514 MB | -66% | Multilingual | Quality quantized |
| `medium.en-q5_0` | 514 MB | -66% | English only | |
| `large-v3-turbo-q5_0` | 547 MB | -66% | Multilingual | Best quantized ★★★ |
| `large-v3-turbo-q8_0` | 834 MB | -48% | Multilingual | Near-lossless |

**Recommendation**: Use `small-q5_1` (181 MB) for everyday multilingual dictation, `large-v3-turbo-q5_0` (547 MB) for best accuracy at 1/3 the size of the full model.

## System Install

### Via TUI (Recommended)

```bash
./build/gleann-sound tui
# → Select "Install"
```

This will:
- Copy binary to `~/.local/bin/gleann-sound`
- Install shell completions for bash, zsh, fish
- Setup udev rules and input group for keyboard access
- Optionally install daemon for auto-start at login

### Via Makefile

```bash
# Full install (requires sudo for udev/input group)
make install

# Input group only
make setup-input
```

### Manual Install

```bash
# Copy binary
cp ./build/gleann-sound ~/.local/bin/

# Ensure ~/.local/bin is in PATH
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.bashrc
```

## Project Structure

```
gleann-sound/
├── cmd/gleann-sound/          # CLI entry point (cobra)
│   ├── main.go                # Root command, version, global flags, config loading
│   ├── transcribe.go          # Mode 1: file transcription (--output)
│   ├── listen.go              # Mode 2: live streaming (--output)
│   ├── serve.go               # Mode 3: gRPC daemon
│   ├── dictate.go             # Mode 4: push-to-talk dictation (async, optional gRPC)
│   ├── test.go                # Mode 5: diagnostic test command
│   └── tui.go                 # Mode 6: interactive TUI
├── internal/
│   ├── config/                # Configuration management
│   │   └── config.go          # Load/Save, model catalog (full + quantized)
│   ├── core/                  # Domain interfaces & types
│   │   └── interfaces.go      # Transcriber, AudioCapturer, KeyboardInjector, Backend Registry
│   ├── audio/                 # Audio capture & processing
│   │   ├── capture.go         # MalgoCapturer (PulseAudio + ALSA fallback)
│   │   ├── vad.go             # Energy-based Voice Activity Detection
│   │   └── resample.go        # PCM format conversion utilities
│   ├── hotkey/                # Cross-platform hotkey detection
│   │   ├── hotkey.go          # Types (Modifier, Key, Hotkey)
│   │   ├── hotkey_linux.go    # evdev backend (X11 + Wayland)
│   │   ├── hotkey_darwin.go   # Carbon wrapper
│   │   └── hotkey_windows.go  # Win32 RegisterHotKey wrapper
│   ├── whisper/               # whisper.cpp transcription backend
│   │   ├── engine.go          # CGO whisper.cpp (build tag: whisper)
│   │   └── stub.go            # No-op stub (default, no CGO needed)
│   ├── onnx/                  # ONNX Runtime transcription backend
│   │   ├── engine.go          # ONNX Runtime engine (build tag: onnx)
│   │   ├── tokenizer.go       # HuggingFace tokenizer.json loader
│   │   └── stub.go            # No-op stub (default, no ONNX needed)
│   ├── keyboard/              # Keystroke injection
│   │   └── inject.go          # RobotGoInjector with X11 display check
│   ├── tui/                   # Interactive terminal UI
│   │   ├── tui.go             # Multi-screen orchestrator
│   │   ├── home.go            # Home menu (Setup/Dictate/Serve/Test/Install…)
│   │   ├── setup.go           # Setup wizard (models, language, hotkey, backend, output, gRPC)
│   │   ├── install.go         # Install/Uninstall + daemon management
│   │   └── styles.go          # Lipgloss theme & ASCII logo
│   └── plugin/                # gRPC server
│       └── grpc_server.go     # Plugin server for gleann integration
├── .github/workflows/
│   ├── ci.yml                 # Test + build on push/PR (stub + whisper + onnx)
│   └── release.yml            # Tag-triggered release builds
├── Makefile                   # Build targets
├── go.mod
├── LICENSE                    # MIT
└── README.md
```

## Build Tags

| Tag | Effect | Use Case |
|-----|--------|----------|
| `whisper` | Links whisper.cpp via CGO | Production builds (CPU) |
| `onnx` | Links ONNX Runtime | Production builds (CPU/GPU) |
| `whisper,onnx` | Both backends available | Full-featured builds |
| _(none)_ | Uses stub transcribers | Development, CI, testing |

```bash
# Full build with whisper.cpp (default)
make build          # equivalent to: go build -tags whisper ...

# ONNX Runtime only
make build-onnx     # equivalent to: go build -tags onnx ...

# Both backends
make build-all      # equivalent to: go build -tags "whisper,onnx" ...

# Stub build (no whisper.cpp, no ONNX)
make build-stub     # equivalent to: go build ...
```

## Testing

```bash
# Run all tests (uses stub transcriber, no whisper.cpp needed)
make test

# Tests with coverage report
make test-cover

# Specific packages
go test ./internal/config/ -v
go test ./internal/tui/ -v
go test ./internal/audio/ -v
go test ./internal/hotkey/ -v
go test ./internal/plugin/ -v

# Lint (requires golangci-lint)
make lint

# Hardware diagnostic
./build/gleann-sound test
```

## Tech Stack

| Component | Library | Purpose |
|-----------|---------|---------|
| Audio Capture | [malgo](https://github.com/gen2brain/malgo) (MiniAudio) | Cross-platform audio (PulseAudio + ALSA + WASAPI + CoreAudio) |
| Transcription | [whisper.cpp](https://github.com/ggerganov/whisper.cpp) (CGO) | Local speech-to-text inference (CPU) |
| Transcription | [ONNX Runtime](https://onnxruntime.ai/) | Local speech-to-text inference (CPU/GPU) |
| Hotkeys | Custom `internal/hotkey` | evdev (Linux), Carbon (macOS), Win32 (Windows) |
| Keystrokes | [robotgo](https://github.com/go-vgo/robotgo) | Simulated keyboard input (X11/WASAPI) |
| CLI | [cobra](https://github.com/spf13/cobra) | Command-line framework |
| TUI | [bubbletea](https://github.com/charmbracelet/bubbletea) + [lipgloss](https://github.com/charmbracelet/lipgloss) | Interactive terminal UI |
| RPC | [gRPC](https://grpc.io/) | Plugin communication with gleann |

## Makefile Targets

| Target | Description |
|--------|-------------|
| `make build` | Full build with whisper.cpp CGO |
| `make build-onnx` | Build with ONNX Runtime backend |
| `make build-all` | Build with whisper.cpp + ONNX Runtime |
| `make build-stub` | Stub build without whisper.cpp |
| `make whisper-setup` | Clone & build whisper.cpp (CPU-only) |
| `make whisper-model` | Download default GGML model (base.en) |
| `make onnx-model` | Download ONNX whisper model (base.en) |
| `make test` | Run all tests |
| `make test-cover` | Tests with coverage report |
| `make lint` | Run golangci-lint |
| `make clean` | Remove build artifacts |
| `make install` | Build + install to /usr/local/bin + udev + input group |
| `make setup-input` | Setup udev rules and input group only |
| `make run-dictate` | Build & run dictation mode |
| `make run-dictate-onnx` | Build & run dictation with ONNX backend |
| `make run-listen` | Build & run live streaming mode |
| `make run-transcribe FILE=x` | Build & run file transcription |

## Platform Support

| OS | Audio Backend | Hotkey Backend | Keystroke Backend |
|----|---------------|----------------|-------------------|
| Linux | PulseAudio / ALSA | evdev (X11 + Wayland) | X11 (robotgo) |
| macOS | CoreAudio | Carbon | CGo (Accessibility) |
| Windows | WASAPI | Win32 (RegisterHotKey) | Win32 (SendInput) |

## Troubleshooting

### Daemon crashes on keystroke injection

**Symptom**: `journalctl` shows `Could not open main display` followed by `SIGSEGV`.

**Cause**: The systemd service doesn't have the correct `DISPLAY` and `XAUTHORITY` environment variables.

**Fix**: Re-install the daemon via the TUI (it now captures your session's display variables):
```bash
gleann-sound tui → Install → "Start dictate daemon at login"
```

Or manually check:
```bash
echo "DISPLAY=$DISPLAY"
echo "XAUTHORITY=$XAUTHORITY"
# Compare with the service file:
cat ~/.config/systemd/user/gleann-sound-dictate.service
```

### Repeated/garbled dictation output

**Symptom**: Same text repeats many times (e.g. "bir amca yaparbir amca yapar...") or garbled characters.

**Cause**: Whisper decoder loop — the model gets stuck repeating a token sequence.

**Fix**: This is handled automatically by multiple safety layers:
- `max_tokens = 64` per segment (caps decoder output)
- `entropy_thold = 2.2` (detects low-entropy/repetitive tokens)
- `logprob_thold = -0.8` (rejects low-confidence segments)
- `isRepetitive()` pattern detection (catches 3+ repeated phrases)
- Pre-VAD silence detection (skips silent audio before whisper)

If you still see this, try a larger model (e.g. `small-q5_1` instead of `base`).

### Hallucinations on silence

**Symptom**: Whisper outputs "İzlediğiniz için teşekkür ederim" or "Thank you for watching" when no one is speaking.

**Cause**: Well-known whisper hallucination on silence/noise, common across many languages.

**Fix**: Handled automatically by:
- `no_speech_prob > 0.6` per-segment filtering
- Hallucination pattern list (Turkish, English, German common phrases)
- `suppress_nst = true` (suppress non-speech tokens)
- Pre-VAD energy check (`hasSpeech()`) before sending to whisper

### Hotkey not detected

**Symptom**: `gleann-sound test` works but daemon doesn't respond to hotkey.

**Fix**: Check if another application captures the same hotkey combo. On Ubuntu, IBus uses `Ctrl+Space` by default:
```bash
gsettings get org.freedesktop.ibus.general.hotkey trigger
# To disable: gsettings set org.freedesktop.ibus.general.hotkey trigger '[]'
```

Consider using a less common hotkey like `F9` or `Ctrl+F9`.

### No keyboard devices found

**Symptom**: `hotkey: cannot open any keyboard device`

**Fix**: Add yourself to the `input` group and re-login:
```bash
sudo usermod -aG input $USER
# Then log out and back in
```

### Audio level too low

**Symptom**: Transcription returns blank/silence.

**Fix**: Check your microphone with `gleann-sound test` and adjust PulseAudio volume:
```bash
pavucontrol  # GUI
# or
pactl set-source-volume @DEFAULT_SOURCE@ 150%
```

## License

MIT
