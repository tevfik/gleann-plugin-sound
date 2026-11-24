#!/usr/bin/env bash
# Fetch ONNX Runtime from the official NuGet package and lay it out like the
# old GitHub release tarball:
#   <destdir>/onnxruntime-linux-<arch>-<version>/{include,lib}
#
# Usage: fetch-onnxruntime.sh <version> <arch: x64|aarch64|arm64> <destdir>
# Env:   ORT_NUPKG=/path/to/file.nupkg  use a local package instead of downloading
#        (its sha256 is still verified).
set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <version> <x64|aarch64|arm64> <destdir>" >&2
  exit 2
fi
version=$1
arch=$2
destdir=$3

# sha256 of microsoft.ml.onnxruntime.<version>.nupkg, keyed by version.
case "$version" in
  1.22.0) want=d571e63a2329baacb713f441e65ad75284de354db6e1ac435fe4bebbb417986a ;;
  *) echo "error: no pinned sha256 for ONNX Runtime $version; add it to $0" >&2; exit 1 ;;
esac

case "$arch" in
  x64) rid=linux-x64; slugarch=x64 ;;
  aarch64|arm64) rid=linux-arm64; slugarch=aarch64 ;;
  *) echo "error: unsupported arch '$arch'" >&2; exit 2 ;;
esac

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

nupkg=${ORT_NUPKG:-}
if [ -z "$nupkg" ]; then
  nupkg=$work/ort.nupkg
  url="https://api.nuget.org/v3-flatcontainer/microsoft.ml.onnxruntime/${version}/microsoft.ml.onnxruntime.${version}.nupkg"
  curl -fsSL --retry 5 --retry-delay 3 --retry-all-errors -o "$nupkg" "$url"
fi

got=$(sha256sum "$nupkg" | cut -d' ' -f1)
if [ "$got" != "$want" ]; then
  echo "error: sha256 mismatch for ONNX Runtime $version nupkg" >&2
  echo "  expected $want" >&2
  echo "  got      $got" >&2
  exit 1
fi

unzip -q "$nupkg" 'build/native/include/*' "runtimes/${rid}/native/*" -d "$work/x"

out=$destdir/onnxruntime-linux-${slugarch}-${version}
rm -rf "$out"
mkdir -p "$out/include" "$out/lib"
cp -a "$work"/x/build/native/include/. "$out/include/"
cp -a "$work/x/runtimes/${rid}/native/." "$out/lib/"

# The tarball shipped libonnxruntime.so -> .so.1 -> .so.<version>.
mv "$out/lib/libonnxruntime.so" "$out/lib/libonnxruntime.so.${version}"
ln -s "libonnxruntime.so.${version}" "$out/lib/libonnxruntime.so.1"
ln -s "libonnxruntime.so.1" "$out/lib/libonnxruntime.so"

echo "ONNX Runtime $version ($rid) installed to $out"
