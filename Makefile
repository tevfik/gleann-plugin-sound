# gleann-sound Makefile
# Build targets for the gleann-sound binary with and without CGO dependencies.

BINARY     := gleann-sound
CMD_DIR    := ./cmd/gleann-sound
BUILD_DIR  := ./build
WHISPER_MODEL ?= models/ggml-base.en.bin

# Go toolchain
GO     := go
GOFLAGS := -trimpath

# CGO flags for whisper.cpp — adjust WHISPER_DIR to your local path.
WHISPER_DIR ?= $(HOME)/whisper.cpp
CGO_CFLAGS  := -I$(WHISPER_DIR)/include -I$(WHISPER_DIR)/ggml/include
CGO_LDFLAGS := -L$(WHISPER_DIR)/build/src -L$(WHISPER_DIR)/build/ggml/src -lwhisper -lggml -lggml-cpu -lggml-base -lm -lstdc++ -lpthread -lgomp

.PHONY: all build build-onnx build-all build-stub clean test lint run-dictate whisper-setup whisper-model onnx-model install setup-input

# ─── Default ──────────────────────────────────────────────────────────
all: build

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
build:
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
	$(GO) test ./... -v -count=1 -timeout 60s

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

# ─── Install with proper permissions ──────────────────────────────────
# Copies the binary to /usr/local/bin and creates a udev rule so that
# keyboard devices are accessible to logged-in users (via "uaccess" tag).
# This eliminates the need for sudo or manual 'input' group membership.
INSTALL_DIR ?= /usr/local/bin
install: build
	@echo "==> Installing $(BINARY) to $(INSTALL_DIR)..."
	sudo install -m 0755 $(BUILD_DIR)/$(BINARY) $(INSTALL_DIR)/$(BINARY)
	@echo "==> Creating udev rule for input device access..."
	@echo 'KERNEL=="event*", SUBSYSTEM=="input", MODE="0660", GROUP="input", TAG+="uaccess"' \
		| sudo tee /etc/udev/rules.d/99-gleann-sound-input.rules > /dev/null
	@sudo udevadm control --reload-rules
	@sudo udevadm trigger --subsystem-match=input
	@echo "==> Adding $(USER) to 'input' group..."
	@sudo usermod -aG input $(USER) 2>/dev/null || true
	@echo ""
	@echo "✓ Installed $(BINARY) to $(INSTALL_DIR)/$(BINARY)"
	@echo "✓ udev rule created — keyboard devices now accessible to logged-in users"
	@echo "✓ User '$(USER)' added to 'input' group"
	@echo ""
	@echo "NOTE: Log out and back in (or run: sg input -c \"gleann-sound ...\") to activate."

# ─── Setup input group only (no install) ──────────────────────────────
setup-input:
	@echo "==> Setting up input device access..."
	@echo 'KERNEL=="event*", SUBSYSTEM=="input", MODE="0660", GROUP="input", TAG+="uaccess"' \
		| sudo tee /etc/udev/rules.d/99-gleann-sound-input.rules > /dev/null
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
