package staticbundle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMacOSOnlyAssemblyStaysPartialAndNonPublishable(t *testing.T) {
	source := completeFixture(t)
	full, err := ReadBundle(source)
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	header := []byte("test macOS scope header\n")
	put(t, filepath.Join(repo, "chdb-purego", "static_chdb.h"), header)
	put(t, filepath.Join(repo, "LICENSE.txt"), []byte("test license\n"))
	r := Release{Engine: full.Engine, Packaging: 1, Time: full.Time, HeaderSHA256: digest(header), Platforms: map[string]Asset{}}
	for _, n := range full.Platforms {
		r.Platforms[n.Platform] = Asset{Name: n.Platform + ".tar.gz", SHA256: n.SourceSHA256}
	}
	var inputs []string
	for _, n := range full.Platforms {
		if !strings.HasPrefix(n.Platform, "darwin-") {
			continue
		}
		b := full
		b.Complete, b.HeaderSHA256, b.Platforms, b.Files = false, r.HeaderSHA256, []Native{n}, nil
		dir := t.TempDir()
		for _, f := range full.Files {
			if !strings.HasPrefix(f.Path, "proxy/"+n.Modules[0]+"/") {
				continue
			}
			if err = copyFile(filepath.Join(source, filepath.FromSlash(f.Path)), filepath.Join(dir, filepath.FromSlash(f.Path))); err != nil {
				t.Fatal(err)
			}
			b.Files = append(b.Files, f)
		}
		if err = writeJSON(filepath.Join(dir, "bundle.json"), b); err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, dir)
	}
	if len(inputs) != 2 {
		t.Fatal("fixture must include both Mac architectures")
	}
	parent := t.TempDir()
	if _, err = Assemble(repo, filepath.Join(parent, "not-a-release"), inputs, r, false); err == nil {
		t.Fatal("two Macs were accepted as a complete four-platform release")
	}
	out := filepath.Join(parent, "macos")
	b, err := Assemble(repo, out, inputs, r, true)
	if err != nil {
		t.Fatal(err)
	}
	if b.Complete || len(b.Platforms) != 2 {
		t.Fatal("macOS-only result lost its partial identity")
	}
	if _, err = readDistribution(out); err == nil {
		t.Fatal("macOS-only bundle became publicly publishable")
	}
	if _, err = PlanPublication(out, testEndpoint, "test-bucket"); err == nil {
		t.Fatal("macOS-only bundle bypassed publication gate")
	}
	b.Complete = true
	if err = writeJSON(filepath.Join(out, "bundle.json"), b); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadBundle(out); err == nil {
		t.Fatal("forged complete flag accepted")
	}
	if _, err = os.Stat(filepath.Join(parent, "not-a-release")); !os.IsNotExist(err) {
		t.Fatal("rejected complete assembly should not create an output")
	}
}
