#!/usr/bin/env bash
# Test the checkout before its independently versioned modules are published.
# Call with an absolute, non-existing GOWORK path; never edit source go.mod files.
set -euo pipefail

repo="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
repo=$(cd "$repo" && pwd -P)
case "${GOWORK:-}" in
  /*) ;;
  *) echo 'Set GOWORK to a new absolute workspace file path' >&2; exit 1 ;;
esac

root=$(go mod edit -json "$repo/go.mod" | jq -er '.Module.Path')
version=$(go mod edit -json "$repo/v2/arrow/go.mod" | jq -er --arg root "$root" \
  '.Require[] | select(.Path == $root) | .Version')
go work init "$repo" "$repo/v2/arrow"
# A use directive alone still allows Go to request the required root version's
# module graph. Replace that exact unpublished version, not all versions of a
# workspace module (which Go rejects). Published-consumer tests use no replace.
go work edit "-replace=$root@$version=$repo"
