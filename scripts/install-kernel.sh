#!/usr/bin/env bash
# install-kernel.sh [dest-dir]
# Fetches the Firecracker team's recommended 6.1 kernel (prebuilt, virtio
# built-in) into <dest>/vmlinux. dest defaults to ./images
set -euo pipefail

DEST="${1:-images}"
mkdir -p "$DEST"

# Pinned from the Firecracker getting-started docs.
URL="https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.9/x86_64/vmlinux-6.1.102"

if [ -s "$DEST/vmlinux" ]; then
  echo "$DEST/vmlinux already present, skipping (delete it to re-download)"
  exit 0
fi

echo "downloading firecracker kernel 6.1.102..."
curl -fL --progress-bar -o "$DEST/vmlinux" "$URL"
ls -lh "$DEST/vmlinux"
