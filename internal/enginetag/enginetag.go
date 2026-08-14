// Package enginetag holds the one definition of how a platform engine module's
// version is derived from the chdb-core release it carries.
//
// The version has to say which engine is inside, because that is the only thing
// a user of lib/<platform> cares about and the only thing that changes what the
// module does. It also has to count repackagings of that same engine: the module
// carries extraction code as well as a payload, so fixing that code needs a new
// version even though the engine has not moved.
//
//	engine v26.7.0       packaging 1  ->  v0.260700.1
//	engine v26.7.0       packaging 2  ->  v0.260700.2
//	engine v26.7.3-rc.1  packaging 1  ->  v0.260703.0-rc.1.1
//
// This mirrors the scheme chdb-node publishes its @chdb/lib-<platform>
// subpackages under (26.7.0-stable.1, 26.7.3-rc.1.1), as closely as Go permits.
// It cannot be the engine version itself: Go requires a /vN path suffix for
// major versions of two or greater, and the engine's major is 26, so tagging
// v26.7.0 would demand the module path lib/<platform>/v26 and a new path every
// time ClickHouse bumps its major. Keeping the major at zero and encoding the
// engine into the minor field leaves the path alone forever.
//
// The counter starts at 1 rather than 0, matching chdb-node, so "the first
// packaging of this engine" reads the same in both ecosystems.
//
// An rc engine encodes as a prerelease of patch 0, which sorts below every
// stable packaging of the same engine version and above every packaging of the
// one before it:
//
//	v0.260702.5  <  v0.260703.0-rc.1.1  <  v0.260703.0-rc.2.1  <  v0.260703.1
//
// Getting this wrong is expensive in a way it is not on npm: the module proxy
// caches a tag permanently and refuses to serve different bytes for it, so a
// mistagged module cannot be corrected, only superseded. Hence one definition
// here, exercised by enginetag_test.go, called by scripts/package-engine.sh
// rather than reimplemented in it.
package enginetag

import (
	"fmt"
	"regexp"
	"strconv"
)

// The engine tags chdb-core publishes: v26.7.0, and v26.5.1-rc.3 for a
// prerelease. Anything else is refused rather than guessed at, because the
// guess would be published under a name that cannot be taken back.
var engineRe = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)(?:-rc\.(\d+))?$`)

// The module versions this package produces, parsed back apart.
var moduleRe = regexp.MustCompile(`^v0\.(\d+)\.(\d+)(?:-rc\.(\d+)\.(\d+))?$`)

// Module returns the version to tag lib/<platform> with for an engine release
// and a packaging counter.
func Module(engine string, counter int) (string, error) {
	m := engineRe.FindStringSubmatch(engine)
	if m == nil {
		return "", fmt.Errorf("engine release %q is neither vX.Y.Z nor vX.Y.Z-rc.N; "+
			"if chdb-core has started tagging some other way, teach this package the new shape", engine)
	}
	if counter < 1 {
		return "", fmt.Errorf("packaging counter is %d; it counts packagings of %s and starts at 1", counter, engine)
	}

	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	patch, _ := strconv.Atoi(m[3])

	// Two digits each. A wider field would silently collide — engine 26.7.0 and
	// 26.70.0 would both encode to the same number — and a collision here means
	// two different engines published under one permanent version.
	if minor > 99 || patch > 99 {
		return "", fmt.Errorf("engine %s has a minor or patch above 99, which the "+
			"two-digit encoding cannot represent without colliding with another release", engine)
	}
	encoded := major*10000 + minor*100 + patch

	if m[4] != "" {
		rc, _ := strconv.Atoi(m[4])
		return fmt.Sprintf("v0.%d.0-rc.%d.%d", encoded, rc, counter), nil
	}
	return fmt.Sprintf("v0.%d.%d", encoded, counter), nil
}

// Engine is the inverse: the chdb-core release a module version says it carries,
// and which packaging of it this is. It exists so a tag can be checked against
// the Version recorded in the module's generated engine_data.go, instead of the
// two being trusted to agree.
func Engine(module string) (engine string, counter int, err error) {
	m := moduleRe.FindStringSubmatch(module)
	if m == nil {
		return "", 0, fmt.Errorf("module version %q does not look like one this scheme produces", module)
	}

	encoded, _ := strconv.Atoi(m[1])
	// Below 10000 there is no major digit, so the version predates this scheme
	// or was written by hand.
	if encoded < 10000 {
		return "", 0, fmt.Errorf("module version %q encodes %d, which is too small to carry an engine version", module, encoded)
	}
	major, minor, patch := encoded/10000, (encoded/100)%100, encoded%100
	patchField, _ := strconv.Atoi(m[2])

	if m[3] != "" {
		rc, _ := strconv.Atoi(m[3])
		counter, _ = strconv.Atoi(m[4])
		if patchField != 0 {
			return "", 0, fmt.Errorf("module version %q is an rc packaging, which this scheme "+
				"writes with a patch field of 0 so it sorts below the stable packagings", module)
		}
		if counter < 1 {
			return "", 0, fmt.Errorf("module version %q has a packaging counter of %d, and it starts at 1", module, counter)
		}
		return fmt.Sprintf("v%d.%d.%d-rc.%d", major, minor, patch, rc), counter, nil
	}

	counter = patchField
	if counter < 1 {
		return "", 0, fmt.Errorf("module version %q has a packaging counter of %d, and it starts at 1", module, counter)
	}
	return fmt.Sprintf("v%d.%d.%d", major, minor, patch), counter, nil
}
