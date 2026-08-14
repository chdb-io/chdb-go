package enginetag

import (
	"strconv"
	"strings"
	"testing"
)

// compare orders two versions the way Go's module resolution does. It covers only
// the shapes Module emits — major 0, an encoded minor, a patch, and an optional
// all-numeric rc.N.M prerelease — which is why it can be this short: no
// alphanumeric prerelease identifier can turn up, so every field compares
// numerically, and a version with a prerelease sorts below the same version
// without one. Checked against golang.org/x/mod/semver on this file's table
// before being committed; that package is not imported because chdb-go should
// not grow a dependency for one assertion.
func compare(a, b string) int {
	numsA, preA := fields(a)
	numsB, preB := fields(b)
	for i := range numsA {
		if numsA[i] != numsB[i] {
			return sign(numsA[i] - numsB[i])
		}
	}
	switch {
	case len(preA) == 0 && len(preB) == 0:
		return 0
	case len(preA) == 0:
		return 1 // a release outranks a prerelease of the same version
	case len(preB) == 0:
		return -1
	}
	for i := 0; i < len(preA) && i < len(preB); i++ {
		if preA[i] != preB[i] {
			return sign(preA[i] - preB[i])
		}
	}
	return sign(len(preA) - len(preB))
}

// fields splits v into its three numeric version fields and the numeric part of
// an rc.N.M prerelease, failing loudly on anything else so the comparator is
// never quietly applied to a shape it does not order correctly.
func fields(v string) (nums []int, pre []int) {
	core, prerelease, _ := strings.Cut(strings.TrimPrefix(v, "v"), "-")
	for _, part := range strings.Split(core, ".") {
		n, err := strconv.Atoi(part)
		if err != nil {
			panic("compare: not a numeric version field in " + v)
		}
		nums = append(nums, n)
	}
	if len(nums) != 3 {
		panic("compare: expected three version fields in " + v)
	}
	if prerelease == "" {
		return nums, nil
	}
	ids := strings.Split(prerelease, ".")
	if ids[0] != "rc" {
		panic("compare: only an rc prerelease is ordered here, got " + v)
	}
	for _, id := range ids[1:] {
		n, err := strconv.Atoi(id)
		if err != nil {
			panic("compare: non-numeric rc identifier in " + v)
		}
		pre = append(pre, n)
	}
	return nums, pre
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

func TestModule(t *testing.T) {
	for _, c := range []struct {
		engine  string
		counter int
		want    string
	}{
		{"v26.7.0", 1, "v0.260700.1"},
		{"v26.7.0", 2, "v0.260700.2"},
		{"v26.7.0", 17, "v0.260700.17"},
		{"v26.5.0", 1, "v0.260500.1"},
		{"v26.5.1", 1, "v0.260501.1"},
		{"v26.11.3", 1, "v0.261103.1"},
		{"v27.1.0", 1, "v0.270100.1"},
		{"v26.7.3-rc.1", 1, "v0.260703.0-rc.1.1"},
		{"v26.7.3-rc.1", 4, "v0.260703.0-rc.1.4"},
		{"v26.5.1-rc.3", 1, "v0.260501.0-rc.3.1"},
	} {
		got, err := Module(c.engine, c.counter)
		if err != nil {
			t.Errorf("Module(%q, %d) returned %v", c.engine, c.counter, err)
			continue
		}
		if got != c.want {
			t.Errorf("Module(%q, %d) = %q, want %q", c.engine, c.counter, got, c.want)
		}
	}
}

func TestModuleRejects(t *testing.T) {
	for _, c := range []struct {
		why     string
		engine  string
		counter int
	}{
		{"counter starts at 1", "v26.7.0", 0},
		{"a negative counter", "v26.7.0", -1},
		{"no leading v", "26.7.0", 1},
		{"only two fields", "v26.7", 1},
		{"a suffix that is not an rc", "v26.7.0-beta.1", 1},
		{"an rc with no number", "v26.7.0-rc", 1},
		{"an unnumbered stable suffix, which is chdb-node's spelling", "v26.7.0-stable.1", 1},
		{"a minor above the two-digit field", "v26.100.0", 1},
		{"a patch above the two-digit field", "v26.7.100", 1},
		{"trailing whitespace, which a shell variable picks up easily", "v26.7.0 ", 1},
	} {
		if got, err := Module(c.engine, c.counter); err == nil {
			t.Errorf("Module(%q, %d) accepted %s and returned %q", c.engine, c.counter, c.why, got)
		}
	}
}

// The two directions have to agree, or a tag could be checked against
// engine_data.go and pass while naming a different engine.
func TestRoundTrip(t *testing.T) {
	for _, engine := range []string{
		"v26.7.0", "v26.5.0", "v26.5.1", "v26.11.3", "v27.1.0",
		"v26.7.3-rc.1", "v26.5.1-rc.3", "v26.99.99", "v26.0.0",
	} {
		for _, counter := range []int{1, 2, 99} {
			module, err := Module(engine, counter)
			if err != nil {
				t.Fatalf("Module(%q, %d): %v", engine, counter, err)
			}
			gotEngine, gotCounter, err := Engine(module)
			if err != nil {
				t.Errorf("Engine(%q): %v", module, err)
				continue
			}
			if gotEngine != engine || gotCounter != counter {
				t.Errorf("Engine(%q) = (%q, %d), want (%q, %d)",
					module, gotEngine, gotCounter, engine, counter)
			}
		}
	}
}

func TestEngineRejects(t *testing.T) {
	for _, c := range []struct {
		why    string
		module string
	}{
		{"a counter of 0", "v0.260700.0"},
		{"an rc counter of 0", "v0.260703.0-rc.1.0"},
		{"an rc written with a non-zero patch field", "v0.260703.1-rc.1.1"},
		{"a minor too small to hold an engine version", "v0.1.1"},
		{"a major other than 0, which the module path forbids", "v1.260700.1"},
		{"the engine version written out instead of encoded", "v26.7.0"},
		{"chdb-node's spelling", "v0.260700.1-stable.1"},
		{"an rc with no packaging counter", "v0.260703.0-rc.1"},
	} {
		if engine, counter, err := Engine(c.module); err == nil {
			t.Errorf("Engine(%q) accepted %s and returned (%q, %d)", c.module, c.why, engine, counter)
		}
	}
}

// Every version this scheme produces has to sort the way Go's module resolution
// will sort it: within an engine by packaging, an rc engine below every stable
// packaging of the same version, and any engine below the next one. Checked by
// construction rather than by comparing strings, since Go compares the fields
// numerically and the prerelease identifiers piecewise.
func TestOrderIsAscending(t *testing.T) {
	ordered := []struct {
		engine  string
		counter int
	}{
		{"v26.7.2", 1},
		{"v26.7.2", 2},
		{"v26.7.3-rc.1", 1},
		{"v26.7.3-rc.1", 2},
		{"v26.7.3-rc.2", 1},
		{"v26.7.3", 1},
		{"v26.7.3", 2},
		{"v26.8.0-rc.1", 1},
		{"v26.8.0", 1},
	}
	prev := ""
	for _, c := range ordered {
		got, err := Module(c.engine, c.counter)
		if err != nil {
			t.Fatalf("Module(%q, %d): %v", c.engine, c.counter, err)
		}
		if prev != "" && compare(prev, got) >= 0 {
			t.Errorf("%s (%s packaging %d) does not sort above %s", got, c.engine, c.counter, prev)
		}
		prev = got
	}
}
