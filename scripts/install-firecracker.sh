#!/usr/bin/env bash
# install-firecracker.sh [version] [dest-dir]
# Downloads a Firecracker release and installs firecracker + jailer binaries.
#   version defaults to a known-good release; dest defaults to /usr/local/bin
set -euo pipefail

VERSION="${1:-1.10.1}"
DEST="${2:-/usr/local/bin}"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64) FC_ARCH=x86_64 ;;
  aarch64) FC_ARCH=aarch64 ;;
  *) echo "unsupported arch: $ARCH"; exit 1 ;;
esac

TARBALL="firecracker-v${VERSION}-${FC_ARCH}.tgz"
URL="https://github.com/firecracker-microvm/firecracker/releases/download/v${VERSION}/${TARBALL}"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

echo "downloading firecracker v${VERSION}..."
curl -fL --progress-bar -o "$TMP/$TARBALL" "$URL"
tar -xzf "$TMP/$TARBALL" -C "$TMP"

install -m 0755 "$TMP/release-v${VERSION}-${FC_ARCH}/firecracker-v${VERSION}-${FC_ARCH}" "$DEST/firecracker"
install -m 0755 "$TMP/release-v${VERSION}-${FC_ARCH}/jailer-v${VERSION}-${FC_ARCH}" "$DEST/jailer" || true

"$DEST/firecracker" --version
echo "installed to $DEST"
