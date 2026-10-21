# STT, speaker and TTS experiments — October 2026

All numbers are CPU-only (Intel i9-13900H, no GPU), measured here. Harness scripts and raw
results: `~/gleann-sound-experiments/` (outside the repo).

## Speech recognition (Turkish)

40 evenly spaced clips of the FLEURS `tr_tr` dev set (504 s), same normalisation for every
system (Turkish-aware lower-casing, punctuation removed). RTF = processing time / audio
duration, model load excluded.

| System | WER | CER | RTF | Streaming |
|--------|-----|-----|-----|-----------|
| Qwen3-ASR-1.7B fp32 (transformers 5.13, 8 threads) | 11.66 % | 2.49 % | 0.85 | no |
| **Qwen3-ASR-1.7B Q8_0 (transcribe.cpp)** | **11.95 %** | 2.75 % | 0.41 | no |
| Qwen3-ASR-0.6B Q8_0 (transcribe.cpp) | 19.39 % | 4.63 % | 0.19 | no |
| Nemotron 3.5 fp32, offline (transformers) | 17.20 % | 3.75 % | 0.22 | — |
| **Nemotron 3.5 Q8_0, streaming 560 ms (transcribe.cpp)** | 16.47 % | 3.79 % | 0.29 | yes |
| Nemotron 3.5 Q8_0, offline `[56,13]` | 16.62 % | 3.79 % | 0.10 | — |
| Nemotron 3.5 Q8_0, streaming 240 ms | 17.35 % | 3.81 % | 0.45 | yes |
| Nemotron 3.5 int4 ONNX (our Go engine, 560 ms) | 19.24 % | 4.21 % | 0.17 | yes |

Reading: Qwen3-ASR-1.7B is ~30 % better (relative) than any Nemotron variant but cannot stream;
the 0.6B Qwen is no better than Nemotron. Q8 loses almost nothing against fp32, int4 costs about
2.7 points. NVIDIA's own FLEURS Turkish figure for Nemotron (11–12 %) is not reproduced on this
subset with this normalisation. In transcribe.cpp, 80 ms chunks run slower than real time and 160 ms
about at real time; 240 ms and up are comfortable. **Decision:** two passes — Nemotron Q8 live,
Qwen3-ASR-1.7B Q8 re-transcribes each finished utterance in the background.

An earlier "near perfect" Turkish Nemotron result came from a hard-coded post-processing table tuned
to one test sentence (since removed; see `corrections` in `sound.json`).

## Speaker identification

Test set: four real single-speaker Turkish recordings (CC-licensed YouTube), 32 spliced turns of
4-11 s with 0.2-0.8 s gaps, ground truth known; "TV" = a Turkish news broadcast mixed in at
10 dB / 20 dB / 3 dB below the voices. (An early set was invalid because one "single speaker"
source was a two-host podcast — check sources before trusting labels.)

**Unsupervised diarization (k = 4 known), DER:**

| Model | clean | TV 20 dB | TV 10 dB |
|-------|-------|----------|----------|
| TitaNet-small + pyannote segmentation | 17.8 % | 38.8 % | 54.0 % |
| Sortformer streaming 4spk v2.1 (22x real time) | 15.2 % | 40.4 % | 52.1 % |
| WeSpeaker ResNet34-LM | 59.1 % | 74.7 % | 66.2 % |
| WeSpeaker CAM++-LM | 68.5 % | 63.4 % | 68.1 % |
| 3D-Speaker CAM++ (EN) | 66.0 % | 72.8 % | 65.8 % |

About 15 points of every "clean" figure is segmentation miss caused by pauses inside the reference
turns, not confusion. VoxCeleb-trained WeSpeaker/3D-Speaker models were unusable here. Whatever
the model, **a TV in the room turns its speakers into speakers**: error 40-55 % at 10-20 dB.

**Closed-set identification of enrolled voices (what the plugin does):** 5 s x 3 enrolment samples
per person, 3 s windows. Turn accuracy (majority of windows): clean 100 %, TV 20 dB 100 %,
10 dB 97 %, 3 dB 81 % (TitaNet-small). In the Go implementation (utterance turns found with 4 s
windows) share of speech time: 95 / 90 / 81 / 58 % correct with 2-4 % wrong names; everything else
is `?`. Cosine threshold 0.35: enrolled speakers score a median 0.67 (clean) / 0.47 (TV 10 dB);
strangers and TV speech stay below ~0.34 (95th percentile).

## Voice activity detection

The existing energy VAD tracks a running average, so after a loud speaker it silently drops quieter
ones (in the 4-speaker test it lost the 17 s following the first voice). Silero VAD v5 fixes that; the
repository's Silero code only spoke the v4 interface and had never run. Now used when the model file
exists.

## Text to speech (Turkish) — not integrated yet

Candidates researched (primary sources; quality evidence for Turkish is thin everywhere):

| Model | Licence | Notes |
|-------|---------|-------|
| Supertonic 3 (ONNX, 99 M params) | model OpenRAIL-M, code MIT | tested, below |
| Piper `tr_TR-dfki` | CC BY-NC-SA | non-commercial; espeak-ng (GPL) |
| FreyaTTS-small | Apache-2.0 | best claimed Turkish WER, no ONNX, RTF 0.70 on Apple M3 |
| MMS-TTS `tur`, XTTS-v2 | CC-BY-NC / CPML | no commercial use |
| Kokoro | Apache-2.0 | no Turkish |

Supertonic 3 with the official ONNX Runtime example (`supertone-inc/supertonic`, 8 denoising steps):
real-time factor **0.3** on CPU (RTF 0.22 with 4 steps, but worse). Five Turkish test sentences
(greeting, numbers/currency, a very long word, a 24-word sentence, English brand names) were
synthesised and read back by Nemotron: round-trip CER 5.6 % (voice F3), 6.6 % (F1), 8.9 % (M3);
the long sentence came back perfectly, short ones and "OpenAI'nin API'si" were the hard cases.
Round-trip ASR is a proxy: **listen to `~/gleann-sound-experiments/tts_samples/` before choosing**
(F3/F1/M3 x 5 sentences). Integration would be a small ONNX Runtime step in Go plus the text
frontend from the example's `helper.py`.
