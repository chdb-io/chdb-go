package staticbundle

import (
	"path/filepath"
	"strings"
	"testing"
)

func selectedPlan(t *testing.T) (PublicationPlan, Release) {
	t.Helper()
	r, err := ReadRelease("../../static-release.json")
	if err != nil {
		t.Fatal(err)
	}
	p, err := ReadPublicationPlan("../../static-publication.json", r)
	if err != nil {
		t.Fatal(err)
	}
	return p, r
}

// stablePlan is the checked-in plan re-pinned to a stable engine, the shape the
// release phase needs once chdb-core ships one.
func stablePlan(t *testing.T) PublicationPlan {
	t.Helper()
	p, _ := selectedPlan(t)
	p.Engine, p.StaticVersion = "v26.11.2", "v0.261102.1"
	return p
}

func TestSelectedReleaseIdentities(t *testing.T) {
	p, _ := selectedPlan(t)
	if p.Prefix != "go.chdb.io/chdb" || p.SiteOrigin != "https://go.chdb.io" || p.StaticVersion != "v0.261102.0-rc.1.1" || p.Release.Bindings != "v2.5.0" || p.Release.Arrow != "v0.1.0" || p.Validation.Bindings != "v2.5.0-rc.2" || p.Validation.Arrow != "v0.1.0-rc.1" {
		t.Fatalf("unexpected selected release: %+v", p)
	}
	for _, c := range []struct {
		plan   PublicationPlan
		phase  string
		prefix string
		root   string
	}{
		{p, PhaseValidation, "static-ci.example/chdb", "v2.5.0-rc.2"},
		{p, PhasePublicRC, "go.chdb.io/chdb", "v2.5.0-rc.2"},
		{stablePlan(t), PhaseRelease, "go.chdb.io/chdb", "v2.5.0"},
	} {
		prefix, versions, err := c.plan.Select(c.phase)
		if err != nil {
			t.Fatalf("%s: %v", c.phase, err)
		}
		if prefix != c.prefix || versions.Bindings != c.root {
			t.Fatalf("%s selected %s with root %s", c.phase, prefix, versions.Bindings)
		}
		b := Bundle{Prefix: prefix, Bindings: versions.Bindings, Engine: c.plan.Engine, Version: c.plan.StaticVersion}
		if err = c.plan.CheckBundle(b, c.phase); err != nil {
			t.Fatalf("%s: %v", c.phase, err)
		}
		// No bundle passes as another phase: each has its own namespace or root.
		for _, other := range []string{PhaseValidation, PhasePublicRC, PhaseRelease} {
			if other != c.phase && c.plan.CheckBundle(b, other) == nil {
				t.Fatalf("%s bundle accepted as %s", c.phase, other)
			}
		}
	}
	if _, _, err := p.Select("latest"); err == nil {
		t.Fatal("unrecognized phase accepted")
	}
}

// A public RC never takes the stable identity, and a stable release never
// ships an RC engine.
func TestPublicPhasesMatchEngineStability(t *testing.T) {
	p, _ := selectedPlan(t)
	if _, _, err := p.Select(PhaseRelease); err == nil {
		t.Fatal("release phase accepted an RC engine")
	}
	if _, _, err := stablePlan(t).Select(PhasePublicRC); err == nil {
		t.Fatal("public-rc phase accepted a stable static version")
	}
	if !Public(PhasePublicRC) || !Public(PhaseRelease) || Public(PhaseValidation) || Public("") {
		t.Fatal("wrong set of public phases")
	}
}

func TestPublicationPlanRejectsDriftAndStableRCDependencies(t *testing.T) {
	for _, scenario := range []string{"engine", "static", "domain", "CI namespace", "root RC", "Arrow RC", "stable root", "stable Arrow"} {
		t.Run(scenario, func(t *testing.T) {
			p, r := selectedPlan(t)
			switch scenario {
			case "engine":
				p.Engine = "v26.11.2"
			case "static":
				p.StaticVersion = "v0.261101.2"
			case "domain":
				p.SiteOrigin = "https://other.example"
			case "CI namespace":
				p.Validation.Prefix = p.Prefix
			case "root RC":
				p.Validation.Bindings = "v2.6.0-rc.1"
			case "Arrow RC":
				p.Validation.Arrow = "v0.1.0-rc.01"
			case "stable root":
				p.Release.Bindings = "v2.5.0-rc.2"
			case "stable Arrow":
				p.Release.Arrow = "v0.1.0-rc.1"
			}
			filename := filepath.Join(t.TempDir(), "publication.json")
			if err := writeJSON(filename, p); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadPublicationPlan(filename, r); err == nil {
				t.Fatal("accepted inconsistent publication plan")
			}
		})
	}
}

func TestStableBundleRejectsRCMinimumDependency(t *testing.T) {
	p := stablePlan(t)
	b := Bundle{Prefix: p.Prefix, Bindings: p.Validation.Bindings, Engine: p.Engine, Version: p.StaticVersion}
	if err := p.CheckBundle(b, "release"); err == nil || !strings.Contains(err.Error(), "validation snapshot") {
		t.Fatalf("stable engine can pin RC minimum dependency: %v", err)
	}
}
