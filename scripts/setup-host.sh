#!/usr/bin/env bash
# setup-host.sh - one-time host preparation for microvmsd.
# Installs firecracker, creates directories, writes config with a fresh API
# token, installs the systemd unit and starts the daemon.
# Run ON THE HOST as root (deploy.sh does this for you over SSH).
set -euo pipefail

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
DATA_DIR="${MICROVMS_DATA_DIR:-/var/lib/microvms}"
CONF_DIR="/etc/microvms"
FC_VERSION="${FC_VERSION:-1.10.1}"

[ "$(id -u)" -eq 0 ] || { echo "run as root"; exit 1; }

echo "== microvms host setup =="

# 1. Preflight
bash "$REPO_DIR/scripts/preflight.sh" || { echo "preflight failed; aborting"; exit 1; }

# 2. Firecracker binary
if ! command -v firecracker >/dev/null 2>&1; then
  bash "$REPO_DIR/scripts/install-firecracker.sh" "$FC_VERSION" /usr/local/bin
else
  echo "firecracker already installed: $(firecracker --version)"
fi

# 3. Directories and assets
mkdir -p "$DATA_DIR/images" "$DATA_DIR/vms" /usr/local/share/microvms "$CONF_DIR"
install -m 0755 "$REPO_DIR/bin/microvmsd" /usr/local/bin/microvmsd
install -m 0644 "$REPO_DIR/docs/api.yaml" /usr/local/share/microvms/openapi.yaml

if [ ! -s "$DATA_DIR/images/vmlinux" ]; then
  bash "$REPO_DIR/scripts/install-kernel.sh" "$DATA_DIR/images"
fi

if [ ! -s "$DATA_DIR/images/rootfs.ext4" ]; then
  if [ -s "$REPO_DIR/images/rootfs.ext4" ]; then
    echo "installing bundled rootfs image..."
    install -m 0644 "$REPO_DIR/images/rootfs.ext4" "$DATA_DIR/images/rootfs.ext4"
  else
    echo "WARNING: no rootfs image found."
    echo "Build one on a machine with Docker:  make image"
    echo "then re-run deploy."
  fi
fi

# 4. Config with API token (preserve tokens if config already exists)
if [ ! -f "$CONF_DIR/config.toml" ]; then
  TOKEN=$(openssl rand -hex 32)
  EXTERNAL_IP="${MICROVMS_EXTERNAL_IP:-$(ip -4 route get 8.8.8.8 2>/dev/null | awk '{for(i=1;i<NF;i++) if($i=="src") print $(i+1)}' | head -1)}"
  cat > "$CONF_DIR/config.toml" <<EOF
# microvmsd configuration
listen = "0.0.0.0:8420"
external_ip = "${EXTERNAL_IP}"
data_dir = "${DATA_DIR}"
bridge = "fcbr0"
host_cidr = "172.16.100.1/24"
ip_start = "172.16.100.10"
ip_end = "172.16.100.254"
ssh_port_base = 2200
api_tokens = ["${TOKEN}"]
default_vcpus = 1
default_mem_mib = 256
max_vcpus = 4
max_mem_mib = 2048
max_vms = 20
firecracker_bin = "/usr/local/bin/firecracker"
kernel_path = "${DATA_DIR}/images/vmlinux"
rootfs_path = "${DATA_DIR}/images/rootfs.ext4"
graceful_shutdown_timeout_sec = 10
EOF
  chmod 0600 "$CONF_DIR/config.toml"
  echo
  echo "=============================================================="
  echo "  API token (give this to your agents):"
  echo "  ${TOKEN}"
  echo "=============================================================="
else
  echo "config already exists at $CONF_DIR/config.toml (token preserved)"
fi

# 5. systemd unit
install -m 0644 "$REPO_DIR/deploy/microvmsd.service" /etc/systemd/system/microvmsd.service
systemctl daemon-reload
systemctl enable microvmsd >/dev/null
systemctl restart microvmsd
sleep 1
systemctl --no-pager --lines 5 status microvmsd || true

echo
echo "done. check: curl http://${EXTERNAL_IP:-localhost}:8420/v1/health"
