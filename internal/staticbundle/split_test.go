package staticbundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const testCommit = "59a7f688d1fc8d895115e104f09a2c4071490d6a"

func testSplitManifest(platform, version, commit string, archive []byte, objects [][]byte) map[string]any {
	var parts []any
	var order []any
	for i, o := range objects {
		name := fmt.Sprintf("part_%03d.o", i)
		parts = append(parts, map[string]any{"index": i, "member": name, "size": len(o), "sha256": digest(o), "linker": "ld.lld-21"})
		order = append(order, name)
	}
	return map[string]any{
		"format": 1, "artifact": "libchdb split static library", "platform": splitPlatform(platform),
		"version": version, "source_commit": commit, "shard_limit_mib": 192.0,
		"archive": "libchdb.a", "archive_size": len(archive), "archive_sha256": digest(archive),
		"link_order": order, "parts": parts,
		"link": map[string]any{"system_libraries": []any{"-lpthread", "-ldl", "-lm", "-lrt"}},
	}
}
func splitManifestJSON(t *testing.T, m map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type splitFixture struct {
	archive []byte
	objects [][]byte
	release Release
}

// newSplitFixture builds a split archive whose members are opaque bytes. The
// manifest checks are byte checks; object validation is tested elsewhere.
func newSplitFixture() splitFixture {
	f := splitFixture{release: Release{Engine: "v26.11.2-rc.1", SourceCommit: testCommit}}
	f.archive = []byte("!<arch>\n")
	f.archive = append(f.archive, arMember("/", []byte("symbol index"))...)
	for i, payload := range []string{"first shard\x00\x00\x00\x00", "second shard\x00\x00\x00", "last"} {
		f.archive = append(f.archive, arMember(fmt.Sprintf("part_%03d.o/", i), []byte(payload))...)
		f.objects = append(f.objects, []byte(payload))
	}
	return f
}

func (f splitFixture) check(t *testing.T, platform string, m map[string]any, archive []byte) error {
	t.Helper()
	d := t.TempDir()
	mp := filepath.Join(d, splitManifestName)
	put(t, mp, splitManifestJSON(t, m))
	manifest, err := readSplitManifest(mp, platform, f.release)
	if err != nil {
		return err
	}
	af := archiveFile(t, archive)
	objects, err := readArchive(af)
	if err != nil {
		return err
	}
	return checkSplitArchive(manifest, af, objects)
}

func TestSplitManifestAcceptsMatchingArchive(t *testing.T) {
	f := newSplitFixture()
	for _, p := range []string{"linux-amd64", "linux-arm64"} {
		if err := f.check(t, p, testSplitManifest(p, "26.11.2-rc.1", testCommit, f.archive, f.objects), f.archive); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
}

func TestSplitManifestRejectsMismatches(t *testing.T) {
	f := newSplitFixture()
	const p = "linux-amd64"
	parts := func(m map[string]any) []any { return m["parts"].([]any) }
	part := func(m map[string]any, i int) map[string]any { return parts(m)[i].(map[string]any) }
	for _, c := range []struct {
		why     string
		edit    func(m map[string]any)
		archive func([]byte) []byte
	}{
		{"future manifest format", func(m map[string]any) { m["format"] = 2 }, nil},
		{"missing format", func(m map[string]any) { delete(m, "format") }, nil},
		{"wrong architecture", func(m map[string]any) { m["platform"] = "linux-aarch64" }, nil},
		{"macOS platform", func(m map[string]any) { m["platform"] = "darwin-arm64" }, nil},
		{"wrong engine version", func(m map[string]any) { m["version"] = "26.11.1" }, nil},
		{"missing engine version", func(m map[string]any) { m["version"] = nil }, nil},
		{"wrong source commit", func(m map[string]any) { m["source_commit"] = strings.Repeat("a", 40) }, nil},
		{"missing source commit", func(m map[string]any) { m["source_commit"] = nil }, nil},
		{"another archive name", func(m map[string]any) { m["archive"] = "libother.a" }, nil},
		{"archive size", func(m map[string]any) { m["archive_size"] = len(f.archive) + 1 }, nil},
		{"archive SHA-256", func(m map[string]any) { m["archive_sha256"] = strings.Repeat("0", 64) }, nil},
		{"tampered member", nil, func(a []byte) []byte {
			b := bytes.Clone(a)
			i := bytes.Index(b, []byte("second"))
			b[i] = 'S'
			return b
		}},
		{"reordered link order", func(m map[string]any) {
			o := m["link_order"].([]any)
			o[0], o[1] = o[1], o[0]
		}, nil},
		{"reordered parts", func(m map[string]any) {
			ps := parts(m)
			ps[0], ps[1] = ps[1], ps[0]
		}, nil},
		{"part missing", func(m map[string]any) {
			m["parts"] = parts(m)[:2]
			m["link_order"] = m["link_order"].([]any)[:2]
		}, nil},
		{"no parts", func(m map[string]any) { m["parts"] = []any{}; m["link_order"] = []any{} }, nil},
		{"duplicate member", func(m map[string]any) {
			part(m, 1)["member"] = "part_000.o"
			m["link_order"].([]any)[1] = "part_000.o"
		}, nil},
		{"member path", func(m map[string]any) {
			part(m, 1)["member"] = "../part_001.o"
			m["link_order"].([]any)[1] = "../part_001.o"
		}, nil},
		{"member size", func(m map[string]any) { part(m, 2)["size"] = 5 }, nil},
		{"member SHA-256", func(m map[string]any) { part(m, 2)["sha256"] = strings.Repeat("0", 64) }, nil},
		{"part over native file limit", func(m map[string]any) { part(m, 2)["size"] = MaxFileBytes + 1 }, nil},
		{"system library the adapter does not link", func(m map[string]any) {
			m["link"] = map[string]any{"system_libraries": []any{"-lpthread", "-lstdc++"}}
		}, nil},
		{"extra archive member", nil, func(a []byte) []byte { return append(bytes.Clone(a), arMember("part_003.o/", []byte("more"))...) }},
	} {
		m := testSplitManifest(p, "26.11.2-rc.1", testCommit, f.archive, f.objects)
		archive := f.archive
		if c.edit != nil {
			c.edit(m)
		}
		if c.archive != nil {
			archive = c.archive(archive)
		}
		if err := f.check(t, p, m, archive); err == nil {
			t.Errorf("%s: accepted", c.why)
		}
	}
	// A manifest is never checked against an unpinned release commit.
	unpinned := f
	unpinned.release.SourceCommit = ""
	if err := unpinned.check(t, p, testSplitManifest(p, "26.11.2-rc.1", "", f.archive, f.objects), f.archive); err == nil {
		t.Error("accepted a manifest against a release without a source commit")
	}
	if err := f.check(t, p, map[string]any{"format": 1, "parts": "not a list"}, f.archive); err == nil {
		t.Error("accepted a malformed manifest")
	}
	big := filepath.Join(t.TempDir(), splitManifestName)
	put(t, big, bytes.Repeat([]byte(" "), maxSplitManifestBytes+1))
	if _, err := readSplitManifest(big, p, f.release); err == nil {
		t.Error("accepted an oversized manifest")
	}
}

func TestCopySplitShardsKeepsBytesAndOrder(t *testing.T) {
	f := newSplitFixture()
	af := archiveFile(t, f.archive)
	objects, err := readArchive(af)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	shards, err := copySplitShards(work, "linux-arm64", af, objects)
	if err != nil {
		t.Fatal(err)
	}
	if len(shards) != len(f.objects) {
		t.Fatalf("got %d shards", len(shards))
	}
	for i, p := range shards {
		if filepath.Base(p) != fmt.Sprintf("p%04d_linux_arm64.syso", i) {
			t.Fatalf("shard %d named %s", i, p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(b, f.objects[i]) {
			t.Fatalf("shard %d bytes changed", i)
		}
	}
}

func releaseTarball(t *testing.T, entries ...[2]string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: e[0], Mode: 0644, Size: int64(len(e[1]))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "release.tar.gz")
	put(t, p, buf.Bytes())
	return p
}

func TestExtractAcceptsOnlyKnownReleaseMembers(t *testing.T) {
	const header = "#define CHDB_VERSION \"26.11.2-rc.1\"\n"
	h := digest([]byte(header))
	full := [][2]string{{"libchdb.a", "!<arch>\n"}, {splitManifestName, "{}"}, {"chdb.h", header}, {"chdb.hpp", "//"}, {"LICENSE.txt", "license"}}
	if err := extract(releaseTarball(t, full...), t.TempDir(), h); err != nil {
		t.Fatalf("rejected the split release layout: %v", err)
	}
	for _, c := range []struct {
		why     string
		entries [][2]string
	}{
		{"unknown member", append(full, [2]string{"install.sh", "#!/bin/sh"})},
		{"nested path", [][2]string{{"libchdb.a", "!<arch>\n"}, {"chdb.h", header}, {"x/" + splitManifestName, "{}"}}},
		{"duplicate manifest", append(full, [2]string{splitManifestName, "{}"})},
		{"duplicate license", append(full, [2]string{"LICENSE.txt", "other"})},
		{"oversized manifest", [][2]string{{"libchdb.a", "!<arch>\n"}, {"chdb.h", header}, {splitManifestName, strings.Repeat(" ", maxSplitManifestBytes+1)}}},
		{"missing archive", [][2]string{{"chdb.h", header}, {splitManifestName, "{}"}}},
	} {
		if err := extract(releaseTarball(t, c.entries...), t.TempDir(), h); err == nil {
			t.Errorf("%s: accepted", c.why)
		}
	}
}

// Linux needs the manifest; macOS archives never carry one.
func TestPackageManifestPresenceMatchesPlatform(t *testing.T) {
	o, d := tinyRelease(t)
	var entries [][2]string
	ar := "!<arch>\n" + string(arMember("part_000.o/", []byte("x")))
	header, err := os.ReadFile(filepath.Join(o.Repo, "chdb-purego", "static_chdb.h"))
	if err != nil {
		t.Fatal(err)
	}
	entries = append(entries, [2]string{"libchdb.a", ar}, [2]string{"chdb.h", string(header)})
	if !strings.HasPrefix(o.Platform, "linux-") {
		entries = append(entries, [2]string{splitManifestName, "{}"})
	}
	o.Archive = releaseTarball(t, entries...)
	data, err := os.ReadFile(o.Archive)
	if err != nil {
		t.Fatal(err)
	}
	o.Release.Platforms[o.Platform] = Asset{Name: "release.tar.gz", SHA256: digest(data)}
	o.Output = filepath.Join(d, "presence")
	_, err = Package(context.Background(), o)
	if err == nil {
		t.Fatal("accepted a release without the platform's archive layout")
	}
	if !strings.Contains(err.Error(), splitManifestName) && !strings.Contains(err.Error(), "member") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// The native file limit admits chdb-core's ~200 MiB shards and nothing over
// 256 MiB, and the unchanged module budget never holds two such shards.
func TestNativeFileLimitBoundaries(t *testing.T) {
	if testing.Short() {
		t.Skip("writes a 256 MiB sparse file into a module zip")
	}
	d := t.TempDir()
	sparse := func(name string, size int64) string {
		p := filepath.Join(d, name)
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		if err = f.Truncate(size); err != nil {
			t.Fatal(err)
		}
		if err = f.Close(); err != nil {
			t.Fatal(err)
		}
		return p
	}
	mod := map[string][]byte{"go.mod": []byte("module go.example.com/native\n")}
	at := sparse("at.syso", MaxFileBytes)
	out := filepath.Join(d, "at")
	if _, err := writeModule(out, "go.example.com/native", "v0.261102.0-rc.1.1", time.Unix(0, 0), mod, map[string]string{"p0000.syso": at}); err != nil {
		t.Fatalf("rejected a %d byte native file: %v", MaxFileBytes, err)
	}
	if err := checkZipDirectory(filepath.Join(out, "proxy", "go.example.com", "native", "@v", "v0.261102.0-rc.1.1.zip"), "go.example.com/native", "v0.261102.0-rc.1.1"); err != nil {
		t.Fatalf("zip inventory rejected a %d byte native file: %v", MaxFileBytes, err)
	}
	over := sparse("over.syso", MaxFileBytes+1)
	if _, err := writeModule(filepath.Join(d, "over"), "go.example.com/native", "v0.261102.0-rc.1.1", time.Unix(0, 0), mod, map[string]string{"p0000.syso": over}); err == nil {
		t.Fatal("accepted a native file over the limit")
	}
	a, b := sparse("a.syso", 200<<20), sparse("b.syso", 200<<20)
	if _, err := writeModule(filepath.Join(d, "two"), "go.example.com/native", "v0.261102.0-rc.1.1", time.Unix(0, 0), mod, map[string]string{"p0000.syso": a, "p0001.syso": b}); err == nil || !strings.Contains(err.Error(), "uncompressed bytes") {
		t.Fatalf("expected the module budget to reject two 200 MiB shards, got %v", err)
	}
}

// CHDB_SPLIT_RELEASE_DIR holds the pinned chdb-core Linux split tarballs. On
// any host, check them against static-release.json and their own manifests
// exactly as Package does before writing modules.
func TestPinnedSplitReleases(t *testing.T) {
	dir := os.Getenv("CHDB_SPLIT_RELEASE_DIR")
	if dir == "" {
		t.Skip("CHDB_SPLIT_RELEASE_DIR not set")
	}
	r, err := ReadRelease("../../static-release.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"linux-amd64", "linux-arm64"} {
		t.Run(p, func(t *testing.T) {
			a := r.Platforms[p]
			path := filepath.Join(dir, a.Name)
			if err := checkDigest(path, a.SHA256); err != nil {
				t.Fatal(err)
			}
			work := t.TempDir()
			if err := extract(path, work, r.HeaderSHA256); err != nil {
				t.Fatal(err)
			}
			m, err := readSplitManifest(filepath.Join(work, splitManifestName), p, r)
			if err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(filepath.Join(work, "libchdb.a"))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			objects, err := readArchive(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, o := range objects {
				if _, _, err := checkObject(io.NewSectionReader(f, o.offset, o.size), p); err != nil {
					t.Fatalf("%s: %v", o.name, err)
				}
			}
			if err = checkSplitArchive(m, f, objects); err != nil {
				t.Fatal(err)
			}
			var largest int64
			for _, o := range objects {
				largest = max(largest, o.size)
			}
			t.Logf("%s: %d shards, largest %.1f MiB, host %s", p, len(objects), float64(largest)/(1<<20), runtime.GOOS)
		})
	}
}
