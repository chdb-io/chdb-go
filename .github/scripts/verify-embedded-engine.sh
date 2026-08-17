#!/usr/bin/env bash
#
# Package the engine for this machine's platform and run a program that carries
# it, with nothing installed on the system.
#
# This is the path a user gets once the platform modules are published, so it is
# checked here before anything is published. The engine is registered from the
# platform module's exported values — exactly what the wiring in the main module
# will do — so the seam between the two is exercised rather than described.
#
# What it asserts, beyond "a query returns the right answer":
#
#   * the library is extracted under a directory named by the payload digest,
#     which is what makes concurrent extraction safe without locking;
#   * a second run reuses that directory instead of writing a few hundred
#     megabytes again;
#   * many processes starting at once converge on one copy and leave no
#     temporary directories behind;
#   * the binary carries no path from the build machine;
#   * nothing is read from a system-wide install, because none is present.
#
# Usage: .github/scripts/verify-embedded-engine.sh
# Run from the repository root.

set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

GOOS_VAL="$(go env GOOS)"
GOARCH_VAL="$(go env GOARCH)"
PLATFORM="$GOOS_VAL-$GOARCH_VAL"
MODDIR="lib/$PLATFORM"

[ -d "$MODDIR" ] || {
	echo "SKIP: no engine module for $PLATFORM" >&2
	exit 0
}

ENGINE_TAG="${CHDB_CORE_TAG:-v26.7.0}"

# Any lib/<platform> tag sitting on this commit has to name the engine the module
# was generated from. Publishing is the one step with no undo — the module proxy
# serves a tag's bytes forever — so the check runs before the tag is pushed. It
# finds nothing in a normal CI run, where the checkout carries no tags, and that
# is the point: this is for the machine doing the publishing.
tags="$(git tag --points-at HEAD --list 'lib/*' 2>/dev/null || true)"
if [ -n "$tags" ]; then
	echo "==> Checking the engine tags on this commit"
	for tag in $tags; do
		go run ./scripts/enginetag -verify "$tag"
	done
fi

echo "==> Cross-compiling every platform module"
# Only the runner's own module is exercised by the rest of this script, so
# without this a mistake in one of the other three would surface whenever that
# platform is next packaged rather than in the change that caused it. The modules
# are pure Go, so building them for another platform costs seconds. GOWORK=off
# because each module must build on its own, from its own go.mod.
for dir in lib/*/; do
	name="$(basename "$dir")"
	case "$name" in
	*-*) ;;
	# lib/embedded is not a platform: its name carries no GOOS-GOARCH to build
	# for, and it covers all four through build tags. Built separately below.
	*)
		continue
		;;
	esac
	echo "    $name"
	(cd "$dir" && GOWORK=off GOOS="${name%%-*}" GOARCH="${name##*-}" go build ./...)
done

# The dispatch module, once per platform it dispatches to, plus one it does not:
# on an uncovered platform it must still build and simply register nothing.
if [ -d lib/embedded ]; then
	echo "    embedded"
	for pair in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
		(cd lib/embedded && GOWORK=off GOOS="${pair%/*}" GOARCH="${pair#*/}" go build ./...)
	done
fi

# Compression ratio is a property of a release, not of the machinery this script
# checks: the level changes neither the extracted library nor its digest, and the
# payload is still several times the part size, so splitting and reassembly are
# exercised either way. At the publishing level this step costs about a minute on
# a runner's four cores, which is most of what this job would spend and none of
# what it is checking.
export ZSTD_LEVEL="${ZSTD_LEVEL:-3}"

echo "==> Packaging chdb-core $ENGINE_TAG for $PLATFORM"
./scripts/package-engine.sh "$ENGINE_TAG" "$PLATFORM"

WORK="$(mktemp -d)"
trap 'chmod -R u+w "$WORK" 2>/dev/null || true; rm -rf "$WORK"' EXIT
CONSUMER="$WORK/consumer"
CACHE="$WORK/cache"
mkdir -p "$CONSUMER" "$CACHE"

# No require directives: the workspace below resolves both modules from the
# working tree. go mod tidy is deliberately not run, because it ignores the
# workspace and would try to fetch versions that are not published yet.
cat >"$CONSUMER/go.mod" <<EOF
module example.com/embedded-consumer

go 1.21
EOF

cat >"$CONSUMER/main.go" <<EOF
package main

import (
	"fmt"
	"os"

	"github.com/chdb-io/chdb-go/v2/chdb"
	chdbpurego "github.com/chdb-io/chdb-go/v2/chdb-purego"
	engine "github.com/chdb-io/chdb-go/lib/$PLATFORM"
)

// This mirrors the wiring the main module will carry once the platform modules
// are published: the payload is handed over at init, before anything can try to
// load an engine.
func init() {
	chdbpurego.RegisterEmbeddedEngine(chdbpurego.EmbeddedEngine{
		Version:  engine.Version,
		FileName: engine.FileName,
		Digest:   engine.Digest,
		Size:     engine.Size,
		Open:     engine.Open,
	})
}

func main() {
	path, err := chdbpurego.LoadedLibraryPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "load failed:", err)
		os.Exit(1)
	}
	fmt.Println("engine-version:", chdbpurego.EmbeddedEngineVersion())
	fmt.Println("engine-path:", path)

	res, err := chdb.Query("SELECT 41 + 1", "CSV")
	if err != nil {
		fmt.Fprintln(os.Stderr, "query failed:", err)
		os.Exit(1)
	}
	fmt.Print("result: ", res.String())
}
EOF

# A workspace stands in for the published modules. Nothing else in the build
# refers to the repository, so a missing export or a mistyped field fails here.
export GOWORK="$WORK/go.work"
go work init
go work use "$REPO_ROOT" "$REPO_ROOT/$MODDIR" "$CONSUMER"

echo "==> Building the consumer"
cd "$CONSUMER"
go build -trimpath -o consumer .

echo "==> Checking for build-machine paths in the binary"
for pattern in "$REPO_ROOT" "$WORK" "${HOME:-/nonexistent-home}"; do
	[ -n "$pattern" ] || continue
	if grep -a -q -F -- "$pattern" consumer; then
		echo "FAIL: binary embeds the build-machine path $pattern" >&2
		exit 1
	fi
done

digest="$(grep -oE 'Digest = "[0-9a-f]+"' "$REPO_ROOT/$MODDIR/engine_data.go" | cut -d'"' -f2)"
[ -n "$digest" ] || {
	echo "FAIL: no digest in $MODDIR/engine_data.go" >&2
	exit 1
}

echo "==> First run: extracts into a fresh cache"
first="$(env -u CHDB_LIB_PATH CHDB_CACHE_DIR="$CACHE" ./consumer)"
echo "$first"

case "$first" in
*"engine-version: $ENGINE_TAG"*) ;;
*)
	echo "FAIL: the running engine is not $ENGINE_TAG" >&2
	exit 1
	;;
esac
case "$first" in
*"result: 42"*) ;;
*)
	echo "FAIL: query did not return 42" >&2
	exit 1
	;;
esac
case "$first" in
*"/$digest/"*) ;;
*)
	echo "FAIL: the engine was not extracted under its digest $digest" >&2
	exit 1
	;;
esac

# No system-wide install exists in this job, so resolving anything outside the
# cache would mean the embedded payload was bypassed.
case "$first" in
*"engine-path: $CACHE/"* | *"engine-path: /private$CACHE/"*) ;;
*)
	echo "FAIL: the engine was loaded from outside the cache directory" >&2
	exit 1
	;;
esac

echo "==> Second run: reuses the extracted copy"
extracted="$(find "$CACHE" -name 'libchdb.*' -type f | head -1)"
before="$(date -r "$extracted" +%s 2>/dev/null || stat -c %Y "$extracted")"
sleep 1
env -u CHDB_LIB_PATH CHDB_CACHE_DIR="$CACHE" ./consumer >/dev/null
after="$(date -r "$extracted" +%s 2>/dev/null || stat -c %Y "$extracted")"
if [ "$before" != "$after" ]; then
	echo "FAIL: the engine was rewritten on the second run" >&2
	exit 1
fi

echo "==> Twelve concurrent cold starts write one copy between them"
rm -rf "$CACHE" && mkdir -p "$CACHE"

# Each in-progress extraction is a .tmp- directory holding a full copy of the
# engine, so counting them while the twelve run is a direct measure of how much
# disk this costs at its peak. Twelve at once is half a gigabyte each: enough to
# fail with ENOSPC on a machine with several gigabytes free, which is how the
# lock in extract.go came to exist. Sampling can only under-count, so a number
# above one is a real regression and never a flake.
peak_file="$WORK/peak-temp-dirs"
echo 0 >"$peak_file"
(
	peak=0
	while [ ! -e "$WORK/racers-done" ]; do
		n="$(find "$CACHE" -mindepth 1 -maxdepth 1 -name '.tmp-*' 2>/dev/null | wc -l | tr -d ' ')"
		[ "$n" -le "$peak" ] || {
			peak="$n"
			echo "$peak" >"$peak_file"
		}
		sleep 0.05
	done
) &
watcher=$!

pids=()
for _ in $(seq 12); do
	env -u CHDB_LIB_PATH CHDB_CACHE_DIR="$CACHE" ./consumer >/dev/null &
	pids+=($!)
done
failed=0
for pid in "${pids[@]}"; do
	wait "$pid" || failed=1
done
touch "$WORK/racers-done"
wait "$watcher" 2>/dev/null || true

[ "$failed" -eq 0 ] || {
	echo "FAIL: a concurrent cold start failed" >&2
	exit 1
}

peak="$(cat "$peak_file")"
echo "    peak concurrent extractions: $peak"
if [ "$peak" -gt 1 ]; then
	echo "FAIL: $peak processes extracted at once; each writes a full copy of the engine" >&2
	exit 1
fi

published="$(find "$CACHE" -mindepth 1 -maxdepth 1 -type d | wc -l | tr -d ' ')"
if [ "$published" != "1" ]; then
	echo "FAIL: expected one published directory, found $published" >&2
	find "$CACHE" -mindepth 1 -maxdepth 1 >&2
	exit 1
fi
leftovers="$(find "$CACHE" -mindepth 1 -maxdepth 1 -name '.tmp-*' | wc -l | tr -d ' ')"
if [ "$leftovers" != "0" ]; then
	echo "FAIL: $leftovers temporary directories were left behind" >&2
	exit 1
fi

echo "==> OK: a binary carrying its own engine works with nothing installed"
