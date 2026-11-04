#!/usr/bin/env bash
# Builds netclone-<version>-amd64.iso from source.
#
# Runs on Debian/Ubuntu (as root) or inside the netclone-builder container.
# See build/build-iso.ps1 for the Windows wrapper.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

VERSION="$(sed -n 's/^const Version = "\(.*\)"/\1/p' internal/server/server.go | head -n1)"
[ -n "${VERSION:-}" ] || VERSION="dev"
OUT="$ROOT/out"

missing=0
for tool in go lb debootstrap xorriso mksquashfs; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        echo "ERROR: required tool not found: $tool" >&2
        missing=1
    fi
done
if [ "$missing" -ne 0 ]; then
    echo "Install live-build, debootstrap, xorriso, squashfs-tools and Go, or use build/build-iso.ps1 (Docker)." >&2
    exit 1
fi

echo "==> NetClone $VERSION: building server and client"
BIN="$ROOT/build/live/config/includes.chroot/usr/local/bin"
SHARE="$ROOT/build/live/config/includes.chroot/usr/local/share/netclone"
DOCS="$ROOT/build/live/config/includes.chroot/usr/share/doc/netclone"
mkdir -p "$BIN" "$SHARE" "$DOCS"

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" \
    -o "$BIN/netclone-server" ./cmd/netclone-server
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" \
    -o "$BIN/netclone-client" ./cmd/netclone-client
chmod 0755 "$BIN/netclone-server" "$BIN/netclone-client"

echo "==> staging netboot helpers"
install -m 0755 build/netboot/init "$SHARE/init"
install -m 0755 build/netboot/udhcpc.script "$SHARE/udhcpc.script"
install -m 0755 build/make-netboot-initramfs.sh "$SHARE/make-netboot-initramfs.sh"
chmod 0755 build/live/config/includes.chroot/usr/local/bin/netclone-prepare-assets
chmod 0755 build/live/config/includes.chroot/etc/xdg/openbox/autostart
chmod 0755 build/live/config/hooks/live/*.hook.chroot

echo "==> staging documentation"
cp -f README.md "$DOCS/"
cp -rf docs "$DOCS/"

echo "==> live-build"
cd "$ROOT/build/live"
# Remove previous build output without touching the checked-in configuration
# (config/includes.chroot holds our files).
rm -rf chroot binary .build \
       config/binary config/chroot config/bootstrap config/common config/source
rm -f live-image-amd64.hybrid.iso live-image-amd64.iso
sh auto/config
lb build

ISO=""
for f in live-image-amd64.hybrid.iso live-image-amd64.iso; do
    if [ -f "$f" ]; then ISO="$f"; break; fi
done
if [ -z "$ISO" ]; then
    echo "ERROR: live-build did not produce an ISO" >&2
    exit 1
fi

mkdir -p "$OUT"
TARGET="$OUT/netclone-$VERSION-amd64.iso"
cp -f "$ISO" "$TARGET"
( cd "$OUT" && sha256sum "$(basename "$TARGET")" > "$(basename "$TARGET").sha256" )

echo ""
echo "==> done"
ls -lh "$TARGET"
echo "USB'ye yazmak icin: build/make-usb.ps1 (Windows) veya dd (Linux)."
