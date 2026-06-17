import os
import sys
import subprocess
import re
import json
import argparse
import glob

def parse_time(time_str):
    """Parses a VTT time string 'HH:MM:SS.MMM' into seconds (float)."""
    parts = time_str.replace(',', '.').split(':')
    if len(parts) == 2:
        return float(parts[0]) * 60 + float(parts[1])
    elif len(parts) == 3:
        return float(parts[0]) * 3600 + float(parts[1]) * 60 + float(parts[2])
    return 0.0

def parse_vtt(vtt_file):
    blocks = []
    with open(vtt_file, 'r', encoding='utf-8') as f:
        content = f.read()
    
    # Simple VTT block matching
    pattern = re.compile(r'(\d{2}:\d{2}:\d{2}\.\d{3})\s*-->\s*(\d{2}:\d{2}:\d{2}\.\d{3})\n((?:.|\n)*?)(?=\n\n|\Z)', re.MULTILINE)
    
    for match in pattern.finditer(content):
        start_str = match.group(1)
        end_str = match.group(2)
        text = match.group(3).strip().replace('\n', ' ')
        
        # Remove tags like <c.colorE5E5E5> or <00:00:01.000>
        text = re.sub(r'<[^>]+>', '', text)
        
        if not text:
            continue
            
        blocks.append({
            'start': parse_time(start_str),
            'end': parse_time(end_str),
            'text': text
        })
    return blocks

def main():
    parser = argparse.ArgumentParser(description="YouTube Dataset Builder for Gleann Sound")
    parser.add_argument("url", help="YouTube video URL")
    parser.add_argument("--outdir", default="test/testdata/youtube", help="Output directory")
    parser.add_argument("--lang", default="tr", help="Language code for subtitles (default: tr)")
    parser.add_argument("--limit", type=int, default=0, help="Limit the number of chunks generated (0 for no limit)")
    args = parser.parse_args()

    # Create safe video id from URL
    match = re.search(r'(?:v=|\/)([0-9A-Za-z_-]{11}).*', args.url)
    video_id = match.group(1) if match else "unknown"
    
    target_dir = os.path.join(args.outdir, video_id)
    os.makedirs(target_dir, exist_ok=True)
    
    print(f"[*] Downloading audio and subtitles for {video_id}...")
    
    # Download audio and subs
    cmd = [
        "pipx", "run", "yt-dlp",
        "--write-sub", "--sub-lang", args.lang,
        "--extract-audio", "--audio-format", "wav",
        "--audio-quality", "0",
        "--output", os.path.join(target_dir, "master.%(ext)s"),
        args.url
    ]
    
    try:
        subprocess.run(cmd, check=True)
    except subprocess.CalledProcessError as e:
        print(f"[!] yt-dlp failed: {e}")
        # Try falling back to auto-subs if manual subs aren't available
        print(f"[*] Falling back to auto-generated subtitles...")
        cmd_auto = [
            "pipx", "run", "yt-dlp",
            "--write-auto-subs", "--sub-lang", args.lang,
            "--extract-audio", "--audio-format", "wav",
            "--audio-quality", "0",
            "--output", os.path.join(target_dir, "master.%(ext)s"),
            args.url
        ]
        subprocess.run(cmd_auto, check=True)

    master_audio = os.path.join(target_dir, "master.wav")
    
    # yt-dlp might save as master.tr.vtt
    vtt_files = glob.glob(os.path.join(target_dir, f"*.{args.lang}.vtt"))
    if not vtt_files:
        print("[!] No VTT subtitle file found!")
        sys.exit(1)
        
    vtt_file = vtt_files[0]
    
    print(f"[*] Parsing subtitles from {vtt_file}...")
    blocks = parse_vtt(vtt_file)
    print(f"[*] Found {len(blocks)} subtitle blocks.")
    
    print(f"[*] Slicing audio with ffmpeg...")
    generated_chunks = 0
    for i, block in enumerate(blocks):
        if args.limit > 0 and i >= args.limit:
            break
            
        start = block['start']
        end = block['end']
        duration = end - start
        text = block['text']
        
        # Output paths
        chunk_audio = os.path.join(target_dir, f"chunk_{i:04d}.wav")
        chunk_jsonl = os.path.join(target_dir, f"chunk_{i:04d}.jsonl")
        
        # Run ffmpeg to slice
        ffmpeg_cmd = [
            "ffmpeg", "-y", "-v", "quiet",
            "-i", master_audio,
            "-ss", str(start),
            "-t", str(duration),
            "-ar", "16000", "-ac", "1", "-f", "wav",
            chunk_audio
        ]
        subprocess.run(ffmpeg_cmd, check=True)
        
        # Write JSONL
        with open(chunk_jsonl, 'w', encoding='utf-8') as f:
            json.dump({"start_ms": int(start*1000), "end_ms": int(end*1000), "text": text}, f, ensure_ascii=False)
            f.write('\n')
        generated_chunks += 1
            
    print(f"[+] Successfully generated {generated_chunks} chunks in {target_dir}")
    print(f"[+] You can now evaluate using:")
    print(f"    go run test/benchmark/evaluate_dataset.go nemotron <model_path> --dir {target_dir}")

if __name__ == "__main__":
    main()
