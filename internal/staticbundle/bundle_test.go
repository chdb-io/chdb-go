package staticbundle

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func digest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func put(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0644); err != nil {
		t.Fatal(err)
	}
}
func arMember(name string, b []byte) []byte {
	h := fmt.Sprintf("%-16s%-12d%-6d%-6d%-8s%-10d`\n", name, 0, 0, 0, "100644", len(b))
	out := append([]byte(h), b...)
	if len(b)%2 != 0 {
		out = append(out, '\n')
	}
	return out
}
func archiveFile(t *testing.T, b []byte) *os.File {
	t.Helper()
	p := filepath.Join(t.TempDir(), "lib.a")
	put(t, p, b)
	f, e := os.Open(p)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestArchiveNamesAndDuplicateMembers(t *testing.T) {
	b := []byte("!<arch>\n")
	b = append(b, arMember("/", []byte("symbol index"))...)
	b = append(b, arMember("/SYM64/", []byte("64-bit index"))...)
	b = append(b, arMember("//", []byte("a_very_long_object_name.o/\n"))...)
	b = append(b, arMember("/0", []byte("one"))...)
	b = append(b, arMember("duplicate.o/", []byte("two"))...)
	b = append(b, arMember("duplicate.o/", []byte("three"))...)
	b = append(b, arMember("#1/12", append([]byte("__.SYMDEF\x00\x00\x00"), []byte("index")...))...)
	b = append(b, arMember("#1/12", append([]byte("bsd_name.o\x00\x00"), []byte("four")...))...)
	f := archiveFile(t, b)
	ms, err := readArchive(f)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a_very_long_object_name.o", "duplicate.o", "duplicate.o", "bsd_name.o"}
	var names []string
	for _, m := range ms {
		names = append(names, m.name)
		data := make([]byte, m.size)
		if _, err = f.ReadAt(data, m.offset); err != nil {
			t.Fatal(err)
		}
		if len(data) == 0 {
			t.Fatal("missing member payload")
		}
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("got %q, want %q", names, want)
	}
}
func TestRejectMalformedArchives(t *testing.T) {
	for _, b := range [][]byte{[]byte("!<thin>\n"), []byte("!<arch>\n"), append([]byte("!<arch>\n"), []byte("short header")...), append([]byte("!<arch>\n"), arMember("/123", []byte("x"))...)} {
		if _, err := readArchive(archiveFile(t, b)); err == nil {
			t.Fatalf("accepted malformed archive %q", b)
		}
	}
}
func TestPrefixValidation(t *testing.T) {
	for _, p := range []string{"go.example.com/chdb", "static-ci.example/chdb"} {
		if err := ValidatePrefix(p); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"", "github.com/chdb-io/chdb-go", "https://go.example.com/chdb", "go.example.com/../other", "go.example.com/CHDB", "go.example.com/chdb@v1", "go.example.com/a//b"} {
		if ValidatePrefix(p) == nil {
			t.Fatalf("accepted %q", p)
		}
	}
}
func TestRepositoryHeaderMatchesRelease(t *testing.T) {
	r, err := ReadRelease("../../static-release.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = checkDigest("../../chdb-purego/static_chdb.h", r.HeaderSHA256); err != nil {
		t.Fatal(err)
	}
}

func tinyRelease(t *testing.T) (Options, string) {
	t.Helper()
	if !supported(runtime.GOOS + "-" + runtime.GOARCH) {
		t.Skip("native runner required")
	}
	for _, tool := range []string{"cc", "ld"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " unavailable")
		}
	}
	d := t.TempDir()
	repo := filepath.Join(d, "repo")
	header := []byte("#define CHDB_VERSION \"26.11.1\"\n")
	put(t, filepath.Join(repo, "chdb-purego", "static_chdb.h"), header)
	put(t, filepath.Join(repo, "LICENSE.txt"), []byte("test license\n"))
	platform := runtime.GOOS + "-" + runtime.GOARCH
	split := strings.HasPrefix(platform, "linux-")
	var objects [][]byte
	ar := []byte("!<arch>\n")
	for i, n := range []string{"first", "second"} {
		p := filepath.Join(d, n+".c")
		put(t, p, []byte("int "+n+"(void) { return 42; }\n"))
		obj := filepath.Join(d, n+".o")
		if output, err := exec.Command("cc", "-g0", "-c", p, "-o", obj).CombinedOutput(); err != nil {
			t.Fatalf("cc: %v: %s", err, output)
		}
		data, err := os.ReadFile(obj)
		if err != nil {
			t.Fatal(err)
		}
		// macOS members share a name, as real archives' do. Linux split
		// archives name each shard, and the manifest lists them in order.
		name := "duplicate.o/"
		if split {
			name = fmt.Sprintf("part_%03d.o/", i)
		}
		ar = append(ar, arMember(name, data)...)
		objects = append(objects, data)
	}
	entries := []struct {
		name string
		b    []byte
	}{{"libchdb.a", ar}, {"chdb.h", header}}
	if split {
		entries = append(entries, struct {
			name string
			b    []byte
		}{splitManifestName, splitManifestJSON(t, testSplitManifest(platform, "26.11.1", testCommit, ar, objects))})
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0644, Size: int64(len(e.b))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(e.b); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(d, "release.tar.gz")
	put(t, path, buf.Bytes())
	r := Release{Engine: "v26.11.1", Packaging: 1, Time: time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC), HeaderSHA256: digest(header), SourceCommit: testCommit, Platforms: map[string]Asset{platform: {Name: "release.tar.gz", SHA256: digest(buf.Bytes())}}}
	return Options{Repo: repo, Archive: path, Platform: platform, Prefix: "go.example.com/chdb", Bindings: "v2.99.0-test", Release: r, Output: filepath.Join(d, "native")}, d
}
func TestPackageAndAssembleNativeObjects(t *testing.T) {
	o, d := tinyRelease(t)
	b, err := Package(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	shards := 1 // macOS prelinks the two members into one shard
	if strings.HasPrefix(o.Platform, "linux-") {
		shards = 2 // Linux ships chdb-core's shards as they are
	}
	if b.Platforms[0].Objects != 2 || b.Platforms[0].Shards != shards {
		t.Fatalf("lost duplicate-named objects: %+v", b)
	}
	if _, err = ReadBundle(o.Output); err != nil {
		t.Fatal(err)
	}
	// Identical pinned inputs, regardless of temp directory, produce identical
	// native module bytes. Versioned artifacts must not depend on wall time.
	copyOpts := o
	copyOpts.Output = filepath.Join(d, "native-again")
	b2, err := Package(context.Background(), copyOpts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b.Files, b2.Files) {
		t.Fatalf("non-deterministic module bytes\n%+v\n%+v", b.Files, b2.Files)
	}
	if _, err = Assemble(o.Repo, filepath.Join(d, "production"), []string{o.Output}, o.Release, false); err == nil {
		t.Fatal("accepted incomplete production release")
	}
	out := filepath.Join(d, "assembled")
	all, err := Assemble(o.Repo, out, []string{o.Output}, o.Release, true)
	if err != nil {
		t.Fatal(err)
	}
	if all.Complete {
		t.Fatal("partial bundle marked complete")
	}
	if _, err = ReadBundle(out); err != nil {
		t.Fatal(err)
	}
	modpath := filepath.Join(out, "proxy", filepath.FromSlash(o.Prefix), "static", "@v", all.Version+".zip")
	z, err := zip.OpenReader(modpath)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	for _, f := range z.File {
		if strings.HasSuffix(f.Name, ".syso") {
			t.Fatal("native bytes leaked into entry module")
		}
	}
	// Missing build tag fails deliberately, before any native engine is used.
	work := filepath.Join(d, "consumer")
	if err = os.Mkdir(work, 0755); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(work, "go.mod"), []byte("module example.com/test\n\ngo 1.21\nrequire "+o.Prefix+"/static "+all.Version+"\n"))
	put(t, filepath.Join(work, "main.go"), []byte("package main\nimport _ \""+o.Prefix+"/static\"\nfunc main(){}\n"))
	cmd := exec.Command("go", "build", "-mod=mod", "-modcacherw", ".")
	cmd.Dir = work
	cmd.Env = append(cleanEnv(os.Environ(), "GOWORK", "GOFLAGS", "GOPROXY", "GOSUMDB", "GOMODCACHE", "CGO_ENABLED"), "GOWORK=off", "GOPROXY=file://"+out+"/proxy", "GOSUMDB=off", "GOMODCACHE="+filepath.Join(d, "cache"), "CGO_ENABLED=0")
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "chdb_static_requires_tag_chdb_static") {
		t.Fatalf("expected unsupported-build diagnostic, got %v: %s", err, output)
	}
	// Corrupting a staged file must be caught before assembly or upload planning.
	corrupt := filepath.Join(o.Output, filepath.FromSlash(b.Files[0].Path))
	put(t, corrupt, []byte("tampered"))
	if _, err = ReadBundle(o.Output); err == nil {
		t.Fatal("accepted tampered native bytes")
	}
}
func TestRefuseBadInputsAndOverwrite(t *testing.T) {
	o, d := tinyRelease(t)
	bad := o
	bad.Prefix = "github.com/chdb-io/native"
	if _, err := Package(context.Background(), bad); err == nil {
		t.Fatal("accepted undiscoverable github vanity prefix")
	}
	bad = o
	bad.Release.Platforms[o.Platform] = Asset{Name: "release.tar.gz", SHA256: strings.Repeat("0", 64)}
	if _, err := Package(context.Background(), bad); err == nil {
		t.Fatal("accepted wrong source digest")
	}
	if err := newOutput(filepath.Join(o.Repo, "payload"), o.Repo); err == nil {
		t.Fatal("accepted in-checkout output")
	}
	path := filepath.Join(d, "exists")
	if err := os.Mkdir(path, 0755); err != nil {
		t.Fatal(err)
	}
	if err := newOutput(path, o.Repo); err == nil {
		t.Fatal("overwrote existing staging directory")
	}
}
func TestBundleRejectsTraversalAndVersionMismatch(t *testing.T) {
	o, _ := tinyRelease(t)
	b, err := Package(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	b.Version = "../../escape"
	data, _ := json.Marshal(b)
	put(t, filepath.Join(o.Output, "bundle.json"), data)
	if _, err = ReadBundle(o.Output); err == nil {
		t.Fatal("accepted traversal version")
	}
}

func TestModuleLimitIncludesAllPayloadFiles(t *testing.T) {
	d := t.TempDir()
	paths := map[string]string{}
	for i := 0; i < 5; i++ {
		p := filepath.Join(d, fmt.Sprintf("p%d.syso", i))
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		if err = f.Truncate(80 << 20); err != nil {
			t.Fatal(err)
		}
		if err = f.Close(); err != nil {
			t.Fatal(err)
		}
		paths[filepath.Base(p)] = p
	}
	_, err := writeModule(d, "go.example.com/native", "v0.261101.1", time.Unix(0, 0), map[string][]byte{"go.mod": []byte("module go.example.com/native\n")}, paths)
	if err == nil || !strings.Contains(err.Error(), "uncompressed bytes") {
		t.Fatalf("expected total module limit, got %v", err)
	}
}

func TestOutputCannotEscapeViaSymlinkIntoCheckout(t *testing.T) {
	d := t.TempDir()
	repo := filepath.Join(d, "repo")
	if err := os.Mkdir(repo, 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(d, "shortcut")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	if err := newOutput(filepath.Join(link, "payload"), repo); err == nil {
		t.Fatal("followed output parent symlink into checkout")
	}
}

func TestZipInventoryRejectsInvalidEntries(t *testing.T) {
	prefix := "go.example.com/native@v0.261101.1/"
	for _, names := range [][]string{{"other.example/native@v0.261101.1/go.mod"}, {prefix + "../escape"}, {prefix + "native.go"}, {prefix + "go.mod", prefix + "go.mod"}} {
		p := filepath.Join(t.TempDir(), "bad.zip")
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		zw := zip.NewWriter(f)
		for _, n := range names {
			w, err := zw.Create(n)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = w.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
		}
		if err = zw.Close(); err != nil {
			t.Fatal(err)
		}
		if err = f.Close(); err != nil {
			t.Fatal(err)
		}
		if err = checkZipDirectory(p, "go.example.com/native", "v0.261101.1"); err == nil {
			t.Fatalf("accepted invalid entries %v", names)
		}
	}
	// A tiny zip can advertise an excessive expansion; reject from directory
	// metadata before attempting to allocate/decompress its payload.
	p := filepath.Join(t.TempDir(), "oversized.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	if _, err = zw.CreateRaw(&zip.FileHeader{Name: prefix + "go.mod", Method: zip.Store, UncompressedSize64: 500<<20 + 1}); err != nil {
		t.Fatal(err)
	}
	if err = zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	if err = checkZipDirectory(p, "go.example.com/native", "v0.261101.1"); err == nil {
		t.Fatal("accepted oversized expansion")
	}
}
