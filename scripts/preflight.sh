#!/usr/bin/env bash
# preflight.sh - verify a machine can run Firecracker microVMs.
# Run this on the host (e.g. ssh host sudo scripts/preflight.sh).
set -u

PASS=0; FAIL=0
ok()   { echo "  [ok]   $1"; PASS=$((PASS+1)); }
bad()  { echo "  [FAIL] $1"; FAIL=$((FAIL+1)); }
info() { echo "  [--]   $1"; }

echo "== microvms preflight =="

# 1. Linux kernel version (firecracker needs >= 4.14; 5.10+ recommended)
KVER=$(uname -r | cut -d. -f1-2)
KMAJ=$(echo "$KVER" | cut -d. -f1); KMIN=$(echo "$KVER" | cut -d. -f2)
if [ "$KMAJ" -gt 5 ] || { [ "$KMAJ" -eq 5 ] && [ "$KMIN" -ge 10 ]; }; then
  ok "kernel $KVER (>= 5.10)"
elif [ "$KMAJ" -eq 4 ] && [ "$KMIN" -ge 14 ] || [ "$KMAJ" -ge 5 ]; then
  info "kernel $KVER works but 5.10+ is recommended"
else
  bad "kernel $KVER too old (need >= 4.14)"
fi

# 2. KVM device - the hard gate
if [ -e /dev/kvm ]; then
  ok "/dev/kvm exists"
  if [ -w /dev/kvm ] || [ -r /dev/kvm ]; then
    ok "/dev/kvm readable/writable by $(id -un)"
  else
    bad "/dev/kvm not accessible by $(id -un) (run as root, or add user to kvm group)"
  fi
else
  bad "/dev/kvm missing: no KVM. If this is a cloud VM, nested virtualization"
  bad "is probably disabled; Firecracker cannot run here."
fi

# 3. CPU virtualization extensions
if grep -qE 'vmx|svm' /proc/cpuinfo 2>/dev/null; then
  ok "CPU has VT-x/AMD-V"
else
  bad "no vmx/svm flags in /proc/cpuinfo"
fi

# 4. TUN/TAP device
if [ -e /dev/net/tun ]; then
  ok "/dev/net/tun exists"
else
  bad "/dev/net/tun missing (kernel module 'tun' not loaded?)"
fi

# 5. Tools the daemon needs
for t in ip iptables; do
  if command -v $t >/dev/null 2>&1; then ok "tool '$t' present"; else bad "tool '$t' missing"; fi
done

# 6. systemd (for the daemon unit)
if command -v systemctl >/dev/null 2>&1; then ok "systemd present"; else info "systemd absent - run microvmsd manually"; fi

# 7. Memory headroom
MEM=$(free -m | awk '/^Mem:/{print $2}')
AVAIL=$(free -m | awk '/^Mem:/{print $7}')
if [ "${AVAIL:-0}" -ge 512 ]; then
  ok "memory: ${MEM}MiB total, ${AVAIL}MiB available"
else
  info "memory: ${MEM}MiB total, ${AVAIL}MiB available (<512MiB free)"
fi

# 8. Root or sudo
if [ "$(id -u)" -eq 0 ]; then ok "running as root"; else info "not root (daemon needs root; setup uses sudo)"; fi

echo
echo "result: $PASS ok, $FAIL failed"
[ "$FAIL" -eq 0 ] && echo "PREFLIGHT PASS - this host can run microvms" || echo "PREFLIGHT FAIL - fix the items above"
exit "$FAIL"
