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

MODULE_PATH="github.com/chdb-io/chdb-go/v2"
REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

# A version that is unique per commit. The module cache is keyed by
# module@version, so reusing one string across differing archive contents
# serves stale code and produces failures that look like compiler bugs.
VERSION="v2.99.99-$(git rev-parse --short HEAD)"

WORK="$(mktemp -d)"
trap 'chmod -R u+w "$WORK" 2>/dev/null || true; rm -rf "$WORK"' EXIT

PROXY="$WORK/proxy"
CONSUMER="$WORK/consumer"
mkdir -p "$PROXY/$MODULE_PATH/@v" "$CONSUMER"

echo "==> Packaging $MODULE_PATH@$VERSION from committed content"
git archive --format=zip --prefix="$MODULE_PATH@$VERSION/" HEAD \
	-o "$PROXY/$MODULE_PATH/@v/$VERSION.zip"
git show HEAD:go.mod >"$PROXY/$MODULE_PATH/@v/$VERSION.mod"
printf '{"Version":"%s"}\n' "$VERSION" >"$PROXY/$MODULE_PATH/@v/$VERSION.info"
echo "$VERSION" >"$PROXY/$MODULE_PATH/@v/list"

# The engine is a release artifact, never part of the module. If it ever shows
# up in the archive, the module has grown a few hundred megabytes by accident.
if unzip -l "$PROXY/$MODULE_PATH/@v/$VERSION.zip" | grep -qE 'libchdb\.(so|dylib)'; then
	echo "FAIL: the module archive contains a libchdb binary" >&2
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
