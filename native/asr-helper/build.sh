#!/usr/bin/env bash
# Builds gleann-asr-helper against transcribe.cpp and installs it.
#
#   native/asr-helper/build.sh [install-dir]       (default install dir: ~/.local/bin)
#
# TRANSCRIBE_SRC may point at a transcribe.cpp checkout or unpacked sdist;
# otherwise the sdist is fetched from PyPI (transcribe-cpp-native), which avoids
# cloning the large git history. Needs cmake, a C/C++ compiler and libopenblas-dev.
# Works on x86_64 and aarch64 Linux. Set GGML_NATIVE=OFF for a build that runs on
# other CPUs than the one it was built on (what the release workflow does);
# CMAKE_EXTRA adds further cmake flags.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
dest="${1:-$HOME/.local/bin}"
cache="${XDG_CACHE_HOME:-$HOME/.cache}/gleann-sound"
src="${TRANSCRIBE_SRC:-$cache/transcribe.cpp}"

if [ ! -f "$src/include/transcribe.h" ]; then
  echo "==> fetching transcribe.cpp source (PyPI sdist) into $src"
  tmp="$(mktemp -d)"
  python3 -m pip download --no-binary :all: --no-deps -d "$tmp" transcribe-cpp-native >/dev/null
  mkdir -p "$src"
  tar xzf "$tmp"/transcribe_cpp_native-*.tar.gz -C "$src" --strip-components=1
  rm -rf "$tmp"
fi

echo "==> building transcribe.cpp (CPU, native)"
cmake -S "$src" -B "$src/build" -DCMAKE_BUILD_TYPE=Release -DGGML_NATIVE="${GGML_NATIVE:-ON}" ${CMAKE_EXTRA:-} >/dev/null
cmake --build "$src/build" -j"$(nproc)" --target transcribe >/dev/null

echo "==> linking gleann-asr-helper"
mkdir -p "$dest"
b="$src/build"
gcc -O2 -Wall -I"$src/include" "$here/main.c" -o "$dest/gleann-asr-helper" \
  "$b/src/libtranscribe.a" "$b/ggml/src/libggml.a" "$b/ggml/src/libggml-cpu.a" "$b/ggml/src/libggml-base.a" \
  -lopenblas -lstdc++ -lm -lpthread -fopenmp -ldl
echo "==> installed $dest/gleann-asr-helper"
