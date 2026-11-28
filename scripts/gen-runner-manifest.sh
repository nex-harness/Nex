#!/usr/bin/env bash
set -euo pipefail

DIR="${1:-dist/runners}"
OUT_FILE="${OUT_FILE:-${DIR}/manifest.json}"
VERSION="${VERSION:-dev}"
COMMIT_SHA="${COMMIT_SHA:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}"
MIN_PROTOCOL_VERSION="${MIN_PROTOCOL_VERSION:-1}"
REQUIRED_CAPABILITIES="${REQUIRED_CAPABILITIES:-}"
RELEASE_BASE_URL="${RELEASE_BASE_URL:-}"
BUILT_AT="$(date +%s)"

if [ ! -d "$DIR" ]; then
  echo "error: directory $DIR does not exist" >&2
  exit 1
fi

sha256_file() {
  local f="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$f" | awk '{print $1}'
  else
    shasum -a 256 "$f" | awk '{print $1}'
  fi
}

size_file() {
  local f="$1"
  wc -c < "$f" | tr -d '[:space:]'
}

json_escape() {
  printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'
}

PLATFORMS=(
  "linux/amd64:nex-runner-linux-amd64"
  "linux/arm64:nex-runner-linux-arm64"
  "darwin/arm64:nex-runner-darwin-arm64"
  "darwin/amd64:nex-runner-darwin-amd64"
  "windows/amd64:nex-runner-windows-amd64.exe"
)

VER_PREFIX=""
if [ -n "$VERSION" ] && [ "$VERSION" != "dev" ]; then
  VER_PREFIX="v${VERSION#v}-"
fi

CAPS_JSON="[]"
if [ -n "$REQUIRED_CAPABILITIES" ]; then
  CAPS_JSON="["
  first_cap=1
  IFS=',' read -ra CAP_ARR <<< "$REQUIRED_CAPABILITIES"
  for c in "${CAP_ARR[@]}"; do
    c_trimmed="$(printf '%s' "$c" | xargs)"
    [ -z "$c_trimmed" ] && continue
    if [ "$first_cap" -eq 0 ]; then
      CAPS_JSON+=", "
    fi
    CAPS_JSON+="\"$(json_escape "$c_trimmed")\""
    first_cap=0
  done
  CAPS_JSON+="]"
fi

BINARIES_JSON=""
first_bin=1
for item in "${PLATFORMS[@]}"; do
  plat="${item%%:*}"
  base_fname="${item#*:}"
  fname="${base_fname}"
  if [ -n "$VER_PREFIX" ] && [ -f "${DIR}/nex-runner-${VER_PREFIX}${base_fname#nex-runner-}" ]; then
    fname="nex-runner-${VER_PREFIX}${base_fname#nex-runner-}"
  fi
  fpath="${DIR}/${fname}"
  if [ ! -f "$fpath" ]; then
    continue
  fi
  hash="$(sha256_file "$fpath")"
  sz="$(size_file "$fpath")"
  rel_url=""
  if [ -n "$RELEASE_BASE_URL" ]; then
    rel_url="${RELEASE_BASE_URL%/}/${fname}"
  fi
  if [ "$first_bin" -eq 0 ]; then
    BINARIES_JSON+=$',\n'
  fi
  BINARIES_JSON+="    \"${plat}\": {"
  BINARIES_JSON+=$'\n'"      \"filename\": \"$(json_escape "$fname")\","
  BINARIES_JSON+=$'\n'"      \"sha256\": \"${hash}\","
  BINARIES_JSON+=$'\n'"      \"size_bytes\": ${sz}"
  if [ -n "$rel_url" ]; then
    BINARIES_JSON+=","$'\n'"      \"release_url\": \"$(json_escape "$rel_url")\""
  fi
  BINARIES_JSON+=$'\n'"    }"
  first_bin=0
done

TMP_OUT="${OUT_FILE}.tmp.$$"
{
  printf '{\n'
  printf '  "version": "%s",\n' "$(json_escape "$VERSION")"
  printf '  "commit_sha": "%s",\n' "$(json_escape "$COMMIT_SHA")"
  printf '  "min_protocol_version": %s,\n' "$MIN_PROTOCOL_VERSION"
  if [ "$CAPS_JSON" != "[]" ]; then
    printf '  "required_capabilities": %s,\n' "$CAPS_JSON"
  fi
  printf '  "built_at": %s,\n' "$BUILT_AT"
  if [ -n "$RELEASE_BASE_URL" ]; then
    printf '  "release_base_url": "%s",\n' "$(json_escape "${RELEASE_BASE_URL%/}")"
  fi
  printf '  "binaries": {\n'
  if [ -n "$BINARIES_JSON" ]; then
    printf '%s\n' "$BINARIES_JSON"
  fi
  printf '  }\n'
  printf '}\n'
} > "$TMP_OUT"
mv "$TMP_OUT" "$OUT_FILE"
echo "==> Generated runner manifest at $OUT_FILE (version=${VERSION})"
