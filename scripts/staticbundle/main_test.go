package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCLI(t *testing.T, args ...string) error {
	t.Helper()
	old := os.Args
	os.Args = append([]string{"staticbundle"}, args...)
	defer func() { os.Args = old }()
	return mainErr()
}

func planArgs(t *testing.T) []string {
	t.Helper()
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return []string{"-repo", repo, "-publication-plan", "static-publication.json"}
}

func TestPublicCLIRequiresNoticeInputsBeforePackaging(t *testing.T) {
	args := append([]string{"package"}, planArgs(t)...)
	args = append(args, "-phase", "public-rc", "-archive", "unused.tar.gz", "-out", filepath.Join(t.TempDir(), "unused"))
	if err := runCLI(t, args...); err == nil || !strings.Contains(err.Error(), "requires prepared -notices") {
		t.Fatalf("public RC notice gate not applied before native packaging: %v", err)
	}
}

// The checked-in plan pins an RC engine, so the stable release phase must
// refuse it before any packaging starts.
func TestReleaseCLIRefusesRCEngine(t *testing.T) {
	args := append([]string{"package"}, planArgs(t)...)
	args = append(args, "-phase", "release", "-notices", t.TempDir(), "-archive", "unused.tar.gz", "-out", filepath.Join(t.TempDir(), "unused"))
	if err := runCLI(t, args...); err == nil || !strings.Contains(err.Error(), "release needs a stable engine") {
		t.Fatalf("release phase accepted an RC engine: %v", err)
	}
}

// Uploads must name a public phase; the default validation phase never
// reaches the permanent namespace, not even by being re-checked as release.
func TestBlobCLIRequiresExplicitPublicPhase(t *testing.T) {
	bundle := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundle, "bundle.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target.json")
	if err := os.WriteFile(target, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"blob-plan", "blob-site", "blob-publish", "publish"} {
		args := append([]string{command}, planArgs(t)...)
		args = append(args, "-bundle", bundle, "-blob-target", target)
		if err := runCLI(t, args...); err == nil || !strings.Contains(err.Error(), "pass -phase public-rc or -phase release") {
			t.Fatalf("%s ran in the validation phase: %v", command, err)
		}
	}
}

func TestProbeUsesPermanentDomainEvenInValidationPhase(t *testing.T) {
	out := filepath.Join(t.TempDir(), "probe")
	args := append([]string{"probe"}, planArgs(t)...)
	args = append(args, "-out", out)
	if err := runCLI(t, args...); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(out, "probe.json"))
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Prefix string `json:"module_prefix"`
	}
	if err = json.Unmarshal(data, &p); err != nil || p.Prefix != "go.chdb.io/chdb" {
		t.Fatalf("probe selected CI/old namespace: %s, %v", data, err)
	}
}

func TestPlanCLIRejectsConflictingOverrides(t *testing.T) {
	args := append([]string{"release-plan"}, planArgs(t)...)
	args = append(args, "-bindings-version", "v2.4.0")
	if err := runCLI(t, args...); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("explicit override silently changed selected versions: %v", err)
	}
}
