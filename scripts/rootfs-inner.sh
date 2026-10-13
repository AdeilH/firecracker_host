#!/bin/sh
# rootfs-inner.sh - runs INSIDE the alpine build container.
# Env: SIZE_MB, PKGS. Overlay mounted at /overlay, output dir at /out.
set -eux

apk add --no-cache e2fsprogs

# 1. minimal Alpine root filesystem
mkdir -p /rootfs/etc/apk
cp /etc/apk/repositories /rootfs/etc/apk/repositories
cp -a /etc/apk/keys /rootfs/etc/apk/
# shellcheck disable=SC2086
apk --root /rootfs --initdb add --no-cache $PKGS

# 2. overlay our guest files (inittab, vmconfig service, sshd_config)
cp -r /overlay/. /rootfs/

# 3. enable OpenRC services via runlevel symlinks
mkdir -p /rootfs/etc/runlevels/boot /rootfs/etc/runlevels/default /rootfs/etc/runlevels/shutdown
ln -sf /etc/init.d/vmconfig  /rootfs/etc/runlevels/boot/vmconfig
ln -sf /etc/init.d/acpid     /rootfs/etc/runlevels/default/acpid
ln -sf /etc/init.d/sshd      /rootfs/etc/runlevels/default/sshd
ln -sf /etc/init.d/killprocs /rootfs/etc/runlevels/shutdown/killprocs
ln -sf /etc/init.d/mount-ro  /rootfs/etc/runlevels/shutdown/mount-ro
ln -sf /etc/init.d/savecache /rootfs/etc/runlevels/shutdown/savecache

# 4. hostname + empty machine-id (ssh host keys are generated on first boot
#    by sshd's init script, keeping the base image clonable)
echo microvms-vm > /rootfs/etc/hostname
touch /rootfs/etc/machine-id

# 5. build the ext4 image
rm -f /out/rootfs.ext4
truncate -s "${SIZE_MB}M" /out/rootfs.ext4
mkfs.ext4 -q -F -d /rootfs -L rootfs /out/rootfs.ext4
ls -lh /out/rootfs.ext4
