#!/bin/sh
# Run mkosi inside the pinned Debian trixie tools container. Usage: build.sh <outdir> [mkosi args]
set -eu
here=$(cd "$(dirname "$0")" && pwd)
out=$1; shift
mkdir -p "$out"
exec docker run --rm --privileged -v "$here":/spike:ro -v "$out":/out \
  -w /out nex-spike-tools mkosi --directory /spike/mkosi --output-dir /out/output \
  --cache-dir /out/cache --workspace-dir /out/work "$@"
