# Publishing engine modules

For maintainers of the `lib/<platform>` engine modules described in [engine.md](engine.md).

```
scripts/package-engine.sh v26.7.0                    # every platform
CHDB_PACKAGING=2 scripts/package-engine.sh v26.7.0   # repackage the same engine
```

The script prints the exact `git tag` commands. Use those — the module proxy
serves a tag's bytes permanently, so a wrong tag can only be superseded, never
fixed. `internal/enginetag` defines the rule, `go run ./scripts/enginetag -verify
lib/<platform>/<version>` checks a tag against the module it names, and CI runs
that check on every `lib/**` tag pushed.

Payloads are added with `git add -f` at publish time and are not on the default
branch, so a clone does not carry every engine version ever shipped.

