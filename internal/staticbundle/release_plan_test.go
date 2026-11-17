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

func TestSelectedReleaseIdentities(t *testing.T) {
	p, _ := selectedPlan(t)
	if p.Prefix != "go.chdb.io/chdb" || p.SiteOrigin != "https://go.chdb.io" || p.StaticVersion != "v0.261101.1" || p.Release.Bindings != "v2.5.0" || p.Release.Arrow != "v0.1.0" || p.Validation.Bindings != "v2.5.0-rc.2" || p.Validation.Arrow != "v0.1.0-rc.1" {
		t.Fatalf("unexpected selected release: %+v", p)
	}
	for _, phase := range []string{"validation", "release"} {
		prefix, versions, err := p.Select(phase)
		if err != nil {
			t.Fatal(err)
		}
		b := Bundle{Prefix: prefix, Bindings: versions.Bindings, Engine: p.Engine, Version: p.StaticVersion}
		if err = p.CheckBundle(b, phase); err != nil {
			t.Fatal(err)
		}
		if phase == "validation" && p.CheckBundle(b, "release") == nil {
			t.Fatal("validation snapshot accepted as public stable engine")
		}
	}
	if _, _, err := p.Select("latest"); err == nil {
		t.Fatal("unrecognized phase accepted")
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
	p, _ := selectedPlan(t)
	b := Bundle{Prefix: p.Prefix, Bindings: p.Validation.Bindings, Engine: p.Engine, Version: p.StaticVersion}
	if err := p.CheckBundle(b, "release"); err == nil || !strings.Contains(err.Error(), "validation snapshot") {
		t.Fatalf("stable engine can pin RC minimum dependency: %v", err)
	}
}
