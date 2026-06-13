#!/usr/bin/env bash
#
# macos-sign.sh — sign and notarize a darwin Mach-O produced by GoReleaser.
#
# Invoked as a per-build post hook from .goreleaser.yaml:
#     bash scripts/macos-sign.sh "{{ .Os }}" "{{ .Path }}"
#
# It signs the binary in place with a Developer ID Application certificate and
# the hardened runtime, then notarizes it with Apple. Because a bare CLI Mach-O
# (and a .tar.gz) cannot be stapled, we rely on Apple's online notarization
# check: notarizing registers the binary's code-signing hash with Apple, and the
# identical signed binary shipped inside the archive is recognized at run time.
#
# Credentials are passed as paths via environment variables, set by the release
# workflow (.github/workflows/release.yml):
#     MACOS_SIGN_P12_FILE           Developer ID Application certificate (.p12/PFX)
#     MACOS_SIGN_P12_PASSWORD_FILE  file containing the .p12 password
#     MACOS_NOTARY_API_KEY_FILE     App Store Connect API key JSON
#                                   (from `rcodesign encode-app-store-connect-api-key`)
#
# The hook no-ops for non-darwin targets, and skips signing entirely when no
# credentials are present — so `goreleaser build --snapshot` works locally
# without any secrets.
set -euo pipefail

os="${1:?usage: macos-sign.sh <os> <binary-path>}"
bin="${2:?usage: macos-sign.sh <os> <binary-path>}"

# Only darwin binaries are Mach-O; everything else is a no-op.
[ "$os" = "darwin" ] || exit 0

p12="${MACOS_SIGN_P12_FILE:-}"
pw="${MACOS_SIGN_P12_PASSWORD_FILE:-}"
api="${MACOS_NOTARY_API_KEY_FILE:-}"

if [ -z "$p12" ] || [ ! -f "$p12" ]; then
  echo "macos-sign: no signing credentials present — skipping sign/notarize for $bin"
  exit 0
fi

echo "macos-sign: signing $bin (Developer ID + hardened runtime)"
rcodesign sign \
  --p12-file "$p12" \
  --p12-password-file "$pw" \
  --code-signature-flags runtime \
  "$bin"

if [ -z "$api" ] || [ ! -f "$api" ]; then
  echo "macos-sign: WARNING: no notary API key present — $bin is signed but NOT notarized"
  exit 0
fi

echo "macos-sign: notarizing $bin (submitting to Apple, waiting for result)"
tmp="$(mktemp -d)"
zip="$tmp/$(basename "$bin").zip"
# Apple's notary service takes a container, not a bare Mach-O; zip the binary.
( cd "$(dirname "$bin")" && zip -q -X "$zip" "$(basename "$bin")" )
rcodesign notary-submit --api-key-file "$api" --wait "$zip"
rm -rf "$tmp"
echo "macos-sign: done — $bin signed + notarized"
