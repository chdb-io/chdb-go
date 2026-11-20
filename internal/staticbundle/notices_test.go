package staticbundle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureNotices() map[string][]byte {
	return map[string][]byte{
		"THIRD_PARTY_NOTICES.txt": []byte("Synthetic test notices, not public release material.\n"),
		"STATIC_RELINKING.md":     []byte("Synthetic test source and relink guidance.\n"),
		"SOURCE_PROVENANCE.json":  []byte(`{"engine":"v26.11.1","core_commit":"bc9c42e6edf4f394884a6a0458aa49abff85f1b6"}`),
	}
}

func TestPackageAndAssemblyPreserveNoticeBytes(t *testing.T) {
	o, d := tinyRelease(t)
	o.Notices = filepath.Join(d, "notices")
	for name, data := range fixtureNotices() {
		put(t, filepath.Join(o.Notices, name), data)
	}
	b, err := Package(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if !sameNotices(b.Notices, noticeInventory(fixtureNotices())) {
		t.Fatal("notice inventory changed")
	}
	out := filepath.Join(d, "assembled-notices")
	assembled, err := Assemble(o.Repo, out, []string{o.Output}, o.Release, true)
	if err != nil {
		t.Fatal(err)
	}
	if !sameNotices(assembled.Notices, b.Notices) {
		t.Fatal("assembly dropped notices")
	}
	if _, err = ReadBundle(out); err != nil {
		t.Fatal(err)
	}
	for _, file := range assembled.Files {
		if !strings.HasSuffix(file.Path, ".zip") {
			continue
		}
		module := strings.TrimSuffix(strings.TrimPrefix(file.Path, "proxy/"), "/@v/"+b.Version+".zip")
		got, err := zipNotices(filepath.Join(out, filepath.FromSlash(file.Path)), module, b.Version, b.Notices, b.Engine)
		if err != nil {
			t.Fatal(err)
		}
		for name, data := range fixtureNotices() {
			if string(got[name]) != string(data) {
				t.Fatal("module lost release notices: " + name)
			}
		}
	}
}

func TestNoticeInputsRejectMissingSymlinkAndWrongEngine(t *testing.T) {
	for _, scenario := range []string{"missing", "symlink", "engine", "oversized"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			for name, data := range fixtureNotices() {
				put(t, filepath.Join(dir, name), data)
			}
			p := filepath.Join(dir, "THIRD_PARTY_NOTICES.txt")
			switch scenario {
			case "missing":
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				other := filepath.Join(t.TempDir(), "notice")
				put(t, other, []byte("outside"))
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, p); err != nil {
					t.Fatal(err)
				}
			case "engine":
				put(t, filepath.Join(dir, "SOURCE_PROVENANCE.json"), []byte(`{"engine":"v26.9.0","core_commit":"bc9c42e6edf4f394884a6a0458aa49abff85f1b6"}`))
			case "oversized":
				if err := os.Truncate(p, MaxNoticeBytes+1); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := loadNotices(dir, "v26.11.1"); err == nil {
				t.Fatal("invalid notice inputs accepted")
			}
		})
	}
}

func TestPublicationRejectsLegacyBundlesWithoutNotices(t *testing.T) {
	dir := completeFixture(t)
	b, err := ReadBundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	b.Notices = nil
	if err = writeJSON(filepath.Join(dir, "bundle.json"), b); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadBundle(dir); err != nil {
		t.Fatal("legacy local bundle should remain readable: ", err)
	}
	if _, err = PlanPublication(dir, testEndpoint, "test-bucket"); err == nil {
		t.Fatal("legacy bundle can be published")
	}
	if _, err = readDistribution(dir); err == nil {
		t.Fatal("legacy bundle bypassed Blob notice delivery check")
	}
}

func TestBundleRejectsForgedNoticeHashes(t *testing.T) {
	dir := completeFixture(t)
	b, err := ReadBundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	b.Notices[0].SHA256 = strings.Repeat("0", 64)
	if err = writeJSON(filepath.Join(dir, "bundle.json"), b); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadBundle(dir); err == nil || !strings.Contains(err.Error(), "notice content differs") {
		t.Fatalf("receipt's forged notice digest was accepted: %v", err)
	}
}
