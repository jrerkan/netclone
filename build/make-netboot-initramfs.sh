#!/bin/bash
# Builds the minimal initramfs used by netbooted clients.
# Runs inside the Debian live-build chroot (root required).
set -euo pipefail

OUT="${1:-/usr/lib/netclone/netboot/initrd.img}"
SHARE=/usr/local/share/netclone
KVER="$(ls /lib/modules | sort -V | tail -n1)"

if [ -z "$KVER" ]; then
    echo "make-netboot-initramfs: no kernel modules found" >&2
    exit 1
fi
echo "make-netboot-initramfs: kernel $KVER -> $OUT"

STAGE="$(mktemp -d /tmp/netclone-initrd.XXXXXX)"
trap 'rm -rf "$STAGE"' EXIT

mkdir -p "$STAGE"/{bin,sbin,etc,usr/bin,usr/sbin,usr/local/bin,lib,lib64,proc,sys,dev,run,tmp,newroot,var/log}
chmod 1777 "$STAGE/tmp"

# copy_bin <absolute-or-name> [fixed-path]
# Copies a binary plus the shared libraries reported by ldd.
copy_bin() {
    local name="$1"
    local src="${2:-}"
    if [ -z "$src" ]; then
        src="$(command -v "$name" || true)"
    fi
    if [ -z "$src" ] || [ ! -e "$src" ]; then
        echo "make-netboot-initramfs: missing $name" >&2
        exit 1
    fi
    local dst="$STAGE$src"
    mkdir -p "$(dirname "$dst")"
    cp -L "$src" "$dst"
    # ldd exits non-zero for static binaries; never let that abort the build.
    ldd "$src" 2>/dev/null | awk '{ for (i = 1; i <= NF; i++) if ($i ~ /^\//) print $i }' | while read -r lib; do
        if [ -e "$STAGE$lib" ]; then continue; fi
        mkdir -p "$STAGE$(dirname "$lib")"
        cp -L "$lib" "$STAGE$lib"
    done || true
}

# Static shell and core utilities.
copy_bin busybox /bin/busybox
for c in sh ls cat echo sleep mount umount mkdir rmdir ln cp mv rm dmesg \
         ifconfig route ip halt poweroff reboot sync cut grep sed awk sort \
         head tail wc date mdev uname ps kill; do
    if [ ! -e "$STAGE/bin/$c" ]; then ln -sf /bin/busybox "$STAGE/bin/$c"; fi
done

# kmod gives us modalias aware modprobe (busybox's does not do aliases).
copy_bin kmod
KMOD_PATH="$(command -v kmod)"
for c in modprobe insmod rmmod lsmod depmod; do
    if [ ! -e "$STAGE/sbin/$c" ]; then ln -sf "$KMOD_PATH" "$STAGE/sbin/$c"; fi
done

# Tools used by the agent.
copy_bin netclone-client /usr/local/bin/netclone-client
copy_bin udp-receiver
copy_bin zstd
copy_bin sgdisk

# ------------------------------------------------------------------ modules
copy_mods() {
    local rel="$1"
    local abs="/lib/modules/$KVER/$rel"
    [ -e "$abs" ] || return 0
    mkdir -p "$STAGE/lib/modules/$KVER"
    ( cd / && cp -a --parents "lib/modules/$KVER/$rel" "$STAGE/" )
}

for rel in \
    kernel/drivers/net \
    kernel/drivers/ata \
    kernel/drivers/scsi \
    kernel/drivers/nvme \
    kernel/drivers/usb \
    kernel/drivers/mmc \
    kernel/drivers/virtio \
    kernel/drivers/pci \
    kernel/crypto \
    kernel/lib \
    kernel/arch/x86/lib ; do
    copy_mods "$rel"
done

# Firmware needed by common NICs.
for fw in rtl_nic bnx2 bnx2x e100; do
    if [ -d "/lib/firmware/$fw" ]; then
        mkdir -p "$STAGE/lib/firmware"
        cp -a "/lib/firmware/$fw" "$STAGE/lib/firmware/"
    fi
done

# depmod inside the staging tree so modules.dep / modules.alias are correct.
depmod -b "$STAGE" "$KVER" 2>/dev/null || true

# -------------------------------------------------------------------- files
cp "$SHARE/init" "$STAGE/init"
chmod 755 "$STAGE/init"
cp "$SHARE/udhcpc.script" "$STAGE/etc/udhcpc.script"
chmod 755 "$STAGE/etc/udhcpc.script"
printf 'netclone\n' > "$STAGE/etc/hostname"
cat > "$STAGE/etc/passwd" <<'EOF'
root:x:0:0:root:/root:/bin/sh
EOF
cat > "$STAGE/etc/group" <<'EOF'
root:x:0:
EOF

# ------------------------------------------------------------------- pack
mkdir -p "$(dirname "$OUT")"
( cd "$STAGE" && find . -print0 | cpio --null -o -H newc --owner 0:0 --quiet ) | gzip -9 > "$OUT"
echo "make-netboot-initramfs: wrote $(du -h "$OUT" | cut -f1) to $OUT"
