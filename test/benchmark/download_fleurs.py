import os
import sys
import argparse
import json

def main():
    parser = argparse.ArgumentParser(description="Download Google FLEURS Turkish test/validation set for benchmarking")
    parser.add_argument("--limit", type=int, default=30, help="Number of samples to download")
    parser.add_argument("--split", default="validation", help="Dataset split (validation or test)")
    parser.add_argument("--outdir", default="test/testdata/fleurs_tr", help="Output directory")
    args = parser.parse_args()

    os.makedirs(args.outdir, exist_ok=True)

    try:
        from datasets import load_dataset
        import soundfile as sf
    except ImportError:
        print("[!] Missing required libraries. Please run: pip install datasets soundfile")
        sys.exit(1)

    print(f"[*] Loading google/fleurs (tr_tr, split={args.split})...")
    # Using streaming=True to avoid downloading the entire dataset archive upfront
    ds = load_dataset("google/fleurs", "tr_tr", split=args.split, streaming=True)

    print(f"[*] Downloading and saving first {args.limit} samples...")
    count = 0
    for i, example in enumerate(ds):
        if count >= args.limit:
            break

        audio = example["audio"]
        transcription = example["transcription"]
        
        # Audio dict has 'array' (numpy array) and 'sampling_rate'
        sr = audio["sampling_rate"]
        arr = audio["array"]
        duration_ms = int((len(arr) / sr) * 1000)

        audio_path = os.path.join(args.outdir, f"chunk_{count:04d}.wav")
        jsonl_path = os.path.join(args.outdir, f"chunk_{count:04d}.jsonl")

        # Save audio
        sf.write(audio_path, arr, sr)

        # Save transcript
        with open(jsonl_path, "w", encoding="utf-8") as f:
            json.dump({
                "start_ms": 0,
                "end_ms": duration_ms,
                "text": transcription
            }, f, ensure_ascii=False)
            f.write("\n")

        print(f"[+] Saved sample {count+1}/{args.limit}: {audio_path}")
        count += 1

    print(f"[+] Finished downloading {count} samples to {args.outdir}!")
    print(f"[+] You can now run the benchmark using:")
    print(f"    go run test/benchmark/evaluate_dataset.go nemotron <model_path> --dir {args.outdir}")

if __name__ == "__main__":
    main()
