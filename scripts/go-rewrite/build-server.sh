#!/usr/bin/env bash
# Cross-compile go/cmd/nex-server for the five release targets (step 2.11 of
# docs/specs/go-rewrite/phase-2-go-server.md).
#
# Output layout: go/dist/<os>-<arch>/nex-server[.exe]. That is the same "one binary per platform
# under dist/sea/" shape the TypeScript release produces (scripts/build-sea.mjs writes
# packages/server/dist/sea/nex-server-<target>), so a packaging step that already knows how to pick
# up a SEA binary picks this up without a new layout.
#
#   linux-amd64    linux-arm64    darwin-arm64    darwin-amd64    windows-amd64
#
# Reproducibility is a REQUIREMENT here, not a nicety: the release publishes a sha256 per target and
# a rebuild that differs would look like a tampered artifact. Four flags produce it:
#
#   -trimpath         removes the absolute build directory from every path in the binary, so the
#                     same source builds the same bytes on any machine and in any CI runner
#   -buildvcs=false    stops the toolchain from stamping commit/time/dirty state, which changes
#                     every commit and cannot be reproduced from a source tarball at all
#   SOURCE_DATE_EPOCH  pinned to the latest commit's committer time, so anything derived from the
#                     build time (the Go build ID, timestamps in the DWARF data) is fixed too
#   -ldflags "-s -w"   drops the symbol table and DWARF: smaller artifact, and no local source
#                     paths to leak in a released binary
#
# CGO_ENABLED=0 on every target. The Go tree uses no cgo, and a cgo-free static binary is what lets
# the same artifact run on debian:bookworm-slim (acceptance item 4 of docker-single-image.md) with
# no libc compatibility story at all.
#
# Usage:
#   scripts/go-rewrite/build-server.sh                 # all five targets
#   scripts/go-rewrite/build-server.sh linux-amd64      # one target
#   DIST_DIR=/tmp/x scripts/go-rewrite/build-server.sh  # somewhere other than go/dist
#
# With no DIST_DIR the script also verifies REPRODUCIBILITY: after building, it rebuilds every
# target into a temporary directory and compares the digests. "Reproducible" is a property a release
# depends on (it publishes a sha256 per target), and asserting it here is what keeps a future
# dependency or toolchain change from quietly breaking it. Pass DIST_DIR to build somewhere specific
# and the second pass is skipped, since the caller owns that directory's contents.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO_DIR="$ROOT/go"
PKG="./cmd/nex-server"
# The version internal/server reports through GET /api/server-info, and the same value the Node
# bundle gets from tsup's __NEX_VERSION__ define. Read from the root package.json so a build cannot
# claim a version the UI does not report.
VERSION="$(node -p 'require(process.argv[1]).version' "$ROOT/package.json")"

# The import path the -X flags address. It has to be the real module path: a wrong one makes -X a
# silent no-op (the linker warns and drops the assignment), which would ship a binary reporting
# 0.0.0-dev with a version that looks correct in the build log.
VERSION_SYMBOL="github.com/tylinux/nex/go/internal/server.Version"

ALL_TARGETS=(linux-amd64 linux-arm64 darwin-arm64 darwin-amd64 windows-amd64)

if [ "$#" -gt 0 ]; then
  TARGETS=("$@")
else
  TARGETS=("${ALL_TARGETS[@]}")
fi

# sourceDateEpoch pins every time-derived value to the latest commit. Falling back to a fixed
# constant keeps a build from an uncommitted tree reproducible rather than nondeterministic — it
# just is not tied to anything, which is the honest answer for a tree with no commits.
source_date_epoch() {
  local stamp
  if stamp="$(git -C "$ROOT" log -1 --pretty=%ct 2>/dev/null)" && [ -n "$stamp" ]; then
    printf '%s' "$stamp"
  else
    printf '%s' "0"
  fi
}

SOURCE_DATE_EPOCH="$(source_date_epoch)"
export SOURCE_DATE_EPOCH

binary_name() { # <target> -> nex-server or nex-server.exe
  case "$1" in
  windows-*) printf 'nex-server.exe' ;;
  *) printf 'nex-server' ;;
  esac
}

size_human() { # bytes -> KiB with one decimal
  awk -v bytes="$1" 'BEGIN { printf "%.1f KiB", bytes / 1024 }'
}

if [ -n "${DIST_DIR:-}" ]; then
  # The caller owns this directory, so the second (verification) pass is skipped: it would build
  # into a second location the caller did not ask for, and the point of DIST_DIR is usually "put the
  # artifact exactly here and nowhere else".
  VERIFY_REPRODUCIBLE=0
else
  DIST_DIR="$GO_DIR/dist"
  VERIFY_REPRODUCIBLE=1
fi
rm -rf "$DIST_DIR"
mkdir -p "$DIST_DIR"

# build_one compiles a single target into $1. It prints the digest on stdout and nothing else, so
# the verification pass can compare two builds by diffing the two lists of digests.
build_one() {
  local out_dir="$1/$2"
  local os="${2%-*}"
  local arch="${2##*-}"
  local out="$out_dir/$(binary_name "$2")"
  mkdir -p "$out_dir"
  (
    cd "$GO_DIR" &&
      CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
        go build \
        -trimpath \
        -buildvcs=false \
        -ldflags "-s -w -X $VERSION_SYMBOL=$VERSION" \
        -o "$out" \
        "$PKG"
  ) >/dev/null 2>&1
}

# digest_of prints one binary's sha256, degrading to an empty string when no digest tool exists.
digest_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | cut -d' ' -f1
  else
    printf ''
  fi
}

echo "nex-server $VERSION (SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH)"
printf '%-14s %-12s %20s  %s\n' TARGET SIZE SHA256 PATH

failures=0
for target in "${TARGETS[@]}"; do
  os="${target%-*}"
  arch="${target##*-}"
  case "$os/$arch" in
  linux/amd64 | linux/arm64 | darwin/arm64 | darwin/amd64 | windows/amd64) ;;
  *)
    echo "unsupported target: $target" >&2
    echo "known targets: ${ALL_TARGETS[*]}" >&2
    exit 2
    ;;
  esac

  if ! build_one "$DIST_DIR" "$target"; then
    echo "build failed: $target" >&2
    failures=$((failures + 1))
    continue
  fi

  out="$DIST_DIR/$target/$(binary_name "$target")"
  size="$(wc -c <"$out" | tr -d ' ')"
  digest="$(digest_of "$out")"
  printf '%-14s %-12s %20s  %s\n' "$target" "$(size_human "$size")" "${digest:0:20}" "$out"
done

if [ "$failures" -gt 0 ]; then
  echo "$failures target(s) failed to build" >&2
  exit 1
fi

echo "built ${#TARGETS[@]} target(s) into $DIST_DIR"

# The static-linking assertion belongs to this script rather than to a separate check: CGO_ENABLED=0
# is an instruction above, and if a future dependency reintroduces cgo this is where it shows up,
# before a release ships a binary that will not start on debian-slim (acceptance item 4 of
# docker-single-image.md).
#
# Only the HOST target can be inspected — ldd on a foreign architecture either refuses or reports
# something meaningless — so the check runs when the host's own target is among the ones built.
host_target=""
case "$(uname -s)-$(uname -m)" in
Linux-x86_64) host_target="linux-amd64" ;;
Linux-aarch64 | Linux-arm64) host_target="linux-arm64" ;;
Darwin-arm64) host_target="darwin-arm64" ;;
Darwin-x86_64) host_target="darwin-amd64" ;;
esac
host_bin="$DIST_DIR/$host_target/nex-server"
if [ -n "$host_target" ] && [ -x "$host_bin" ]; then
  case "$(uname -s)" in
  Linux)
    # ldd prints "not a dynamic executable" (on stdout, exit 1) for a static binary. Anything else
    # on stdout is a shared-object line and means a library the release image would have to ship.
    if ldd "$host_bin" 2>/dev/null | grep -qv 'not a dynamic executable'; then
      echo "WARNING: $host_bin is not statically linked:" >&2
      ldd "$host_bin" >&2
    fi
    ;;
  Darwin)
    # otool -L on a Go binary without cgo lists nothing but the loader stub.
    if [ -n "$(otool -L "$host_bin" 2>/dev/null | tail -n +2 | grep -v '^$')" ]; then
      echo "WARNING: $host_bin links libraries:" >&2
      otool -L "$host_bin" >&2
    fi
    ;;
  esac
fi

# ---- reproducibility ------------------------------------------------------------------------
#
# The second pass is what makes "reproducible" a checked property rather than a claim. It rebuilds
# every target into a throwaway directory and compares digests; any difference means the flags above
# have stopped being sufficient (a new dependency, a toolchain change, an absolute path that leaked
# back in through -trimpath), and a release that publishes a sha256 per target would then be
# publishing a digest nobody can reproduce.
#
# Skipped when the caller passed DIST_DIR, because then the caller owns that directory and did not
# ask for a second one.
if [ "$VERIFY_REPRODUCIBLE" -eq 1 ]; then
  echo
  echo "verifying reproducibility (second build into a temporary directory)"
  verify_dir="$(mktemp -d "${TMPDIR:-/tmp}/nex-server-repro.XXXXXX")"
  trap 'rm -rf "$verify_dir"' EXIT

  first_list="$verify_dir/first.txt"
  second_list="$verify_dir/second.txt"
  : >"$first_list"
  : >"$second_list"

  for target in "${TARGETS[@]}"; do
    name="$(binary_name "$target")"
    digest_of "$DIST_DIR/$target/$name" >>"$first_list"
  done
  for target in "${TARGETS[@]}"; do
    if ! build_one "$verify_dir" "$target"; then
      echo "REPRODUCIBILITY: the second build of $target failed" >&2
      exit 1
    fi
    name="$(binary_name "$target")"
    digest_of "$verify_dir/$target/$name" >>"$second_list"
  done

  if cmp -s "$first_list" "$second_list" && [ -s "$first_list" ]; then
    echo "reproducible: two builds of ${#TARGETS[@]} target(s) produced identical digests"
  else
    echo "NOT REPRODUCIBLE: two builds of the same commit produced different binaries" >&2
    paste "$first_list" "$second_list" >&2
    exit 1
  fi
fi