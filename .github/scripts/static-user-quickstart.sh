#!/usr/bin/env bash
# Run the static-engine quickstart exactly as a user would: a new module,
# `go get <prefix>/static@latest`, the documented main.go, a tagged build, run.
#
#   static-user-quickstart.sh public [prefix]   # published modules, default GOPROXY/GOSUMDB
#   static-user-quickstart.sh local <bundle>    # an assembled, not yet published bundle
#
# local serves the bundle's own proxy directory ahead of proxy.golang.org, so the
# static modules are the exact bytes that would be uploaded while the root
# bindings come from their published release. Its checksums are not in
# sum.golang.org yet, so only the static prefix skips the checksum database.
set -euo pipefail
mode=${1:?usage: static-user-quickstart.sh public|local ...}
work=$(mktemp -d "${RUNNER_TEMP:-/tmp}/quickstart.XXXX")
for v in GOWORK GOFLAGS GOPROXY GOSUMDB GONOSUMDB GONOSUMCHECK GOPRIVATE GONOPROXY GOINSECURE GOTOOLCHAIN; do unset "$v"; done
export GOWORK=off GOTOOLCHAIN=local GOMODCACHE="$work/mod-cache" GOCACHE="$work/build-cache"
case "$mode" in
  public)
    prefix=${2:-go.chdb.io/chdb}
    want="" ;;
  local)
    bundle=$(cd "${2:?bundle directory required}" && pwd)
    prefix=$(jq -r .module_prefix "$bundle/bundle.json")
    want=$(jq -r .version "$bundle/bundle.json")
    # A partial bundle is never published; QUICKSTART_ALLOW_PARTIAL is for local debugging only.
    [ "${QUICKSTART_ALLOW_PARTIAL:-}" = 1 ] || test "$(jq -r .complete "$bundle/bundle.json")" = true
    export GOPROXY="file://$bundle/proxy,https://proxy.golang.org"
    export GONOSUMDB="${prefix%%/*}" ;;
  *) echo "unknown mode $mode" >&2; exit 2 ;;
esac

mkdir "$work/myapp"; cd "$work/myapp"
go mod init example.com/myapp
cat > main.go <<'GO'
package main

import (
	"fmt"

	"github.com/chdb-io/chdb-go/v2/chdb"
	_ "go.chdb.io/chdb/static" // static engine
)

func main() {
	r, err := chdb.Query("SELECT 42", "CSV")
	if err != nil {
		panic(err)
	}
	fmt.Print(r.String())
}
GO
if [ "$prefix" != go.chdb.io/chdb ]; then
  sed -i.bak "s#go.chdb.io/chdb/static#$prefix/static#" main.go && rm main.go.bak
fi

echo "+ go get $prefix/static@latest"
go get "$prefix/static@latest"
echo "+ go mod tidy"
go mod tidy
got=$(go list -m -f '{{.Version}}' "$prefix/static")
root=$(go list -m -f '{{.Version}}' github.com/chdb-io/chdb-go/v2)
echo "resolved $prefix/static $got with root $root"
if [ -n "$want" ] && [ "$got" != "$want" ]; then
  echo "@latest resolved $got, the bundle is $want" >&2
  exit 1
fi
echo "+ CGO_ENABLED=1 go build -tags chdb_static -o myapp ."
CGO_ENABLED=1 go build -tags chdb_static -o myapp .
out=$(./myapp)
echo "$out"
test "$out" = 42

# The same build from the module cache alone: no network, checksums still on.
GOPROXY=off GOFLAGS=-mod=mod CGO_ENABLED=1 go build -a -tags chdb_static -o myapp-offline .
test "$(./myapp-offline)" = 42
case "$(uname -s)" in
  Darwin) otool -L myapp ;;
  Linux) readelf -d myapp | grep NEEDED ;;
esac
ls -l myapp
echo "QUICKSTART PASSED ($mode, $prefix/static $got, $(go env GOOS)/$(go env GOARCH))"
