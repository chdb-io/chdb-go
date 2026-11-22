package staticbundle

import (
	"archive/zip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func snapshotFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	put(t, filepath.Join(repo, "go.mod"), []byte("module "+RootModule+"\n\ngo 1.21\n"))
	put(t, filepath.Join(repo, "root.go"), []byte("package root\n"))
	put(t, filepath.Join(repo, ArrowDir, "go.mod"), []byte("module "+RootModule+"/arrow\n\ngo 1.23.0\n"))
	put(t, filepath.Join(repo, ArrowDir, "batch.go"), []byte("package chdbarrow\n"))
	put(t, filepath.Join(repo, ArrowDir, "nested", "go.mod"), []byte("module example.com/nested\n"))
	put(t, filepath.Join(repo, ArrowDir, "nested", "payload.syso"), []byte("excluded nested payload"))
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = repo
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	return repo, filepath.Join(dir, "staging")
}

func snapshotContents(t *testing.T, out, module, version string) map[string]string {
	t.Helper()
	z, err := zip.OpenReader(filepath.Join(out, "proxy", filepath.FromSlash(module), "@v", version+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	contents := map[string]string{}
	for _, f := range z.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		contents[f.Name] = string(data)
	}
	return contents
}

func TestIndependentArrowVerificationSnapshot(t *testing.T) {
	repo, out := snapshotFixture(t)
	if err := Snapshot(repo, out, "v2.99.0-static-ci"); err != nil {
		t.Fatal(err)
	}
	root := snapshotContents(t, out, RootModule, "v2.99.0-static-ci")
	if len(root) != 2 {
		t.Fatalf("root snapshot leaked nested modules: %v", root)
	}
	module := RootModule + "/arrow"
	generated := "package chdbarrow\nimport _ \"static-ci.example/chdb/static\"\n"
	if err := snapshotModule(filepath.Join(repo, ArrowDir), out, module, arrowSnapshotVersion,
		map[string][]byte{"zz_static_bundle_test.go": []byte(generated)}); err != nil {
		t.Fatal(err)
	}
	files := snapshotContents(t, out, module, arrowSnapshotVersion)
	if len(files) != 3 || files[module+"@"+arrowSnapshotVersion+"/zz_static_bundle_test.go"] != generated {
		t.Fatalf("unexpected Arrow snapshot: %v", files)
	}
	if _, err := os.Stat(filepath.Join(repo, ArrowDir, "zz_static_bundle_test.go")); !os.IsNotExist(err) {
		t.Fatalf("verification changed source checkout: %v", err)
	}
}

func TestSnapshotRefusesSourceIdentityPayloadAndInjectionConflicts(t *testing.T) {
	for _, scenario := range []string{"identity", "native payload", "generated collision"} {
		t.Run(scenario, func(t *testing.T) {
			repo, out := snapshotFixture(t)
			module := RootModule + "/arrow"
			extra := map[string][]byte{}
			switch scenario {
			case "identity":
				module = "example.com/not-arrow"
			case "native payload":
				put(t, filepath.Join(repo, ArrowDir, "payload.syso"), []byte("not a source file"))
			case "generated collision":
				extra["batch.go"] = []byte("package replaced\n")
			}
			if err := snapshotModule(filepath.Join(repo, ArrowDir), out, module, arrowSnapshotVersion, extra); err == nil {
				t.Fatal("accepted invalid source snapshot")
			}
		})
	}
}
