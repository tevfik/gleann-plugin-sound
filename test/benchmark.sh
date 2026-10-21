#!/bin/bash
set -e

# --- Colors for output ---
GREEN='\033[0;32m'
CYAN='\033[0;36m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

echo -e "${CYAN}=== GLEANN SOUND AUTOMATED BENCHMARK MANAGER ===${NC}"

# 1. Detect and set workspaces
SOUND_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NEMOTRON_SANDBOX_DIR="/home/tevfik/WORKSPACE/nemotron-onnx-go"

# 2. Build the latest gleann-plugin-sound binary with ONNX support
echo -e "${YELLOW}[1/4] Rebuilding gleann-plugin-sound with ONNX build tags...${NC}"
cd "$SOUND_DIR"
go build -tags onnx -o build/gleann-plugin-sound ./cmd/gleann-plugin-sound
echo -e "${GREEN}✓ Binary built successfully in $SOUND_DIR/build/gleann-plugin-sound${NC}"

# 3. Locate the ONNX Runtime library
if [ -z "$ORT_SHARED_LIBRARY_PATH" ]; then
    if [ -f "$NEMOTRON_SANDBOX_DIR/lib/libonnxruntime.so" ]; then
        export ORT_SHARED_LIBRARY_PATH="$NEMOTRON_SANDBOX_DIR/lib/libonnxruntime.so"
        echo -e "${GREEN}✓ Auto-detected ONNX Runtime library at: $ORT_SHARED_LIBRARY_PATH${NC}"
    else
        echo -e "${YELLOW}! libonnxruntime.so not found in sandbox, using system-wide search...${NC}"
    fi
else
    echo -e "${GREEN}✓ Using custom ORT_SHARED_LIBRARY_PATH: $ORT_SHARED_LIBRARY_PATH${NC}"
fi

# 4. Resolve default model and test audio file
MODEL_DIR="${1:-$NEMOTRON_SANDBOX_DIR/model}"
WAV_FILE="${2:-$NEMOTRON_SANDBOX_DIR/test.wav}"
LANG="${3:-tr}"

if [ ! -d "$MODEL_DIR" ]; then
    echo -e "${RED}Error: Nemotron model directory not found at $MODEL_DIR${NC}"
    echo -e "Please pass a valid model path: ./benchmark.sh [model_dir] [wav_file]"
    exit 1
fi

if [ ! -f "$WAV_FILE" ]; then
    echo -e "${RED}Error: Test audio file not found at $WAV_FILE${NC}"
    echo -e "Please pass a valid WAV file: ./benchmark.sh [model_dir] [wav_file]"
    exit 1
fi

echo -e "${YELLOW}[2/4] Running Nemotron ASR ONNX execution provider check...${NC}"
# Check if CUDA options are available
if nvidia-smi &>/dev/null; then
    echo -e "${GREEN}✓ NVIDIA GPU detected. Running with CUDA GPU acceleration if available.${NC}"
    PROVIDER="auto"
else
    echo -e "${YELLOW}! NVIDIA GPU not detected or driver missing. Running on CPU.${NC}"
    PROVIDER="cpu"
fi

echo -e "${YELLOW}[3/4] Starting Benchmark Command...${NC}"
echo -e "      Model Path: $MODEL_DIR"
echo -e "      WAV File:   $WAV_FILE"
echo -e "      Language:   $LANG"
echo -e "--------------------------------------------------------"

./build/gleann-plugin-sound benchmark \
    --backend nemotron \
    --model "$MODEL_DIR" \
    --file "$WAV_FILE" \
    --language "$LANG" \
    --provider "$PROVIDER"

echo -e "${YELLOW}[4/4] Benchmark completed!${NC}"
echo -e "${CYAN}=== How to read the results ===${NC}"
echo -e "  - ${GREEN}Real-Time Factor (RTF)${NC}: Specifies the ratio of processing time to audio duration."
echo -e "  - ${GREEN}RTF < 1.0${NC}: The model runs faster than real-time. (e.g., RTF=0.1 means 10s of audio is decoded in 1s). Suitable for real-time dictation."
echo -e "  - ${GREEN}RTF > 1.0${NC}: The model is slower than real-time. It might cause lag during live microphone capture."
echo -e "  - ${GREEN}Microphone / Keystroke Injection integration${NC}: Run ${YELLOW}./build/gleann-plugin-sound dictate --backend nemotron --model $MODEL_DIR --language tr${NC} to start voice typing with F9."
