#!/usr/bin/env bash
# deploy.sh - build locally and deploy to the microvms host over SSH.
# usage: scripts/deploy.sh user@host [--full]
#   --full  also rebuild/re-upload kernel + rootfs assets (first deploy)
#
# Ships the bundle with scp (no privileges needed) and then runs ONE
# privileged ssh session (setup-host.sh) so you type the sudo password once.
# Run this from a real terminal if the host's sudo needs a password.
set -euo pipefail

TARGET="${1:?usage: deploy.sh user@host [--full]}"
FULL="${2:-}"
REMOTE_DIR="/opt/microvms"

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_DIR"

echo "== build =="
make build
if [ "$FULL" = "--full" ] || [ ! -s images/rootfs.ext4 ] || [ ! -s images/vmlinux ]; then
  make assets
fi

echo "== ship to ${TARGET} (no privileges needed) =="
tar czf /tmp/microvms-deploy.tgz bin scripts guest deploy docs images Makefile
scp -q /tmp/microvms-deploy.tgz "$TARGET:/tmp/microvms-deploy.tgz"

echo "== install on host (sudo password prompted once) =="
ssh -t "$TARGET" "sudo bash -c '
  set -e
  rm -rf $REMOTE_DIR
  mkdir -p $REMOTE_DIR
  tar xzf /tmp/microvms-deploy.tgz -C $REMOTE_DIR
  rm /tmp/microvms-deploy.tgz
  chmod +x $REMOTE_DIR/scripts/*.sh
  bash $REMOTE_DIR/scripts/setup-host.sh
'"
rm -f /tmp/microvms-deploy.tgz

HOST_IP=$(echo "$TARGET" | cut -d@ -f2)
echo
echo "deploy complete. verify with:"
echo "  curl http://$HOST_IP:8420/v1/health"
