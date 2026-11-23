#!/usr/bin/env bash
# Sync the app version into mygo.json and clients/web/package.json so that
# desktop, Android and iOS bundles carry the same version as the release tag.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

VERSION="${1:-}"
if [ -z "$VERSION" ]; then
  echo "Usage: $0 <version>" >&2
  exit 1
fi

if [[ "$(uname -s)" == "Darwin" ]]; then
  sed -i '' -E "s/(\"version\": *\")[^\"]*(\")/\1${VERSION}\2/" mygo.json
  sed -i '' -E "s/^  \"version\": \"[^\"]*\"/  \"version\": \"${VERSION}\"/" clients/web/package.json
else
  sed -i -E "s/(\"version\": *\")[^\"]*(\")/\1${VERSION}\2/" mygo.json
  sed -i -E "s/^  \"version\": \"[^\"]*\"/  \"version\": \"${VERSION}\"/" clients/web/package.json
fi

echo "==> App version set to ${VERSION}"
grep -m1 '"version"' mygo.json
grep -m1 '"version"' clients/web/package.json
