# gleann-plugin-sound Makefile
# Build targets for the gleann-plugin-sound binary with and without CGO dependencies.

BINARY     := gleann-plugin-sound
CMD_DIR    := ./cmd/gleann-plugin-sound
BUILD_DIR  := ./build
WHISPER_MODEL ?= models/ggml-base.en.bin

# Go toolchain
GO      := go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -s -w -X main.version=$(VERSION)
GOFLAGS := -trimpath -ldflags "$(LDFLAGS)"

# CGO flags for whisper.cpp — adjust WHISPER_DIR to your local path.
WHISPER_DIR ?= $(HOME)/whisper.cpp
CGO_CFLAGS  := -I$(WHISPER_DIR)/include -I$(WHISPER_DIR)/ggml/include
CGO_LDFLAGS := -L$(WHISPER_DIR)/build/src -L$(WHISPER_DIR)/build/ggml/src -Wl,-rpath,$(WHISPER_DIR)/build/src -Wl,-rpath,$(WHISPER_DIR)/build/ggml/src -Wl,-rpath,'$$ORIGIN' -lwhisper -lggml -lggml-cpu -lggml-base -lm -lstdc++ -lpthread -lgomp

.PHONY: asr-helper all build build-onnx build-all build-stub clean test lint run-dictate run-dictate-nemotron whisper-setup whisper-model onnx-model silero-model install install-user uninstall setup-input release

# ─── Sidecar for the "tc" backend and the Qwen3-ASR refiner ──────────
# Builds transcribe.cpp and the helper; see native/asr-helper/build.sh.
asr-helper:
	native/asr-helper/build.sh $(HOME)/.local/bin

# ─── Default ──────────────────────────────────────────────────────────
.PHONY: all build
all: build
build: whisper-build


# ─── Download, build, and install whisper.cpp (CPU-only) ──────────────
# This clones whisper.cpp into WHISPER_DIR and builds a static library.
# No GPU support — runs entirely on CPU.
whisper-setup:
	@echo "==> Setting up whisper.cpp (CPU-only) at $(WHISPER_DIR)..."
	@if [ ! -d "$(WHISPER_DIR)" ]; then \
		git clone --depth 1 https://github.com/ggerganov/whisper.cpp.git $(WHISPER_DIR); \
	else \
		echo "    whisper.cpp directory already exists, skipping clone."; \
	fi
	@mkdir -p $(WHISPER_DIR)/build
	cd $(WHISPER_DIR)/build && cmake .. \
		-DCMAKE_BUILD_TYPE=Release \
		-DGGML_CUDA=OFF \
		-DGGML_METAL=OFF \
		-DGGML_VULKAN=OFF \
		-DGGML_SYCL=OFF \
		-DGGML_BF16=OFF \
		-DWHISPER_BUILD_EXAMPLES=OFF \
		-DWHISPER_BUILD_TESTS=OFF \
		-DBUILD_SHARED_LIBS=OFF
	cd $(WHISPER_DIR)/build && cmake --build . --config Release -j$$(nproc)
	@echo "==> whisper.cpp built successfully at $(WHISPER_DIR)/build"

# ─── Download a Whisper GGML model ────────────────────────────────────
# Downloads the base.en model by default. Override MODEL_SIZE for others
# (tiny, tiny.en, base, base.en, small, small.en, medium, medium.en,
#  large-v3, large-v3-turbo).
MODEL_SIZE ?= base.en
whisper-model:
	@mkdir -p models
	@if [ ! -f "models/ggml-$(MODEL_SIZE).bin" ]; then \
		echo "==> Downloading ggml-$(MODEL_SIZE).bin..."; \
		if [ "$(MODEL_SIZE)" = "large-v3-turbo" ]; then \
			curl -L --progress-bar \
				"https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3-turbo.bin" \
				-o "models/ggml-$(MODEL_SIZE).bin"; \
		else \
			curl -L --progress-bar \
				"https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-$(MODEL_SIZE).bin" \
				-o "models/ggml-$(MODEL_SIZE).bin"; \
		fi; \
		echo "==> Model saved to models/ggml-$(MODEL_SIZE).bin"; \
	else \
		echo "    models/ggml-$(MODEL_SIZE).bin already exists, skipping."; \
	fi

# ─── Full build with whisper.cpp CGO ──────────────────────────────────
.PHONY: whisper-build
whisper-build:
	@echo "==> Building $(BINARY) with CGO (whisper.cpp)..."
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=1 \
	CGO_CFLAGS="$(CGO_CFLAGS)" \
	CGO_LDFLAGS="$(CGO_LDFLAGS)" \
	$(GO) build $(GOFLAGS) -tags whisper -o $(BUILD_DIR)/$(BINARY) $(CMD_DIR)
	@echo "==> Done: $(BUILD_DIR)/$(BINARY)"

# ─── Build with ONNX Runtime backend ─────────────────────────────────
# Requires ONNX Runtime shared library installed.
# Set ORT_LIB_PATH to the directory containing libonnxruntime.so if
# not in default library search path.
build-onnx:
	@echo "==> Building $(BINARY) with ONNX Runtime backend..."
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=1 \
	$(GO) build $(GOFLAGS) -tags onnx -o $(BUILD_DIR)/$(BINARY)-onnx $(CMD_DIR)
	@echo "==> Done: $(BUILD_DIR)/$(BINARY)-onnx"

# ─── Build with both whisper.cpp + ONNX Runtime ──────────────────────
build-all:
	@echo "==> Building $(BINARY) with whisper.cpp + ONNX Runtime..."
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=1 \
	CGO_CFLAGS="$(CGO_CFLAGS)" \
	CGO_LDFLAGS="$(CGO_LDFLAGS)" \
	$(GO) build $(GOFLAGS) -tags "whisper,onnx" -o $(BUILD_DIR)/$(BINARY) $(CMD_DIR)
	@echo "==> Done: $(BUILD_DIR)/$(BINARY) (whisper + onnx)"

# ─── Download ONNX whisper model from Hugging Face ───────────────────
# Downloads the encoder + decoder ONNX files and tokenizer.
# Override ONNX_MODEL_SIZE for other sizes (tiny, base, small, medium).
ONNX_MODEL_SIZE ?= base.en
ONNX_MODEL_DIR  := models/whisper-$(ONNX_MODEL_SIZE)-onnx
onnx-model:
	@mkdir -p $(ONNX_MODEL_DIR)
	@echo "==> Downloading whisper-$(ONNX_MODEL_SIZE) ONNX model..."
	@for f in encoder.onnx decoder.onnx tokenizer.json config.json; do \
		if [ ! -f "$(ONNX_MODEL_DIR)/$$f" ]; then \
			echo "    Downloading $$f..."; \
			curl -L --progress-bar \
				"https://huggingface.co/openai/whisper-$(ONNX_MODEL_SIZE)/resolve/main/onnx/$$f" \
				-o "$(ONNX_MODEL_DIR)/$$f"; \
		else \
			echo "    $(ONNX_MODEL_DIR)/$$f already exists, skipping."; \
		fi; \
	done
	@echo "==> ONNX model saved to $(ONNX_MODEL_DIR)/"

# ─── Download Silero VAD ONNX model ──────────────────────────────────
# Downloads the Silero VAD model (~2 MB) for neural speech detection.
# Used by the streaming pipeline to replace energy-based VAD.
SILERO_VAD_URL := https://github.com/snakers4/silero-vad/raw/master/src/silero_vad/data/silero_vad.onnx
SILERO_VAD_PATH := models/silero_vad.onnx
silero-model:
	@mkdir -p models
	@if [ ! -f "$(SILERO_VAD_PATH)" ]; then \
		echo "==> Downloading Silero VAD model..."; \
		curl -L --progress-bar "$(SILERO_VAD_URL)" -o "$(SILERO_VAD_PATH)"; \
		echo "==> Silero VAD model saved to $(SILERO_VAD_PATH)"; \
	else \
		echo "    $(SILERO_VAD_PATH) already exists, skipping."; \
	fi

# ─── Stub build without whisper.cpp (for development / CI) ───────────
# CGO is still enabled for malgo (audio) and robotgo (keyboard), but
# whisper.cpp is NOT linked — the stub transcriber is used instead.
build-stub:
	@echo "==> Building $(BINARY) without whisper.cpp (stub transcriber)..."
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=1 \
	$(GO) build $(GOFLAGS) -o $(BUILD_DIR)/$(BINARY) $(CMD_DIR)
	@echo "==> Done: $(BUILD_DIR)/$(BINARY)"

# ─── Tests ────────────────────────────────────────────────────────────
test:
	LD_LIBRARY_PATH="$(WHISPER_DIR)/build/src:$(WHISPER_DIR)/build/ggml/src:$$LD_LIBRARY_PATH" \
	CGO_ENABLED=1 \
	CGO_CFLAGS="$(CGO_CFLAGS)" \
	CGO_LDFLAGS="$(CGO_LDFLAGS)" \
	$(GO) test -tags "whisper,onnx" ./... -v -count=1 -timeout 60s

# ─── Test with coverage report ────────────────────────────────────────
test-cover:
	$(GO) test ./... -count=1 -timeout 60s -coverprofile=coverage.out -covermode=atomic
	$(GO) tool cover -func=coverage.out
	@echo "==> HTML report: go tool cover -html=coverage.out"

# ─── Lint ─────────────────────────────────────────────────────────────
lint:
	golangci-lint run ./...

# ─── Clean ────────────────────────────────────────────────────────────
clean:
	rm -rf $(BUILD_DIR) coverage.out

# ─── Install ──────────────────────────────────────────────────────────
# Installs the whisper + ONNX build (speaker identification needs ONNX).
# The binary is written to a temp name and renamed so a running dictate
# daemon does not block the upgrade ("text file busy").
INSTALL_DIR  ?= /usr/local/bin
USER_BIN_DIR ?= $(HOME)/.local/bin

# install-user — build and install to ~/.local/bin (no sudo)
install-user: build-all
	@mkdir -p $(USER_BIN_DIR)
	install -m 0755 $(BUILD_DIR)/$(BINARY) $(USER_BIN_DIR)/$(BINARY).new
	mv -f $(USER_BIN_DIR)/$(BINARY).new $(USER_BIN_DIR)/$(BINARY)
	@echo "✅ Installed $(BINARY) → $(USER_BIN_DIR)/$(BINARY)"
	@echo "   Make sure $(USER_BIN_DIR) is in your PATH; restart a running 'dictate' to use it."

# install — system-wide (needs sudo): binary to /usr/local/bin plus a udev rule
# so keyboard devices are accessible to logged-in users (via "uaccess" tag).
install: build-all
	@echo "==> Installing $(BINARY) to $(INSTALL_DIR)..."
	sudo install -m 0755 $(BUILD_DIR)/$(BINARY) $(INSTALL_DIR)/$(BINARY).new
	sudo mv -f $(INSTALL_DIR)/$(BINARY).new $(INSTALL_DIR)/$(BINARY)
	@echo "==> Creating udev rule for input device access..."
	@echo 'KERNEL=="event*", SUBSYSTEM=="input", MODE="0660", GROUP="input", TAG+="uaccess"' \
		| sudo tee /etc/udev/rules.d/99-gleann-plugin-sound-input.rules > /dev/null
	@sudo udevadm control --reload-rules
	@sudo udevadm trigger --subsystem-match=input
	@echo "==> Adding $(USER) to 'input' group..."
	@sudo usermod -aG input $(USER) 2>/dev/null || true
	@echo "✅ Installed $(BINARY) → $(INSTALL_DIR)/$(BINARY)"
	@echo "✅ udev rule created, user '$(USER)' added to 'input' group"
	@echo "NOTE: Log out and back in (or run: sg input -c \"gleann-plugin-sound ...\") to activate."

# uninstall — remove both the user and the system-wide binary
uninstall:
	rm -f $(USER_BIN_DIR)/$(BINARY)
	@if [ -f $(INSTALL_DIR)/$(BINARY) ]; then sudo rm -f $(INSTALL_DIR)/$(BINARY); fi
	@echo "✅ Removed $(BINARY)"

# ─── Setup input group only (no install) ──────────────────────────────
setup-input:
	@echo "==> Setting up input device access..."
	@echo 'KERNEL=="event*", SUBSYSTEM=="input", MODE="0660", GROUP="input", TAG+="uaccess"' \
		| sudo tee /etc/udev/rules.d/99-gleann-plugin-sound-input.rules > /dev/null
	@sudo udevadm control --reload-rules
	@sudo udevadm trigger --subsystem-match=input
	@sudo usermod -aG input $(USER)
	@echo "✓ Done. Log out and back in to activate."

# ─── Quick-run helpers ────────────────────────────────────────────────
run-dictate: build
	$(BUILD_DIR)/$(BINARY) dictate --key "ctrl+alt+space" --model $(WHISPER_MODEL)

run-listen: build
	$(BUILD_DIR)/$(BINARY) listen --model $(WHISPER_MODEL)

run-transcribe: build
	$(BUILD_DIR)/$(BINARY) transcribe --model $(WHISPER_MODEL) --file $(FILE)

run-dictate-onnx: build-onnx
	$(BUILD_DIR)/$(BINARY)-onnx dictate --backend onnx --key "ctrl+alt+space" --model $(ONNX_MODEL_DIR)

run-dictate-nemotron: build-onnx
	ORT_LIB_PATH=/home/tevfik/WORKSPACE/nemotron-onnx-go/lib/libonnxruntime.so \
	LD_LIBRARY_PATH=/home/tevfik/WORKSPACE/nemotron-onnx-go/lib \
	$(BUILD_DIR)/$(BINARY)-onnx dictate --backend nemotron --key "ctrl+alt+space" --model /home/tevfik/WORKSPACE/nemotron-onnx-go/model

# ─── Release ──────────────────────────────────────────────────────────
.PHONY: release
release: build
	@mkdir -p dist
	tar czf dist/$(BINARY)-$(VERSION)-linux-amd64.tar.gz -C $(BUILD_DIR) $(BINARY)
	@echo "📦 Release artifact in dist/"
