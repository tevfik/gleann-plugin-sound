# gleann-plugin-sound

[![CI](https://github.com/tevfik/gleann-plugin-sound/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/tevfik/gleann-plugin-sound/actions/workflows/ci.yml)
[![Release](https://github.com/tevfik/gleann-plugin-sound/actions/workflows/release.yml/badge.svg?event=push)](https://github.com/tevfik/gleann-plugin-sound/actions/workflows/release.yml)

Audio processing companion daemon/plugin for the [gleann](https://github.com/tevfik/gleann) vector database. Captures audio, runs local speech recognition ([whisper.cpp](https://github.com/ggerganov/whisper.cpp), [ONNX Runtime](https://onnxruntime.ai/) or the Nemotron 3.5 streaming model), and delivers transcriptions — as CLI output, injected keystrokes for voice dictation, or over HTTP/WebSocket.

All audio is processed **locally** — no cloud APIs required.

## Key Features

- **Four Backends** — `whisper` (whisper.cpp, CPU), `onnx` (Whisper on ONNX Runtime, CPU/GPU), `nemotron` (Nemotron 3.5 streaming, int4 ONNX) and `tc` (Nemotron 3.5 streaming, GGUF Q8 through the `gleann-asr-helper` sidecar — best live accuracy), selectable via `--backend`, `sound.json` or the TUI
- **Local Whisper Inference** — CGO-backed whisper.cpp, CPU-only (no GPU required)
- **ONNX Runtime Backend** — GPU-accelerated inference via CUDA/DirectML/CoreML when available
- **Execution Modes** — File transcription, live streaming, voice dictation, plugin server (HTTP + live WebSocket), speaker enrolment, interactive TUI, diagnostic test
- **Push-to-Talk Dictation** — Global hotkey captures speech and injects text as keystrokes
- **Async Pipeline** — Transcription runs in background; start a new recording immediately while previous one is being transcribed
- **Auto-Chunking** — Long recordings (>30 s) are split and transcribed in streaming fashion
- **Anti-Repetition** — Decoder loop prevention via max_tokens, entropy threshold, and pattern detection
- **Hallucination Filtering** — Multi-layer filtering: no_speech_prob, pattern matching, and pre-VAD silence detection
- **Quantized Models** — Q5/Q8 quantized models for 2-3× faster inference with minimal quality loss
- **Interactive TUI** — Setup wizard for model download, configuration, backend selection, daemon install, and diagnostics
- **Multilingual** — Supports 99+ languages via multilingual Whisper models
- **Energy-Based VAD** — Voice Activity Detection with EMA smoothing, skips silence automatically
- **Cross-Platform Audio** — MiniAudio (malgo) with PulseAudio + ALSA fallback / WASAPI / CoreAudio
- **Cross-Platform Hotkeys** — evdev on Linux (X11 + Wayland), Carbon on macOS, Win32 on Windows
- **Daemon Mode** — systemd (Linux), launchd (macOS), schtasks (Windows) auto-start at login
- **Config System** — Persistent settings in `~/.gleann/sound.json`, CLI flags as override/fallback
- **Shell Completions** — bash, zsh, and fish autocompletion
- **Stub Mode** — Build and develop without whisper.cpp installed (no-op transcriber)
- **File Output** — Transcribe and listen modes save JSONL output to file (default: `<input>.jsonl`)

## Architecture

```
┌──────────────────────────────────────────────────────────┐
│                CLI  (cobra)                               │
│  transcribe │ listen │ dictate │ tui │ test               │
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
├──────────────────────────────────────────────────────────┤
│  Config (~/.gleann/sound.json)                            │
│  TUI (bubbletea) │ HTTP Plugin (gleann build)             │
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
git clone https://github.com/tevfik/gleann-plugin-sound.git
cd gleann-plugin-sound

# Build whisper.cpp (CPU-only, ~2 min)
make whisper-setup

# Download a Whisper model
make whisper-model                         # default: base.en (~142 MB)
make whisper-model MODEL_SIZE=small-q5_1   # quantized, ~181 MB ★★
make whisper-model MODEL_SIZE=large-v3-turbo-q5_0  # best quantized, ~547 MB

# Build gleann-plugin-sound
make build

# Or build with ONNX Runtime support
make build-onnx

# Or build with both backends
make build-all
```

### 3. Interactive Setup (Recommended)

```bash
./build/gleann-plugin-sound tui
```

The TUI wizard guides you through:
1. **Preset** — *Recommended* (the most accurate setup measured, see
   [Meeting pipeline](#meeting-pipeline-live-refined-with-names)), *Lightweight* or *Custom*.
   A preset only pre-fills the next steps; each one can still be changed.
2. **Model Selection** — Choose and download models (Whisper, Nemotron, the Qwen3-ASR refiner, Silero VAD)
3. **Backend** — whisper, onnx, nemotron or tc
4. **Default Model** — Set the model used when `--model` is not specified
5. **Language** — Defaults to the system language (Türkçe/English); auto-detect is available
6. **Refine Model** — Live backends only: a second model checks each utterance (on, off, or your own GGUF)
7. **Hotkey** — Configure the push-to-talk hotkey for dictate mode
8. **Output Directory** — Set where transcription files are saved
9. **Save** — Configuration saved to `~/.gleann/sound.json` (settings the wizard does not edit, such as
   `corrections`, are kept)

The *Recommended* preset selects Nemotron 3.5 streaming Q8 (`tc` backend), Qwen3-ASR-1.7B Q8 as the
refiner and the system language: about 3 GB of downloads plus the `gleann-asr-helper` sidecar (from the
release, or `make asr-helper`). Measured on conversational Turkish it makes 17 % word errors against 39 %
for live words alone. Dictation then types the checked text after each pause instead of word by word.
An existing setup can switch it on from **Setup → Refine Model**, or with
`gleann-plugin-sound models download qwen3-asr-1.7b-q8-gguf` and `refine_model` in `sound.json`.

### 4. Setup Input Device Access (Linux)

For dictation mode, gleann-plugin-sound reads keyboard events via evdev. You need to be in the `input` group:

```bash
# Option A: Use the TUI installer
./build/gleann-plugin-sound tui
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
gleann-plugin-sound test
```

Tests: microphone capture → hotkey detection → speech recognition → keyboard injection.

### 6. Start Dictating

```bash
# With config (uses saved defaults from ~/.gleann/sound.json)
gleann-plugin-sound dictate

# With explicit flags (overrides config)
gleann-plugin-sound dictate --key "ctrl+shift+space" --language tr

# With a specific model and backend
gleann-plugin-sound dictate --backend whisper --model ~/.gleann/models/ggml-small-q5_1.bin --language tr

# As a background daemon (auto-starts at login)
gleann-plugin-sound daemon install
```

Hold the hotkey, speak, release. Run only one dictation process at a time (the
foreground command refuses to start while the daemon is running): stop the daemon
first with `gleann-plugin-sound daemon remove` or
`systemctl --user stop gleann-plugin-sound-dictate`.

## Linux arm64 (Raspberry Pi, AWS Graviton, Apple Silicon VMs)

The plugin runs natively on `linux/arm64`; releases ship `linux-arm64` archives for every
flavour (stub, whisper, onnx, whisper-onnx) and for `gleann-asr-helper`. Nothing is
x86-specific: ONNX Runtime is fetched as `onnxruntime-linux-aarch64`, and ggml uses NEON.

What the target machine needs:

| Component | Package / source |
|---|---|
| Microphone capture | ALSA or PulseAudio runtime (`libasound2`, `libpulse0`) |
| Typing into other apps (dictation) | X11 only: `libx11-6 libxtst6 libxinerama1` (Wayland: transcription and the HTTP server work, key injection does not) |
| `tc` backend (best live accuracy) | `gleann-asr-helper-<tag>-linux-arm64.tar.gz` from the release, next to the plugin or in `~/.local/bin`; needs `libopenblas0` |
| `onnx` / `nemotron` backends | nothing: ONNX Runtime is downloaded on first use |
| File decoding | `ffmpeg` |

To build on arm64 yourself, run the same commands as on x86 (`make build-all`,
`native/asr-helper/build.sh`); add `GGML_NATIVE=OFF` to the helper build if the binary must run
on other CPUs than the build machine.

## Execution Modes

| Mode | Command | Description |
|------|---------|-------------|
| **Transcribe** | `gleann-plugin-sound transcribe` | On-demand file transcription via ffmpeg → speech recognition |
| **Listen** | `gleann-plugin-sound listen` | Real-time transcription (mic / speaker), one JSON line per utterance; `--partials` for word-by-word |
| **Dictate** | `gleann-plugin-sound dictate` | Push-to-talk; words are typed as you speak (streaming backends `tc`, `nemotron`) |
| **Daemon** | `gleann-plugin-sound daemon install\|remove\|status` | Run dictation in the background from login |
| **Plugin Serve** | `gleann-plugin-sound plugin-serve` | HTTP server for `gleann index build` (`/convert`) plus the live WebSocket (`/live`) |
| **Install** | `gleann-plugin-sound install` | Register as a gleann plugin |
| **Speakers** | `gleann-plugin-sound speakers record\|enroll\|list\|remove\|test` | Enrol voices (record from the mic or from files) so meetings are transcribed with names |
| **Update** | `gleann-plugin-sound update [--check] [--force]` | Replace this binary with the latest GitHub release of the same build flavour |
| **Live client** | `gleann-plugin-sound live-client` | Stream a file to a `/live` endpoint (protocol test client) |
| **TUI** | `gleann-plugin-sound tui` | Interactive setup, install, daemon management, and diagnostics |
| **Test** | `gleann-plugin-sound test` | Diagnostic: mic, hotkey, speech recognition, keyboard |
| **Devices** | `gleann-plugin-sound devices` | List audio capture/playback devices |
| **Benchmark / Evaluate** | `gleann-plugin-sound benchmark`, `evaluate` | Real-time factor on a file; interactive WER/CER measurement |

Run `gleann-plugin-sound <command> --help` for the flags of each command.

## Trying it live

Each of these can be run on its own; none needs gleann itself.

**1. Microphone to terminal** (simplest)

```bash
gleann-plugin-sound listen --language tr           # one JSON line per utterance
gleann-plugin-sound listen --partials              # provisional lines while you speak
gleann-plugin-sound listen --source speaker        # what the computer is playing (meetings, videos)
```

**2. Phone or another machine over WebSocket**

```bash
# Server: a token is mandatory when listening beyond loopback
gleann-plugin-sound plugin-serve --host 0.0.0.0 --token "$(openssl rand -hex 16)" --language tr

# Client: streams a file at microphone speed and prints the events
gleann-plugin-sound live-client --url ws://<host>:8767 --token <token> meeting.mp3
```

Protocol details are in [Live WebSocket endpoint](#live-websocket-endpoint-phone--remote-microphone).

**3. Name the speakers** (optional)

```bash
gleann-plugin-sound speakers enroll Ayşe ayse-15s.wav
gleann-plugin-sound speakers list
```

**4. Push-to-talk dictation**

```bash
gleann-plugin-sound daemon install                 # background service, or:
gleann-plugin-sound dictate --key "ctrl+shift+space" --language tr
```

**5. Health check**

```bash
gleann-plugin-sound test
```

Each `/live` session starts its own sidecar (about 0.8 GB of RAM); at most `--max-live` (default 4) sessions run at once.

## Meeting pipeline: live, refined, with names

The recommended setup for Turkish (measured on 40 FLEURS Turkish clips, CPU only,
i9-13900H; WER = word error rate):

| Stage | Model | WER | Speed |
|-------|-------|-----|-------|
| Live (words ~1 s after speech) | Nemotron 3.5 streaming, Q8 GGUF (`--backend tc`) | 16.5 % | RTF 0.29 |
| Live, int4 ONNX (`--backend nemotron`) | same model, int4 | 19.2 % | RTF 0.17 |
| Refine (each finished utterance) | Qwen3-ASR-1.7B, Q8 GGUF (`--refine-model`) | **11.95 %** | RTF 0.41 |

Live text appears immediately and is never retracted; when an utterance ends, the
refiner re-transcribes it in the background and the better text arrives as a
`refined` event (WebSocket: `{"type":"refined"}`, `listen`: `"refined":true`).

```bash
# 1. Build the sidecar both models run in (transcribe.cpp; needs cmake, libopenblas-dev)
make asr-helper                      # installs ~/.local/bin/gleann-asr-helper

# 2. Models (Hugging Face: handy-computer/*-gguf)
cd ~/.gleann/models
curl -LO https://huggingface.co/handy-computer/nemotron-3.5-asr-streaming-0.6b-gguf/resolve/main/nemotron-3.5-asr-streaming-0.6b-Q8_0.gguf
curl -LO https://huggingface.co/handy-computer/Qwen3-ASR-1.7B-gguf/resolve/main/Qwen3-ASR-1.7B-Q8_0.gguf

# 3. Use them
gleann-plugin-sound listen --backend tc --model ~/.gleann/models/nemotron-3.5-asr-streaming-0.6b-Q8_0.gguf \
    --refine-model ~/.gleann/models/Qwen3-ASR-1.7B-Q8_0.gguf --language tr
```

`refine_model` (and `backend`/`default_model`) can live in `~/.gleann/sound.json`
instead of flags; `--refine-model=off` switches refinement off for one run. (`/live`'s
`lang` query parameter sets the live model's language per session; the refiner uses the
server's `--language`.) The sidecar is a separate process because whisper.cpp and
transcribe.cpp each bring their own statically linked ggml; it also isolates crashes.
Each `/live` session starts its own sidecar (~0.8 GB of RAM each).

### Accuracy on real speech

The FLEURS numbers above are read speech. To check conversational speech, four
TEDx Istanbul/Izmir/Hisar/Hacettepe talks in Turkish (92-99 s excerpts each, about 6.5 min
of speech in total) were scored against their human-written YouTube subtitles
(WER %, CPU only, 2026-10-21):

| Clip | Live words (`tc`, `/live`) | Live + refined (`/live`, Qwen) | Qwen offline | Whisper large-v3-turbo | Whisper small | Nemotron int4 |
|------|------|------|------|------|------|------|
| Talk 1 (clear) | 35.7 | 7.0 | 6.4 | 1.8 | 12.3 | 51.5 |
| Talk 2 (fast, jokes) | 63.6 | 32.6 | 32.2 | 26.0 | 72.9 | 76.4 |
| Talk 3 | 38.1 | 17.3 | 13.2 | 5.6 | 41.6 | 68.0 |
| Talk 4 (slow) | 20.2 | 11.8 | 9.2 | 5.9 | 16.0 | 22.7 |
| **Mean** | **39.4** | **17.2** | **15.3** | **9.8** | 35.7 | 54.7 |

What this means:

- Live words are fast but often wrong on spontaneous speech (about 40 % WER here,
  against 16.5 % on read FLEURS sentences). Dictation therefore defaults to
  **accurate mode** (the refined text) whenever `refine_model` is configured.
- Refining recovers most of the gap: 39 % → 17 %, close to running Qwen on the whole file.
- Whisper large-v3-turbo is the most accurate but needs about 1.3-2.3 s of CPU per
  second of audio, so it only suits offline transcription (`--backend whisper`), not live use.
- Changing the Nemotron look-ahead (`GLEANN_ASR_ATT_RIGHT` 0, 3, 6, 13) or the language
  setting did not help (all within noise or worse).
- Talk 2's reference is not verbatim (the subtitles drop words and reorder a sentence),
  so its absolute numbers are inflated for every system; compare columns, not values.
- Synthetic clips with rare words (for example a Turkish tongue-twister) can come out
  with odd spellings; this is the model's recognition, not a hard-coded string.

Reproduce: download audio and subtitles with `yt-dlp --write-subs --sub-langs tr`, cut an
excerpt aligned to whole subtitle cues, run `transcribe --file` or `live-client`, and
compute WER after lower-casing (Turkish `İ`/`I`) and removing punctuation.

### Who is speaking

Unsupervised diarization is unreliable when a TV or radio is on: every voice becomes a
"speaker" (4 labelled speakers, TV at 10 dB: 50-60 % diarization error with every
model tried). **Enrolled voices are robust to that**, so this plugin identifies people
it has been introduced to and leaves everyone else as `?` instead of inventing speakers:

```bash
gleann-plugin-sound speakers record Ayşe [--lang tr|en]    # shows a short text to read aloud, 25 s from the mic
gleann-plugin-sound speakers enroll Ayşe ayse-15s.wav      # or from a file: 15-30 s of that person only
gleann-plugin-sound speakers enroll Mehmet mehmet.wav
gleann-plugin-sound speakers list
gleann-plugin-sound speakers test meeting.wav              # who speaks when; --turns for change detection
```

Voice prints (TitaNet-small embeddings, `~/.gleann/models/nemo_en_titanet_small.onnx`)
are stored in `~/.gleann/speakers.json` (mode 0600). Once anyone is enrolled, `listen`,
`transcribe` and `plugin-serve` label every utterance (`"speaker":"Ayşe"`); `--speakers`
also numbers unenrolled voices ("Speaker 1…") when nobody is enrolled, `--speakers=false`
turns it off. Utterances in which people answer each other without a pause are split
at speaker changes before refining, so each person's words are transcribed and labelled
separately.

Measured by `speakers test --turns` on 4 real speakers spliced into a labelled 4-minute
meeting, with a Turkish news broadcast mixed in as the "TV" (share of speech time):

| Background | correct | unknown (`?`) | wrong name |
|------------|---------|---------------|------------|
| none | 95 % | 2 % | 3 % |
| TV 20 dB below the voices | 90 % | 8 % | 2 % |
| TV 10 dB below | 81 % | 16 % | 2 % |
| TV 3 dB below | 58 % | 37 % | 4 % |

Limits: an utterance that changes speaker without any pause is split on audio, not on
words, so a boundary can be off by a fraction of a second; overlapping speech is not
separated; the live (unrefined) line carries only the utterance's dominant speaker;
cosine threshold is `speaker_threshold` in `sound.json` (default 0.35; dictation uses the stricter `dictate_speaker_threshold`, see [Voice Dictation](#voice-dictation)).

### Transcribing recordings

```bash
# Silero VAD finds the speech, speaker turns are split, Qwen3-ASR transcribes each piece
gleann-plugin-sound transcribe --file meeting.mp3 --refine-model ~/.gleann/models/Qwen3-ASR-1.7B-Q8_0.gguf --language tr
```

`plugin-serve --refine-model ...` does the same for `gleann index build` (`/convert`),
with speaker names in the markdown (`[00:07 - 00:14] Ayşe: ...`). A one-hour recording
takes roughly 35 minutes on a laptop CPU.

### Voice activity detection

`listen`, `dictate` and `/live` use **Silero VAD v5** when
`~/.gleann/models/silero_vad.onnx` exists (`make silero-model`; override with
`GLEANN_SILERO_VAD`). The energy detector it replaces adapts to the loudest speaker and
silently drops quieter ones: in a four-speaker test it lost every utterance between
the first loud voice and the next.

## Plugin Integration (gleann build)

gleann-plugin-sound can act as a **document-extraction plugin** for the main [gleann](https://github.com/tevfik/gleann) application. When registered, `gleann build` automatically transcribes audio/video files and indexes them alongside your text documents.

### How it works

```
gleann build my-vault --docs ~/Documents/
    │
    ├── notes.md      → read directly, chunk, embed
    ├── report.pdf    → gleann-docs plugin extracts text
    └── meeting.mp3   → gleann-plugin-sound plugin transcribes audio
            │
            ├─ 1. gleann POSTs the file to http://localhost:8767/convert
            ├─ 2. gleann-plugin-sound saves to temp, decodes via ffmpeg
            ├─ 3. Whisper inference → timestamped segments
            ├─ 4. Segments formatted as markdown:
            │     "# Transcription: meeting.mp3
            │      [00:00 - 00:15] First segment text.
            │      [00:15 - 00:32] Second segment text."
            ├─ 5. Returns {"markdown": "..."} as JSON
            └─ 6. gleann chunks the markdown and indexes it
```

No files are created on disk — all data flows in-memory over HTTP. The temp file for ffmpeg is deleted immediately after transcription.

### Registration and capabilities

The plugin advertises `audio-transcription`, which is how current gleann routes
audio (`GLEANN_AUDIO_PLUGIN_URL`, used together with `--multimodal-model`), and
`document-extraction` for older gleann versions. Files without speech get HTTP 422
so gleann skips them. Run the server (`plugin-serve`) before `gleann index build`;
current gleann does not start audio plugins by itself.

```bash
gleann-plugin-sound plugin-serve --language auto &      # language "auto" detects per recording
gleann index build my-media --docs ./recordings --multimodal-model <any-model>
```

gleann caches multimodal results by file content, model and language — not by
plugin. After switching ASR backend or model, set `GLEANN_MULTIMODAL_CACHE=off`
(or clear `~/.gleann/cache/multimodal`) to re-transcribe.

### Supported formats

`.mp3`, `.wav`, `.m4a`, `.flac`, `.ogg`, `.opus`, `.aac`, `.wma`, `.webm`, `.mp4`, `.mkv`, `.avi`

Requires **ffmpeg** on `$PATH` for audio decoding. `/convert` hands the file to ffmpeg whatever its extension, so any other container ffmpeg can read (for example `.mov`) works too, but only the extensions above are registered with gleann. Files ffmpeg cannot decode (empty, corrupt, not audio, a video without an audio track) get HTTP 422 with ffmpeg's message; a silent file gets 422 `no speech detected`.

### Quick setup

```bash
# 1. Build (if not already done)
make build

# 2. Register as a gleann plugin
./build/gleann-plugin-sound install

# 3. Done! gleann build now transcribes audio files automatically
gleann build my-vault --docs ~/Documents/
```

### What `install` does

The `install` command writes an entry to `~/.gleann/plugins.json`:

```json
{
  "name": "gleann-plugin-sound",
  "url": "http://localhost:8767",
  "command": ["/path/to/gleann-plugin-sound", "plugin-serve", "--port", "8767",
              "--model", "/home/user/.gleann/models/ggml-small-q5_1.bin",
              "--backend", "whisper", "--language", "tr"],
  "capabilities": ["document-extraction"],
  "extensions": [".mp3", ".wav", ".m4a", ".flac", ".ogg", ".webm", ".mp4", ".mkv", ".avi"]
}
```

- Binary path is resolved automatically via `os.Executable()`
- Model, backend, and language are read from `~/.gleann/sound.json` (set via `gleann-plugin-sound tui`)
- If the plugin is offline when gleann needs it, the PluginManager auto-starts it using the stored command

### `plugin-serve` command

Runs the HTTP server that gleann communicates with:

```bash
# Uses defaults from config
gleann-plugin-sound plugin-serve

# Override port or language
gleann-plugin-sound plugin-serve --port 8767 --language tr
```

The model is loaded **lazily** on the first `/convert` request, so the server starts instantly and health checks pass immediately.

### Notes

- **Timeout**: gleann's PluginManager uses a configurable HTTP timeout per file (default: 120 seconds). Plugins can specify their own timeout in `plugins.json`. For very long files, use `gleann-plugin-sound transcribe --file` separately.
- **Concurrency**: The Whisper engine is not thread-safe. Concurrent requests from gleann's parallel workers are serialised via mutex.

## Usage

### File Transcription

Transcribe any audio/video file to timestamped JSONL (requires ffmpeg):

```bash
gleann-plugin-sound transcribe --file recording.mp3 --model models/ggml-base.en.bin
```

Output is written to both stdout and a file (default: `<input>.jsonl`):
```bash
# Custom output path
gleann-plugin-sound transcribe --file recording.mp3 -o result.jsonl

# Using ONNX backend
gleann-plugin-sound transcribe --file recording.mp3 --backend onnx --model models/whisper-base.en-onnx/
```

Output:
```json
{"start":"0s","end":"3.5s","text":"Hello, this is a test recording."}
{"start":"3.5s","end":"7.2s","text":"It demonstrates file transcription."}
```

### Live Streaming

```bash
# Real time: one JSON line per utterance (nemotron backend)
gleann-plugin-sound listen --language tr

# Word-by-word: provisional lines ("partial":true) while you speak
gleann-plugin-sound listen --partials

# Transcribe what the computer is playing (meetings, videos)
gleann-plugin-sound listen --source speaker

# Save output to file (also prints to stdout)
gleann-plugin-sound listen -o transcription.jsonl
```

**How real time works.** Nemotron is a cache-aware streaming model, so it is fed
audio continuously in 0.56 s chunks and keeps its state between them. There is
no re-decoding of overlapping windows. A word is released once it can no longer
change: expect it about one second after it is spoken (0.56 s chunk + one word of
hold-back), and an utterance closes after a 0.7 s pause. While it is quiet nothing
reaches the model. On a laptop CPU the real-time factor is ~0.15–0.25.

`dictate` uses the same engine: hold the hotkey and the words are typed as you
speak, and the last ones appear right after you release it.

Other backends (whisper, onnx) still use the older sliding-window pipeline.

### Live WebSocket endpoint (phone / remote microphone)

`plugin-serve` also exposes `GET /live`, a WebSocket for streaming audio from
another device (a phone app, a browser, a meeting recorder) and getting words back
in real time. Requires the nemotron backend.

```bash
# Accept connections from the network; a token is mandatory when not on loopback
gleann-plugin-sound plugin-serve --host 0.0.0.0 --token "$(openssl rand -hex 16)" --language auto

# Test from any machine (streams a file at microphone speed, prints the events)
gleann-plugin-sound live-client --url ws://my-host:8767 --token <token> meeting.mp3
```

Protocol (`ws://host:8767/live?token=...&lang=tr`):

| Direction | Message |
|-----------|---------|
| client → server | binary frames: raw PCM, signed 16-bit little-endian, **mono, 16 000 Hz** |
| client → server | `{"type":"end"}` — no more audio; the last words are flushed |
| server → client | `{"type":"ready","sample_rate":16000,"format":"pcm_s16le"}` |
| server → client | `{"type":"partial","seq":0,"text":"...","start":1.2,"end":3.4}` — the utterance so far; replaces the previous partial |
| server → client | `{"type":"final","seq":0,"text":"...","start":1.2,"end":5.0}` — the utterance ended |
| server → client | `{"type":"done"}` then a normal close; `{"type":"error","message":"..."}` on failure |

Options: `lang` (default: the server's language, `auto` detects), `partials=0` to
drop partial events, `Authorization: Bearer <token>` instead of `token=`. Times
are seconds on the session's audio clock. Up to `--max-live` (default 4) sessions
share one loaded model; capture at 16 kHz on the device (Android `AudioRecord`
and iOS `AVAudioEngine` can do this natively). The connection is plain `ws://`;
put a TLS proxy in front if it crosses an untrusted network.

### Voice Dictation

Push-to-talk dictation — hold the hotkey to speak, release to transcribe and inject as keystrokes:

```bash
gleann-plugin-sound dictate --key "ctrl+shift+space" --language tr
```

With a streaming backend (`tc`, `nemotron`) there are two typing modes:

- **Accurate (default when `refine_model` is set in `sound.json`, or with `--refine-model <gguf>`)** —
  after each pause the utterance is re-transcribed by Qwen3-ASR and that text is typed.
  Text appears roughly `0.7 s + 0.4 × (length of the utterance)` after you stop speaking,
  in whole phrases. On real talks this is several times more accurate than live words
  (see [Accuracy on real speech](#accuracy-on-real-speech)).
- **Live (`--refine-model=off`)** — words are typed about a second after you say them and
  cannot be corrected afterwards.

With `whisper` or `onnx` text is typed in chunks and when you release the key.
Only one dictation process may run at a time.

**Only my voice** (streaming backends; TUI → Speakers → "Dictate: only my voice"): dictation types only
utterances that match an enrolled voice, after each pause. Each utterance needs a score of
at least `dictate_speaker_threshold` (default 0.45; the "Match threshold" row in the Speakers menu changes it with ←/→
in 0.05 steps, enter resets it, and the daemon restarts). Utterances too short to judge (under 1 s of audio
including the pause) are not typed. Against 30 FLEURS Turkish speakers, other voices
scored at most 0.38–0.42 on 1–3 s pieces, while an enrolled user on a laptop microphone
scored 0.45–0.54. The daemon log shows every decision with its score
(`[dictate] voice: … (score …)` / `ignored …`). Use it to tune the threshold for your
room.

**Async pipeline** (whisper/onnx): Transcription and injection run in the background. You can press the hotkey again immediately while the previous recording is still being transcribed.

**Auto-chunking**: Recordings longer than 30 seconds are automatically split and transcribed in streaming fashion — no need to release the key for long dictations.

Supported modifier keys: `ctrl`, `alt`, `shift`, `super`/`win`/`cmd`
Supported trigger keys: `space`, `a-z`, `0-9`, `f1-f12`

### Diagnostic Test

Run a quick check of all components:

```bash
gleann-plugin-sound test --key "ctrl+shift+space" --model ~/.gleann/models/ggml-base.bin
```

Tests:
1. **Microphone** — 3-second recording, shows peak audio level
2. **Hotkey** — Waits for press/release within 10 seconds
3. **Speech recognition** — Transcribes the microphone recording with the configured backend
4. **Keyboard** — Injects a test string as keystrokes

### Background dictation service

```bash
gleann-plugin-sound daemon install   # systemd user service / launchd / scheduled task
gleann-plugin-sound daemon status
gleann-plugin-sound daemon remove
```

Run `install` from the installed copy (e.g. `~/.local/bin`): the service is written
for the binary that is running. It starts at login and holds the hotkey from
`~/.gleann/sound.json`; logs: `journalctl --user -u gleann-plugin-sound-dictate -f`.

### Interactive TUI

```bash
gleann-plugin-sound tui
```

The TUI provides these screens:
- **Setup** — Download models (full + quantized), choose the backend (`whisper`, `onnx`, `nemotron`, `tc`), language, hotkey, audio source, output directory, and default model
- **Install** — Copy binary to `~/.local/bin`, install shell completions, setup input group, install daemon
- **Uninstall** — Remove binary, daemon, completions, config, and downloaded models
- **Dictate**, **Listen**, **Serve** (`plugin-serve`), **Test** — Launch those commands with your saved settings
- **Speakers** — List enrolled voices; `a` records a new one from the microphone (25 s while you read a short text shown on screen; `tab` switches it between Türkçe and English), `enter` re-records the selected voice, `d` forgets it
- **Update** — Runs `gleann-plugin-sound update`

From gleann: `gleann tui` → **Plugins** → *gleann-plugin-sound* → `c` opens this TUI (it runs `gleann-plugin-sound tui`, found on `PATH`).

### Settings in the gleann Web UI

gleann's Web UI (**System → Plugins → Configure**) edits the same settings as the TUI's Setup:
language, backend, model, refine model, ONNX provider, audio source, hotkey, output directory and
speaker threshold. `plugin-serve` describes the form itself (`GET /settings`) and saves it
(`PUT /settings`, all-or-nothing validation, written to `~/.gleann/sound.json`); gleann starts the
plugin if it is not running and proxies both calls as `/api/plugins/gleann-plugin-sound/settings`.
Changes apply after restarting `plugin-serve` and the dictate service. Off loopback the
`--token` is required for `/settings` too. Recording voices still needs the terminal or TUI (browsers
cannot reach the machine's microphone through this plugin).

### Updating

```bash
gleann-plugin-sound update --check    # is there a newer release?
gleann-plugin-sound update            # download, verify against checksums.txt, replace this binary
```

The update installs the release asset of the *same build flavour* as the running binary
(`stub`, `whisper`, `onnx` or `whisper-onnx`, as reported by `update --check`) and never
substitutes another one, so a `make build-all` binary keeps both backends. When a release
has no asset for your flavour the command stops and says so: rebuild with `make build-all`.
Models, `~/.gleann/sound.json`, enrolled voices and the `gleann-asr-helper` sidecar
(`make asr-helper`) are not touched; restart `plugin-serve` and the dictate daemon afterwards.
`gleann plugin update` chooses an asset by its own rule (it prefers any `-onnx-` build) and
does not know about build flavours, so use `gleann-plugin-sound update` for a `tc`/Qwen setup.

## Daemon Management

gleann-plugin-sound can run as a background daemon that starts automatically at login:

```bash
# Install via TUI
gleann-plugin-sound tui → Install → "Start dictate daemon at login"

# Check status (Linux)
systemctl --user status gleann-plugin-sound-dictate.service

# View logs
journalctl --user -u gleann-plugin-sound-dictate.service -f

# Manual control
systemctl --user stop gleann-plugin-sound-dictate.service
systemctl --user start gleann-plugin-sound-dictate.service
systemctl --user restart gleann-plugin-sound-dictate.service
```

| OS | Backend | Service Location |
|----|---------|-----------------|
| Linux | systemd user service | `~/.config/systemd/user/gleann-plugin-sound-dictate.service` |
| macOS | launchd agent | `~/Library/LaunchAgents/com.gleann.sound.dictate.plist` |
| Windows | Scheduled Task | `gleann-plugin-sound-dictate` task |

The daemon reads all settings from `~/.gleann/sound.json` — no command-line flags needed (except `--verbose` for debug logging).

## Configuration

Configuration is stored in `~/.gleann/sound.json` and created by the TUI setup wizard.

```json
{
  "default_model": "/home/user/.gleann/models/ggml-small-q5_1.bin",
  "language": "tr",
  "hotkey": "ctrl+shift+space",
  "backend": "whisper",
  "audio_source": "mic",
  "refine_model": "",
  "output_dir": "~/transcriptions",
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

Optional `"corrections"` rewrites words the model gets systematically wrong
(names, jargon). Matching is case-insensitive and keeps capitalisation and
punctuation; a key with spaces matches a phrase. It applies to `listen`,
`dictate`, `transcribe` and `plugin-serve`:

```json
"corrections": { "gılan": "gleann", "kal isiyor": "çalışıyor" }
```

**Priority**: CLI flags override config values. If no config exists, hardcoded defaults are used.

```bash
# Uses config defaults
gleann-plugin-sound dictate

# Override model from config
gleann-plugin-sound dictate --model /path/to/other-model.bin

# Override language
gleann-plugin-sound listen --language en
```

### Shell Completions

Install via the TUI installer (Install → Shell completions), or generate them yourself:

```bash
gleann-plugin-sound completion bash > ~/.local/share/bash-completion/completions/gleann-plugin-sound
gleann-plugin-sound completion zsh  > ~/.local/share/zsh/site-functions/_gleann-plugin-sound
gleann-plugin-sound completion fish > ~/.config/fish/completions/gleann-plugin-sound.fish
```

## ONNX Runtime Backend

The ONNX Runtime backend enables GPU-accelerated inference via CUDA, DirectML, or CoreML.

### CPU (Default — Automatic)

No manual setup needed. When you select the ONNX backend, the TUI setup wizard (or the engine at first run) automatically downloads the CPU-only ONNX Runtime library to `~/.gleann/lib/`.

```bash
# Build with ONNX support
make build-onnx

# The runtime library is downloaded automatically on first use
gleann-plugin-sound listen --backend onnx --model ~/.gleann/models/whisper-base-onnx/
```

### GPU — NVIDIA CUDA (Linux / Windows)

For GPU acceleration, install the CUDA-enabled ONNX Runtime via pip:

```bash
# Linux / Windows — CUDA 12.x
pip install onnxruntime-gpu
```

The TUI setup wizard will also offer to install this automatically when you select CUDA or Auto as the execution provider.

After installation, set the execution provider in your config or via the TUI:

```json
{
  "backend": "onnx",
  "execution_provider": "auto"
}
```

| Provider | Behaviour |
|----------|-----------|
| `auto` | Try CUDA first, fall back to CPU |
| `cuda` | Force CUDA — fails if not available |
| `cpu` | Force CPU-only inference |

### GPU — DirectML (Windows)

An alternative to CUDA on Windows, works with most GPUs (AMD, Intel, NVIDIA):

```bash
pip install onnxruntime-directml
```

### GPU — CoreML (macOS)

No separate package needed. The base ONNX Runtime already includes CoreML support on macOS. Select `auto` as the execution provider to use it.

### Custom Library Path

If you installed ONNX Runtime to a non-standard location, point to it via environment variable or `LD_LIBRARY_PATH`:

```bash
# Direct path to the shared library
export ORT_LIB_PATH=/path/to/libonnxruntime.so

# Or add the directory to LD_LIBRARY_PATH
export LD_LIBRARY_PATH=/path/to/onnxruntime/lib:$LD_LIBRARY_PATH
```

The library discovery order is:
1. `ORT_LIB_PATH` / `ORT_SHARED_LIBRARY_PATH` environment variables
2. `~/.gleann/lib/` (auto-downloaded or pip-installed)
3. System library paths (`/usr/lib/`, `/usr/local/lib/`)
4. `LD_LIBRARY_PATH` entries
5. Python site-packages

CUDA-capable builds (with `libonnxruntime_providers_cuda.so` alongside) are preferred over CPU-only builds.

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
./build/gleann-plugin-sound tui
# → Select "Install"
```

This will:
- Copy binary to `~/.local/bin/gleann-plugin-sound`
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
cp ./build/gleann-plugin-sound ~/.local/bin/

# Ensure ~/.local/bin is in PATH
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.bashrc
```

## Project Structure

```
gleann-plugin-sound/
├── cmd/gleann-plugin-sound/          # CLI entry point (cobra)
│   ├── main.go                # Root command, version, global flags, config loading
│   ├── transcribe.go          # Mode 1: file transcription (--output)
│   ├── listen.go              # Mode 2: live streaming (--output)
│   ├── dictate.go             # Mode 3: push-to-talk dictation (async)
│   ├── test.go                # Mode 5: diagnostic test command
│   ├── tui.go                 # Mode 6: interactive TUI
│   ├── plugin_serve.go        # HTTP server for gleann plugin integration
│   └── install.go             # Register as gleann plugin (~/.gleann/plugins.json)
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
│   │   ├── setup.go           # Setup wizard (models, language, hotkey, backend, output)
│   │   ├── install.go         # Install/Uninstall + daemon management
│   │   └── styles.go          # Lipgloss theme & ASCII logo
│   ├── httpserver/            # HTTP plugin server (gleann build integration)
│   │   └── server.go          # /health + /convert endpoints
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

Release archives are named `gleann-plugin-sound[-stub|-onnx|-whisper-onnx]-<tag>-<os>-<arch>.tar.gz`
(the plain name is the whisper build) and are accompanied by a `checksums.txt`; `update` relies on both.

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
go test ./internal/pipeline/ -v

# Lint (requires golangci-lint)
make lint

# Hardware diagnostic
./build/gleann-plugin-sound test
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
gleann-plugin-sound tui → Install → "Start dictate daemon at login"
```

Or manually check:
```bash
echo "DISPLAY=$DISPLAY"
echo "XAUTHORITY=$XAUTHORITY"
# Compare with the service file:
cat ~/.config/systemd/user/gleann-plugin-sound-dictate.service
```

### Dictation types nothing, wrong words, or every word twice

Work through these in order:

1. **Is one process running, and is it the one you expect?** Two dictation
   processes (the daemon plus a foreground `dictate`) both hear the hotkey and type
   every word twice. Current versions refuse to start a second one; check with
   `gleann-plugin-sound daemon status` and `pgrep -af "gleann-plugin-sound dictate"`.
2. **Is the right microphone the default?** `gleann-plugin-sound devices` marks the
   default capture device with `[DEFAULT]`. A laptop's digital microphone is far
   noisier than a headset; change the default in the system sound settings.
3. **Does recognition work without the keyboard path?** Run
   `gleann-plugin-sound listen --partials --language tr` and speak. If the text is
   right there, the problem is injection or the hotkey, not recognition.
4. **Is the language set?** With `--language tr` (or `language` in `sound.json`) the
   model is told what to expect; `auto` is less accurate on short phrases.
5. **Watch the log.** `journalctl --user -u gleann-plugin-sound-dictate -f`
   (daemon) or `gleann-plugin-sound dictate --verbose` (foreground) shows each key
   press, each typed chunk and any injection error.
6. **Keystroke injection needs an X11 session** (`DISPLAY` set). Under a pure Wayland
   session, injection into native Wayland windows does not work.
7. **Systematic misrecognitions** (names, jargon) can be fixed with the
   `corrections` map in `sound.json`.

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

**Symptom**: `gleann-plugin-sound test` works but daemon doesn't respond to hotkey.

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

**Fix**: Check your microphone with `gleann-plugin-sound test` and adjust PulseAudio volume:
```bash
pavucontrol  # GUI
# or
pactl set-source-volume @DEFAULT_SOURCE@ 150%
```

## License

MIT
