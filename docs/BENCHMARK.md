# Speech-to-Text Benchmark Guide

This document describes how to run ASR (Automatic Speech Recognition) benchmarks for the `gleann-plugin-sound` project. It also provides comparative performance results between **Whisper.cpp** and **Nemotron-ASR (ONNX)**.

---

## 1. Benchmark Results

Tests were performed on an Intel CPU (CPU-only, no GPU acceleration) using 4 intra-op CPU threads.

### Dataset: Google FLEURS Turkish (tr_tr) Validation Split
- **Samples**: 30 audio clips (16kHz, 16-bit Mono WAV, averaging 5-15 seconds per clip).
- **Ground Truth**: Human-verified manual transcriptions.

| Model / Backend | Parameters | Quantization / Format | Average RTF (Real-Time Factor) | Word Error Rate (WER) | Character Error Rate (CER) |
| :--- | :---: | :---: | :---: | :---: | :---: |
| **Nemotron-3.5-ASR (ONNX)** | 600M | **INT4** | **0.23x** (4.3x faster than real-time) | **15.32%** | **3.93%** |
| **Whisper Small (Whisper.cpp)** | 244M | FP32 / GGML | 0.45x (2.2x faster than real-time) | 19.43% | 4.89% |
| **Whisper Large-v3-Turbo** | 809M | FP32 / GGML | 1.95x (Slower than real-time) | ~25.0% | — |

### Key Findings
1. **Accuracy**: **Nemotron-3.5-ASR (INT4)** achieves a significantly lower Word Error Rate (15.32% vs 19.43%) than Whisper Small on Turkish speech, despite being quantized to 4-bit.
2. **Speed & Latency**: Nemotron is twice as fast as Whisper Small (RTF 0.23x vs 0.45x) on CPU due to optimized ONNX Runtime execution and quantized operations.
3. **Resource Efficiency**: Int4 quantization reduces memory footprint and computational requirements, making Nemotron the optimal single-model choice for edge dictation.

---

## 2. How to Run Benchmarks

The benchmark suite consists of two main tools located in `test/benchmark/`:
1. **`youtube_dataset_builder.py`**: Downloads any YouTube video and splits it into short, timestamped audio chunks based on manual subtitles.
2. **`evaluate_dataset.go`**: Iterates over a dataset folder, runs transcription, and calculates WER/CER against ground truth jsonl files.

### Step 1: Generate a Dataset

#### Option A: Download a YouTube Video
Find a video (e.g. a TEDx Talk) that has **manual** (human-uploaded) subtitles enabled. Run the dataset builder:
```bash
python3 test/benchmark/youtube_dataset_builder.py "YOUTUBE_VIDEO_URL" --lang tr --outdir test/testdata/youtube
```
This downloads the audio, parses the subtitles, slices the audio into chunks, and saves them under `test/testdata/youtube/<video_id>/`.

#### Option B: Use Custom WAV Files
Put your `.wav` files (16kHz mono) in a folder, and create a corresponding `.jsonl` file for each audio file with the same name.
The JSONL format must be:
```json
{"text": "your ground truth transcription here"}
```

---

### Step 2: Run the Evaluation Tool

Ensure you set the correct environment paths for ONNX Runtime if you are using the Nemotron backend.

#### Run with Nemotron-ASR Backend (ONNX)
```bash
ORT_LIB_PATH=/path/to/libonnxruntime.so \
LD_LIBRARY_PATH=/path/to/lib \
go run -tags onnx test/benchmark/evaluate_dataset.go \
  nemotron \
  /path/to/nemotron/model \
  --dir test/testdata/fleurs_tr
```

#### Run with Whisper Backend (Whisper.cpp)
Requires `cgo` compiler flags pointing to your local `whisper.cpp` installation:
```bash
CGO_CFLAGS="-I/path/to/whisper.cpp/include -I/path/to/whisper.cpp/ggml/include" \
CGO_LDFLAGS="-L/path/to/whisper.cpp/build/src -L/path/to/whisper.cpp/build/ggml/src -lwhisper -lggml -lggml-cpu -lggml-base -lm -lstdc++ -lpthread -lgomp" \
go run -tags whisper test/benchmark/evaluate_dataset.go \
  whisper \
  /path/to/whisper-model.bin \
  --dir test/testdata/fleurs_tr
```

---

## 3. Changing the Target Language

By default, `evaluate_dataset.go` runs speech recognition in Turkish (`tr`). If you want to evaluate an English dataset, open `test/benchmark/evaluate_dataset.go` and change:
```go
engine.SetLanguage("tr")
```
to:
```go
engine.SetLanguage("en")
```
