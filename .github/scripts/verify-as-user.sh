#!/usr/bin/env bash
#
# Build this commit into a module archive the way the Go module proxy would,
# then consume it from a throwaway module outside the repository — the same
# steps a user runs after `go get`.
#
# Testing inside the repository proves less than it looks. It compiles working
# tree files that may never have been committed, links against artifacts that
# only exist on the build machine, and can pass while the archive users
# actually download is missing a file or carries a path that exists nowhere
# else. This script closes that gap:
#
#   * the archive is produced by `git archive`, so only committed content is in
#     it — an untracked or ignored file that the build needs fails here;
#   * the consumer module lives outside the repository and resolves everything
#     through a file-backed module proxy, so nothing is picked up by accident;
#   * the binary is built with -trimpath and then scanned for build-machine
#     paths, so a leaked absolute path fails here rather than on a user's
#     machine;
#   * the engine is placed beside the built binary and no environment variable
#     is set, so the default resolution order is what gets exercised.
#
# Usage: .github/scripts/verify-as-user.sh
# Run from the repository root. Needs git, go, curl and tar.

set -euo pipefail

# unzip is checked here rather than where it is used, because the only use is
# inside an `if` condition, where a missing command is a false answer instead of
# an error and the check it guards would silently pass.
for tool in git go curl tar unzip; do
	command -v "$tool" >/dev/null || {
		echo "missing required tool: $tool" >&2
		exit 1
	}
done

MODULE_PATH="github.com/chdb-io/chdb-go/v2"
REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

# A version that is unique per commit. The module cache is keyed by
# module@version, so reusing one string across differing archive contents
# serves stale code and produces failures that look like compiler bugs.
VERSION="v2.99.99-$(git rev-parse --short HEAD)"

# Two runs on the same commit produce the same module version, and the module
# cache would serve the first run's copy — including a broken one, which then
# looks like the fix did not work. CI starts with an empty cache; this is what
# makes a local run behave the same way. Only this module's entries are removed.
MODCACHE="$(go env GOMODCACHE)"
if [ -n "$MODCACHE" ]; then
	for stale in "$MODCACHE/cache/download/$MODULE_PATH/@v" "$MODCACHE/$MODULE_PATH@$VERSION"; do
		[ -e "$stale" ] || continue
		chmod -R u+w "$stale" 2>/dev/null || true
		rm -rf "$stale"
	done
fi

WORK="$(mktemp -d)"
trap 'chmod -R u+w "$WORK" 2>/dev/null || true; rm -rf "$WORK"' EXIT

PROXY="$WORK/proxy"
CONSUMER="$WORK/consumer"
mkdir -p "$PROXY/$MODULE_PATH/@v" "$CONSUMER"

# A directory with its own go.mod belongs to a different module, and the proxy
# leaves those out of the parent module's archive. git archive does not know
# that, and a zip carrying lib/*/go.mod is rejected on unzip with "go.mod file
# not in module root directory" — so the exclusion has to be reproduced here or
# this script fails on an archive no user could ever receive.
#
# The list is derived rather than written out, so adding a platform module needs
# no edit here.
nested=()
while IFS= read -r modfile; do
	nested+=(":(exclude)$(dirname "$modfile")/*")
done < <(git ls-files '*/go.mod')

echo "==> Packaging $MODULE_PATH@$VERSION from committed content"
# ${nested[@]+...} keeps an empty list from tripping set -u on bash 3.2, which
# is what /bin/bash still is on the macOS runners.
git archive --format=zip --prefix="$MODULE_PATH@$VERSION/" HEAD \
	${nested[@]+"${nested[@]}"} \
	-o "$PROXY/$MODULE_PATH/@v/$VERSION.zip"
git show HEAD:go.mod >"$PROXY/$MODULE_PATH/@v/$VERSION.mod"
printf '{"Version":"%s"}\n' "$VERSION" >"$PROXY/$MODULE_PATH/@v/$VERSION.info"
echo "$VERSION" >"$PROXY/$MODULE_PATH/@v/list"

# Listed once, into a variable, because both checks below are the kind that must
# not be able to answer "no" by accident. Under pipefail, `unzip -l | grep -q`
# reports the status of a pipeline in which grep closes the pipe on its first
# match: unzip can then be killed by SIGPIPE, the pipeline is non-zero, and the
# `if` reads as "nothing found" precisely when something was. It needs a listing
# larger than a pipe buffer to happen, which this archive is not yet, so this is
# closing it before it can rather than after it did.
ARCHIVE_LISTING="$(unzip -l "$PROXY/$MODULE_PATH/@v/$VERSION.zip")"

# The engine is a release artifact, never part of the module. If it ever shows
# up in the archive, the module has grown a few hundred megabytes by accident.
if echo "$ARCHIVE_LISTING" | grep -qE 'libchdb\.(so|dylib|zst)'; then
	echo "FAIL: the module archive contains a libchdb binary" >&2
	exit 1
fi

if echo "$ARCHIVE_LISTING" | grep -qE '/lib/[^/]+/go\.mod'; then
	echo "FAIL: the module archive contains a nested module; the proxy excludes those" >&2
	exit 1
fi

echo "==> Writing a consumer module in $CONSUMER"
cat >"$CONSUMER/go.mod" <<EOF
module example.com/chdb-consumer

go 1.21
EOF

cat >"$CONSUMER/main.go" <<'EOF'
package main

import (
	"fmt"
	"os"

	"github.com/chdb-io/chdb-go/v2/chdb"
	chdbpurego "github.com/chdb-io/chdb-go/v2/chdb-purego"
)

func main() {
	path, err := chdbpurego.LoadedLibraryPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "load failed:", err)
		os.Exit(1)
	}
	fmt.Println("engine:", path)

	res, err := chdb.Query("SELECT 41 + 1", "CSV")
	if err != nil {
		fmt.Fprintln(os.Stderr, "query failed:", err)
		os.Exit(1)
	}
	fmt.Print("result: ", res.String())
}
EOF

# The local proxy only carries this module; third-party dependencies fall
# through to the next entry in the chain.
export GOPROXY="file://$PROXY,${GOPROXY:-https://proxy.golang.org},direct"
export GOSUMDB=off
export GOFLAGS=-mod=mod

echo "==> go get, as a user would"
cd "$CONSUMER"
go get "$MODULE_PATH@$VERSION"
grep -q "$MODULE_PATH $VERSION" go.mod || {
	echo "FAIL: go.mod does not reference the packaged version" >&2
	cat go.mod >&2
	exit 1
}

echo "==> go build -trimpath"
go build -trimpath -o consumer .

echo "==> Checking for build-machine paths in the binary"
leaked=0
for pattern in "$REPO_ROOT" "$CONSUMER" "$WORK" "${HOME:-/nonexistent-home}"; do
	[ -n "$pattern" ] || continue
	if grep -a -q -F -- "$pattern" consumer; then
		echo "FAIL: binary embeds the build-machine path $pattern" >&2
		leaked=1
	fi
done
[ "$leaked" -eq 0 ] || exit 1

echo "==> Placing the engine beside the binary and running with no env set"
"$REPO_ROOT/update_libchdb.sh" >/dev/null
ls libchdb.so >/dev/null

output="$(env -u CHDB_LIB_PATH ./consumer)"
echo "$output"

case "$output" in
*"engine: $CONSUMER/libchdb.so"* | *"engine: /private$CONSUMER/libchdb.so"*) ;;
*)
	echo "FAIL: the engine beside the binary was not the one resolved" >&2
	exit 1
	;;
esac

case "$output" in
*"result: 42"*) ;;
*)
	echo "FAIL: query did not return 42" >&2
	exit 1
	;;
esac

echo "==> Checking the diagnostic when no engine can be found"
mv libchdb.so engine-moved-away
if env -u CHDB_LIB_PATH ./consumer >"$WORK/notfound.txt" 2>&1; then
	echo "FAIL: loading succeeded with no engine present" >&2
	exit 1
fi
cat "$WORK/notfound.txt"
for want in "libchdb not found" "next to executable" "system path" "CHDB_LIB_PATH"; do
	grep -q -- "$want" "$WORK/notfound.txt" || {
		echo "FAIL: diagnostic does not mention $want" >&2
		exit 1
	}
done

echo "==> OK: this commit is usable as a published module"
