#!/usr/bin/env bash
# Build the committed root module through a local Go module proxy, then link
# an application outside the checkout against an independently supplied .a.
set -euo pipefail

: "${CHDB_STATIC_DIR:?set CHDB_STATIC_DIR to the extracted static release directory}"
test -s "$CHDB_STATIC_DIR/libchdb.a"
test -s "$CHDB_STATIC_DIR/chdb.h"
case "$CHDB_STATIC_DIR" in
  /*) ;;
  *) echo 'CHDB_STATIC_DIR must be absolute' >&2; exit 2 ;;
esac

module=github.com/chdb-io/chdb-go/v2
root=$(git rev-parse --show-toplevel)
cd "$root"
version="v2.99.99-$(git rev-parse --short HEAD)"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
proxy="$work/proxy"
consumer="$work/consumer"
mkdir -p "$proxy/$module/@v" "$consumer"

# Go excludes nested modules from the root module's zip. Match that layout,
# and package HEAD rather than the working tree to catch unpublished files.
nested=()
while IFS= read -r modfile; do
  nested+=(":(exclude)$(dirname "$modfile")/*")
done < <(git ls-files '*/go.mod')
git archive --format=zip --prefix="$module@$version/" HEAD \
  ${nested[@]+"${nested[@]}"} -o "$proxy/$module/@v/$version.zip"
git show HEAD:go.mod > "$proxy/$module/@v/$version.mod"
printf '{"Version":"%s"}\n' "$version" > "$proxy/$module/@v/$version.info"
printf '%s\n' "$version" > "$proxy/$module/@v/list"

listing=$(unzip -l "$proxy/$module/@v/$version.zip")
for path in 'chdb-purego/binding_static.go' 'chdb-purego/static_unsupported.go'; do
  if ! grep -Fq "$path" <<< "$listing"; then
    echo "static module zip is missing $path" >&2
    exit 1
  fi
done
if grep -Eq 'libchdb\.(a|so|dylib|zst)' <<< "$listing"; then
  echo 'static engine bytes leaked into the root module zip' >&2
  exit 1
fi

cd "$consumer"
go mod init example.com/chdb-static-consumer
# The source lives in the packaged module. No Go workspace or replace may
# turn this back into an in-repository build.
export GOWORK=off
export GOPROXY="file://$proxy,${GOPROXY:-https://proxy.golang.org},direct"
export GOSUMDB=off
export GOFLAGS=-mod=mod
go get "$module@$version"
cp "$root/.github/testdata/static-consumer.go" main.go

export CGO_ENABLED=1
export CGO_CFLAGS="-DCHDB_STATIC_HEADER=\"$CHDB_STATIC_DIR/chdb.h\""
case "$(uname -s)-$(uname -m)" in
  Darwin-arm64|Darwin-x86_64) export CGO_LDFLAGS="-Wl,-force_load,$CHDB_STATIC_DIR/libchdb.a" ;;
  Linux-x86_64|Linux-aarch64)
    export CGO_LDFLAGS_ALLOW='-Wl,(--whole-archive|--no-whole-archive)|-l:libchdb[.]a'
    export CGO_LDFLAGS="-L$CHDB_STATIC_DIR" ;;
  *) echo 'unsupported static consumer platform' >&2; exit 2 ;;
esac
ulimit -c 0
go build -trimpath -tags chdb_static,chdb_static_manual -o consumer .
for path in "$root" "$consumer" "$CHDB_STATIC_DIR"; do
  if LC_ALL=C grep -a -q -F "$path" consumer; then
    echo "static consumer contains build path: $path" >&2
    exit 1
  fi
done
case "$(uname -s)" in
  Darwin)
    otool -L consumer
    if otool -L consumer | grep -Eq 'libchdb\.(so|dylib)'; then exit 1; fi ;;
  Linux)
    readelf -d consumer
    if readelf -d consumer | grep -Eq '\(NEEDED\).*libchdb\.so'; then exit 1; fi ;;
esac
env -u CHDB_LIB_PATH ./consumer | grep -qx '26.11.1:42'
echo 'static module-proxy consumer passed'
