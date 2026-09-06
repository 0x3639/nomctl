#!/usr/bin/env bash
# nomctl bootstrap: detect architecture, download the latest GitHub release,
# verify its sha256 and install the binary. Usage:
#   curl -fsSL https://raw.githubusercontent.com/0x3639/nomctl/main/install.sh | sudo bash
# Environment overrides: NOMCTL_REPO (owner/name), NOMCTL_VERSION (tag),
# NOMCTL_INSTALL_DIR (default /usr/local/bin).
set -euo pipefail

REPO="${NOMCTL_REPO:-0x3639/nomctl}"
VERSION="${NOMCTL_VERSION:-latest}"
INSTALL_DIR="${NOMCTL_INSTALL_DIR:-/usr/local/bin}"

if [[ $EUID -ne 0 ]]; then
  echo "install.sh must be run as root (try: curl ... | sudo bash)" >&2
  exit 1
fi
if [[ "$(uname -s)" != "Linux" ]]; then
  echo "nomctl only supports Linux" >&2
  exit 1
fi
case "$(uname -m)" in
  x86_64)        ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
for tool in curl tar sha256sum; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 1; }
done

if [[ "$VERSION" == "latest" ]]; then
  VERSION="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest")"
  VERSION="${VERSION##*/}"
fi
[[ "$VERSION" == v* ]] || VERSION="v$VERSION"

ARCHIVE="nomctl_${VERSION#v}_linux_${ARCH}.tar.gz"
BASE="https://github.com/$REPO/releases/download/$VERSION"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "Downloading nomctl $VERSION ($ARCH)..."
curl -fsSL -o "$TMP/$ARCHIVE" "$BASE/$ARCHIVE"
curl -fsSL -o "$TMP/checksums.txt" "$BASE/checksums.txt"

echo "Verifying checksum..."
(cd "$TMP" && grep " $ARCHIVE\$" checksums.txt | sha256sum -c --quiet -)

tar -xzf "$TMP/$ARCHIVE" -C "$TMP" nomctl
install -m 0755 "$TMP/nomctl" "$INSTALL_DIR/nomctl"
echo "Installed $("$INSTALL_DIR/nomctl" --version) to $INSTALL_DIR/nomctl"
echo "Next: sudo nomctl deploy && sudo nomctl start   (or just: sudo nomctl)"
