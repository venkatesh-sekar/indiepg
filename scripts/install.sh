#!/bin/sh
# indiepg installer — downloads the latest release binary to /usr/local/bin,
# verifies its checksum, and hands off to `indiepg install` (provision Postgres
# + start the service).
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/venkatesh-sekar/indiepg/main/scripts/install.sh | sudo sh
#
# Knobs (environment variables):
#   INDIEPG_VERSION    release tag to install (default: latest)
#   INDIEPG_BIN_DIR    install dir for the binary (default: /usr/local/bin)
#   INDIEPG_NO_INSTALL if set, only install the binary; skip `indiepg install`
#
# Extra args are forwarded to `indiepg install`, e.g.:
#   ... | sudo sh -s -- --bind 127.0.0.1:9000

set -eu

REPO="venkatesh-sekar/indiepg"
BIN_DIR="${INDIEPG_BIN_DIR:-/usr/local/bin}"
VERSION="${INDIEPG_VERSION:-latest}"

say() { printf 'indiepg: %s\n' "$1" >&2; }
die() { printf 'indiepg: error: %s\n' "$1" >&2; exit 1; }

# --- preconditions --------------------------------------------------------
[ "$(id -u)" -eq 0 ] || die "must run as root — pipe to 'sudo sh', e.g. curl -fsSL <url> | sudo sh"

OS="$(uname -s)"
[ "$OS" = "Linux" ] || die "indiepg installs a native systemd Postgres and supports Linux only (got $OS)"

case "$(uname -m)" in
	x86_64 | amd64)  ARCH="amd64" ;;
	aarch64 | arm64) ARCH="arm64" ;;
	*) die "unsupported architecture: $(uname -m)" ;;
esac

# Pick a downloader. fetch() prints the body to stdout and EXITS NONZERO on any
# HTTP/transport error (curl -f / wget default), so callers can distinguish a
# network/rate-limit failure from a genuinely-missing release.
if command -v curl >/dev/null 2>&1; then
	dl() { curl -fsSL "$1" -o "$2"; }
	fetch() { curl -fsSL "$1"; }
elif command -v wget >/dev/null 2>&1; then
	dl() { wget -qO "$2" "$1"; }
	fetch() { wget -qO- "$1"; }
else
	die "need curl or wget to download the release"
fi

# --- resolve version ------------------------------------------------------
if [ "$VERSION" = "latest" ]; then
	say "resolving latest release..."
	API="https://api.github.com/repos/$REPO/releases/latest"
	# Capture the body and the fetch exit status SEPARATELY (not mid-pipeline,
	# where set -e would only see sed/head). This way a 403 rate-limit or a
	# network outage produces a distinct, actionable message.
	BODY="$(fetch "$API")" || die "could not reach the GitHub API — network down or rate-limited? Set INDIEPG_VERSION=vX.Y.Z to skip resolution, or build locally (see README)."
	VERSION="$(printf '%s\n' "$BODY" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)"
	[ -n "$VERSION" ] || die "no published release found for $REPO — set INDIEPG_VERSION, or build locally (see README)."
fi

ASSET="indiepg_linux_${ARCH}"
URL="https://github.com/$REPO/releases/download/${VERSION}/${ASSET}"

# --- download -------------------------------------------------------------
TMP="$(mktemp)" || die "could not create a temp file"
trap 'rm -f "$TMP"' EXIT

say "downloading ${ASSET} (${VERSION})..."
dl "$URL" "$TMP" || die "download failed: $URL
  - if you set INDIEPG_VERSION, check it matches a published tag
  - the release may not ship a binary for arch '${ARCH}'
  - or no release is published yet — build locally (see README)"

# --- verify integrity -----------------------------------------------------
# Best-effort: if the release ships a checksums file we REQUIRE a match; if it
# doesn't (or no sha256 tool is present) we warn and continue rather than block.
SUMS_URL="https://github.com/$REPO/releases/download/${VERSION}/indiepg_${VERSION}_checksums.txt"
if SUMS="$(fetch "$SUMS_URL" 2>/dev/null)"; then
	EXPECTED="$(printf '%s\n' "$SUMS" | awk -v n="$ASSET" '$2 == n || $2 == "*"n {print $1; exit}')"
	if [ -n "$EXPECTED" ]; then
		if command -v sha256sum >/dev/null 2>&1; then
			ACTUAL="$(sha256sum "$TMP" | awk '{print $1}')"
		elif command -v shasum >/dev/null 2>&1; then
			ACTUAL="$(shasum -a 256 "$TMP" | awk '{print $1}')"
		else
			ACTUAL=""
			say "warning: no sha256 tool found; skipping integrity check"
		fi
		if [ -n "$ACTUAL" ]; then
			[ "$EXPECTED" = "$ACTUAL" ] || die "checksum mismatch for ${ASSET} — refusing to install (expected ${EXPECTED}, got ${ACTUAL})"
			say "checksum verified"
		fi
	else
		say "warning: ${ASSET} not listed in checksums; skipping integrity check"
	fi
else
	say "warning: no checksums file for ${VERSION}; skipping integrity check"
fi

# --- install --------------------------------------------------------------
DEST="${BIN_DIR}/indiepg"
mkdir -p "$BIN_DIR" 2>/dev/null || true
if ! { install -m 0755 "$TMP" "$DEST" 2>/dev/null || { cp "$TMP" "$DEST" && chmod 0755 "$DEST"; }; }; then
	die "could not install to $DEST — is $BIN_DIR writable? (override with INDIEPG_BIN_DIR)"
fi
say "installed $DEST"

# The temp file is now redundant. Remove it and clear the trap, because the
# exec below replaces this shell and would otherwise bypass the EXIT trap.
rm -f "$TMP"
trap - EXIT

# --- hand off to install --------------------------------------------------
if [ -n "${INDIEPG_NO_INSTALL:-}" ]; then
	say "binary installed; skipping 'indiepg install' (INDIEPG_NO_INSTALL set)"
	say "run it yourself with: sudo indiepg install"
	exit 0
fi

say "running 'indiepg install'..."
exec "$DEST" install "$@"
