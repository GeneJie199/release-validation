#!/usr/bin/env sh
set -eu

ACTION=${1:-install}
case "$ACTION" in
  install|doctor|uninstall|purge) shift ;;
  *) ACTION=install ;;
esac

SOURCE=${1:-./releaseguard}
CHECKSUMS=${2:-}
PREFIX=${PREFIX:-/usr/local}
STATE_DIR=${RELEASEGUARD_STATE_DIR:-/var/lib/releaseguard}
BINARY="$PREFIX/bin/releaseguard"

verify_checksum() {
  [ -n "$CHECKSUMS" ] || return 0
  [ -f "$CHECKSUMS" ] || { echo "checksum file not found: $CHECKSUMS" >&2; exit 1; }
  name=$(basename "$SOURCE")
  expected=$(awk -v name="$name" '$2 == name || $2 == "*" name { print $1; exit }' "$CHECKSUMS")
  [ -n "$expected" ] || { echo "checksum entry not found for $name" >&2; exit 1; }
  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$SOURCE" | awk '{print $1}')
  else
    actual=$(shasum -a 256 "$SOURCE" | awk '{print $1}')
  fi
  [ "$actual" = "$expected" ] || { echo "SHA-256 verification failed for $SOURCE" >&2; exit 1; }
  echo "verified SHA-256 for $name"
}

case "$ACTION" in
  install)
    [ -f "$SOURCE" ] || { echo "binary not found: $SOURCE" >&2; echo "usage: sudo ./scripts/install.sh install [releaseguard] [checksums.txt]" >&2; exit 1; }
    verify_checksum
    install -d -m 0755 "$PREFIX/bin"
    install -d -m 0750 "$STATE_DIR"
    install -m 0755 "$SOURCE" "$BINARY"
    echo "installed $BINARY"
    echo "state directory: $STATE_DIR"
    "$BINARY" version
    ;;
  doctor)
    [ -x "$BINARY" ] || { echo "releaseguard is not installed at $BINARY" >&2; exit 1; }
    if [ -f "$STATE_DIR/release-report.json" ]; then
      "$BINARY" doctor --report "$STATE_DIR/release-report.json" --state "$STATE_DIR/releaseguard-runs.db"
    else
      "$BINARY" version
      echo "[WARN] report: create a release report before running the full doctor"
    fi
    ;;
  uninstall)
    rm -f "$BINARY"
    echo "removed $BINARY; preserved $STATE_DIR"
    ;;
  purge)
    case "$STATE_DIR" in
      */releaseguard|*/releaseguard/*) ;;
      *) echo "refusing to purge unexpected state directory: $STATE_DIR" >&2; exit 1 ;;
    esac
    [ "${RELEASEGUARD_CONFIRM_PURGE:-}" = "$STATE_DIR" ] || { echo "set RELEASEGUARD_CONFIRM_PURGE to the exact state directory" >&2; exit 1; }
    rm -rf "$STATE_DIR"
    echo "purged $STATE_DIR"
    ;;
esac
