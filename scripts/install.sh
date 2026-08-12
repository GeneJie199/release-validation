#!/usr/bin/env sh
set -eu

SOURCE=${1:-./releaseguard}
CHECKSUMS=${2:-}
PREFIX=${PREFIX:-/usr/local}
STATE_DIR=${RELEASEGUARD_STATE_DIR:-/var/lib/releaseguard}

if [ ! -f "$SOURCE" ]; then
  echo "binary not found: $SOURCE" >&2
	echo "usage: sudo ./scripts/install.sh [path-to-releaseguard] [checksums.txt]" >&2
  exit 1
fi

if [ -n "$CHECKSUMS" ]; then
	if [ ! -f "$CHECKSUMS" ]; then
		echo "checksum file not found: $CHECKSUMS" >&2
		exit 1
	fi
	NAME=$(basename "$SOURCE")
	EXPECTED=$(awk -v name="$NAME" '$2 == name || $2 == "*" name { print $1; exit }' "$CHECKSUMS")
	if [ -z "$EXPECTED" ]; then
		echo "checksum entry not found for $NAME" >&2
		exit 1
	fi
	if command -v sha256sum >/dev/null 2>&1; then
		ACTUAL=$(sha256sum "$SOURCE" | awk '{print $1}')
	else
		ACTUAL=$(shasum -a 256 "$SOURCE" | awk '{print $1}')
	fi
	if [ "$ACTUAL" != "$EXPECTED" ]; then
		echo "SHA-256 verification failed for $SOURCE" >&2
		exit 1
	fi
	echo "verified SHA-256 for $NAME"
fi

install -d -m 0755 "$PREFIX/bin"
install -d -m 0750 "$STATE_DIR"
install -m 0755 "$SOURCE" "$PREFIX/bin/releaseguard"
echo "installed $PREFIX/bin/releaseguard"
echo "state directory: $STATE_DIR"
