#!/usr/bin/env bash
# build-image.sh - build the Alpine guest rootfs (ext4) using Docker.
# The image self-configures at boot from the /dev/vdb config disk written by
# microvmsd (see guest/etc/init.d/vmconfig).
#
# usage: scripts/build-image.sh [output-path]   (default images/rootfs.ext4)
set -euo pipefail

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${1:-$REPO_DIR/images/rootfs.ext4}"
SIZE_MB="${ROOTFS_SIZE_MB:-512}"
ALPINE_VERSION="${ALPINE_VERSION:-3.20}"
# extra packages agents commonly want; override to slim down
PKGS="${ROOTFS_PKGS:-alpine-base openssh acpid bash curl git python3 ca-certificates e2fsprogs tar gzip unzip}"

mkdir -p "$(dirname "$OUT")"
echo "building rootfs ($SIZE_MB MiB, alpine $ALPINE_VERSION, pkgs: $PKGS)..."

docker run --rm \
  -v "$REPO_DIR/guest:/overlay:ro" \
  -v "$REPO_DIR/scripts/rootfs-inner.sh:/inner.sh:ro" \
  -v "$(dirname "$OUT"):/out" \
  -e SIZE_MB="$SIZE_MB" -e PKGS="$PKGS" \
  alpine:"$ALPINE_VERSION" sh /inner.sh

ls -lh "$OUT"
echo "rootfs built: $OUT"
