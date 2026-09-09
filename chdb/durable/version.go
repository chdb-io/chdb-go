package durable

import (
	"regexp"
	"strconv"
	"strings"
)

// chDB release precedence.
//
// The V1 engine gate compares versions, and it compares them by release
// precedence rather than as strings. The difference is not academic: as
// strings, "26.10.0" < "26.7.0" and "26.7.2-rc.2" > "26.7.2", both of which
// are backwards, and both of which would let a reader open an object it cannot
// actually restore.
//
// The ordering is semver's:
//
//	26.7.2-rc.1  <  26.7.2-rc.2  <  26.7.2  <  26.7.3  <  26.8.1  <  27.0.0
//
// A pre-release sorts *below* the release it leads to, which is what makes an
// object written by 26.7.2-rc.2 — the release that first exported the durable
// ABI, and what chdb_version() reports there — readable by 26.7.2 and
// everything after it.
//
// The parser is semver-shaped rather than a match on the two shapes chDB
// happens to ship today. A future 26.7.2-beta.1 sorts correctly here, where a
// tighter pattern would refuse an object it could have opened safely — the
// wrong place to fail closed. Failing closed belongs where nothing can be
// concluded: a string that does not parse at all is refused rather than
// guessed at, because an unrecognised version is not evidence of
// compatibility, and treating it as one is how a reader restores an archive
// from a release nothing has tested it against.

// parsedVersion is a version string decomposed for comparison.
type parsedVersion struct {
	// release holds the numeric components, e.g. [26, 7, 2]. At least one.
	release []int64
	// prerelease holds the dot-separated identifiers, e.g. ["rc", "2"], with
	// numeric ones flagged. Nil for a final release, which sorts above every
	// pre-release of the same numbers.
	prerelease []preIdent
}

type preIdent struct {
	text    string
	number  int64
	numeric bool
}

// Numeric release, optional -prerelease, optional +build. Build metadata is
// parsed and ignored, as semver requires: it takes no part in precedence.
var versionPattern = regexp.MustCompile(`^(\d+(?:\.\d+)*)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$`)

// parseVersion decomposes a chDB version string, reporting whether it is one.
func parseVersion(value string) (parsedVersion, bool) {
	m := versionPattern.FindStringSubmatch(strings.TrimSpace(value))
	if m == nil {
		return parsedVersion{}, false
	}
	var release []int64
	for _, part := range strings.Split(m[1], ".") {
		n, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return parsedVersion{}, false
		}
		release = append(release, n)
	}
	var prerelease []preIdent
	if m[2] != "" {
		for _, id := range strings.Split(m[2], ".") {
			if n, err := strconv.ParseInt(id, 10, 64); err == nil && !strings.HasPrefix(id, "-") {
				prerelease = append(prerelease, preIdent{text: id, number: n, numeric: true})
			} else {
				prerelease = append(prerelease, preIdent{text: id})
			}
		}
	}
	return parsedVersion{release: release, prerelease: prerelease}, true
}

func comparePrerelease(a, b []preIdent) int {
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		// A shorter identifier list sorts lower when all shared parts are
		// equal: rc.1 comes before rc.1.1.
		if i >= len(a) {
			return -1
		}
		if i >= len(b) {
			return 1
		}
		x, y := a[i], b[i]
		if x.numeric && y.numeric {
			if x.number != y.number {
				if x.number < y.number {
					return -1
				}
				return 1
			}
			continue
		}
		// Numeric identifiers always sort below alphanumeric ones.
		if x.numeric != y.numeric {
			if x.numeric {
				return -1
			}
			return 1
		}
		if x.text != y.text {
			if x.text < y.text {
				return -1
			}
			return 1
		}
	}
	return 0
}

// comparePrecedence returns a negative number if a precedes b, zero if they
// are the same release, and a positive number otherwise.
func comparePrecedence(a, b parsedVersion) int {
	n := len(a.release)
	if len(b.release) > n {
		n = len(b.release)
	}
	for i := 0; i < n; i++ {
		// A missing component is zero, so 26.7 and 26.7.0 are the same
		// release.
		var x, y int64
		if i < len(a.release) {
			x = a.release[i]
		}
		if i < len(b.release) {
			y = b.release[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	if a.prerelease == nil && b.prerelease == nil {
		return 0
	}
	// No pre-release outranks any pre-release of the same numbers.
	if a.prerelease == nil {
		return 1
	}
	if b.prerelease == nil {
		return -1
	}
	return comparePrerelease(a.prerelease, b.prerelease)
}

// CompareEngineVersions orders two chDB version strings by release
// precedence, refusing either one it cannot parse rather than guessing.
//
// The returned int is negative when a precedes b, zero when they are the same
// release, and positive otherwise.
func CompareEngineVersions(a, b string) (int, error) {
	pa, okA := parseVersion(a)
	pb, okB := parseVersion(b)
	if !okA || !okB {
		bad := a
		if okA {
			bad = b
		}
		return 0, newError(CategoryEngineIncompatible,
			"durable: cannot order chdb version %q by release precedence, so compatibility "+
				"cannot be established; refusing rather than guessing", bad)
	}
	return comparePrecedence(pa, pb), nil
}

// maxEngineVersion returns the later of two version strings by precedence.
func maxEngineVersion(a, b string) (string, error) {
	cmp, err := CompareEngineVersions(a, b)
	if err != nil {
		return "", err
	}
	if cmp >= 0 {
		return a, nil
	}
	return b, nil
}
