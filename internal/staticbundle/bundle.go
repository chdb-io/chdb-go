// Package staticbundle prepares immutable, independently downloadable native
// modules. Staging never writes remotes; only the explicitly confirmed Publish
// operation can upload. Large working files stay outside the checkout, and the
// root Go module acquires no native-module dependencies.
package staticbundle

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/format"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/chdb-io/chdb-go/v2/internal/enginetag"
)

const RootModule = "github.com/chdb-io/chdb-go/v2"
const MaxModuleBytes int64 = 384 << 20

// MaxFileBytes bounds one native file. chdb-core's split Linux shards are cut
// from at most 192 MiB of input and can come out near 200 MiB; 256 MiB leaves
// headroom, and MaxModuleBytes still never fits two such shards in one module.
// Go's own limit is 500 MiB per module zip and per uncompressed module.
const MaxFileBytes int64 = 256 << 20
const objectBudget int64 = 64 << 20

var Platforms = []string{"darwin-amd64", "darwin-arm64", "linux-amd64", "linux-arm64"}
var prefixRE = regexp.MustCompile(`^[a-z0-9]+(?:[.-][a-z0-9]+)+(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*$`)
var bindingRE = regexp.MustCompile(`^v2\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?$`)

type Asset struct {
	Name   string `json:"asset"`
	SHA256 string `json:"sha256"`
}
type Release struct {
	Engine       string           `json:"engine"`
	Packaging    int              `json:"packaging"`
	Time         time.Time        `json:"time"`
	HeaderSHA256 string           `json:"header_sha256"`
	SourceCommit string           `json:"source_commit"`
	Platforms    map[string]Asset `json:"platforms"`
}
type File struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type Native struct {
	Platform     string   `json:"platform"`
	Modules      []string `json:"modules"`
	Objects      int      `json:"objects"`
	Shards       int      `json:"shards"`
	SourceSHA256 string   `json:"source_sha256"`
}
type Bundle struct {
	Schema       int       `json:"schema"`
	Prefix       string    `json:"module_prefix"`
	Engine       string    `json:"engine"`
	Version      string    `json:"version"`
	Bindings     string    `json:"bindings_version"`
	Time         time.Time `json:"time"`
	HeaderSHA256 string    `json:"header_sha256"`
	Platforms    []Native  `json:"platforms"`
	Complete     bool      `json:"complete"`
	Files        []File    `json:"files"`
	Notices      []File    `json:"notices,omitempty"`
}
type Options struct {
	Repo, Archive, Output, Platform, Prefix, Bindings, Notices string
	Release                                                    Release
}

func ReadRelease(path string) (Release, error) {
	var r Release
	b, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal(b, &r); err != nil {
		return r, err
	}
	if _, err = enginetag.Module(r.Engine, r.Packaging); err != nil {
		return r, err
	}
	if r.Time.IsZero() || !isDigest(r.HeaderSHA256) || !isCommit(r.SourceCommit) {
		return r, fmt.Errorf("release must pin time, header SHA-256 and source commit")
	}
	for _, p := range Platforms {
		a, ok := r.Platforms[p]
		if !ok || filepath.Base(a.Name) != a.Name || a.Name == "" || !isDigest(a.SHA256) {
			return r, fmt.Errorf("release must pin asset and SHA-256 for %s", p)
		}
	}
	return r, nil
}
func isDigest(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && s == strings.ToLower(s)
}
func ValidatePrefix(prefix string) error {
	if !prefixRE.MatchString(prefix) || strings.Contains(prefix, "..") || prefix == "github.com" || strings.HasPrefix(prefix, "github.com/") {
		return fmt.Errorf("module prefix %q must be a lowercase vanity path on an authorized domain, not github.com", prefix)
	}
	return nil
}
func newOutput(path, repo string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	r, err := filepath.Abs(repo)
	if err != nil {
		return err
	}
	r, err = filepath.EvalSymlinks(r)
	if err != nil {
		return err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return err
	}
	abs = filepath.Join(parent, filepath.Base(abs))
	if rel, err := filepath.Rel(r, abs); err == nil && (rel == "." || !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != "..") {
		return fmt.Errorf("output must be outside the checkout")
	}
	return os.Mkdir(abs, 0755) // refuse existing output, including partial attempts
}

func Package(ctx context.Context, o Options) (Bundle, error) {
	var b Bundle
	if err := ValidatePrefix(o.Prefix); err != nil {
		return b, err
	}
	if !bindingRE.MatchString(o.Bindings) {
		return b, fmt.Errorf("explicit canonical v2 bindings version required")
	}
	if o.Platform != runtime.GOOS+"-"+runtime.GOARCH {
		return b, fmt.Errorf("package %s on a native runner, not %s/%s", o.Platform, runtime.GOOS, runtime.GOARCH)
	}
	a, ok := o.Release.Platforms[o.Platform]
	if !ok {
		return b, fmt.Errorf("unsupported platform %s", o.Platform)
	}
	if err := checkDigest(o.Archive, a.SHA256); err != nil {
		return b, err
	}
	header := filepath.Join(o.Repo, "chdb-purego", "static_chdb.h")
	if err := checkDigest(header, o.Release.HeaderSHA256); err != nil {
		return b, fmt.Errorf("adapter header: %w", err)
	}
	v, err := enginetag.Module(o.Release.Engine, o.Release.Packaging)
	if err != nil {
		return b, err
	}
	notices, err := loadNotices(o.Notices, o.Release.Engine)
	if err != nil {
		return b, err
	}
	license, err := os.ReadFile(filepath.Join(o.Repo, "LICENSE.txt"))
	if err != nil {
		return b, err
	}
	metadataBytes := int64(len(license)) + 64<<10
	for _, data := range notices {
		metadataBytes += int64(len(data))
	}
	if metadataBytes+MaxFileBytes > MaxModuleBytes {
		return b, fmt.Errorf("release notices leave insufficient room for native payloads")
	}
	if err = newOutput(o.Output, o.Repo); err != nil {
		return b, err
	}
	work, err := os.MkdirTemp("", "chdb-static-package-")
	if err != nil {
		return b, err
	}
	defer os.RemoveAll(work)
	if err = extract(o.Archive, work, o.Release.HeaderSHA256); err != nil {
		return b, err
	}
	f, err := os.Open(filepath.Join(work, "libchdb.a"))
	if err != nil {
		return b, err
	}
	defer f.Close()
	objects, err := readArchive(f)
	if err != nil {
		return b, err
	}
	var minimum, sdk uint32
	for _, m := range objects {
		min, s, err := checkObject(io.NewSectionReader(f, m.offset, m.size), o.Platform)
		if err != nil {
			return b, fmt.Errorf("member %s: %w", m.name, err)
		}
		minimum = maxVersion(minimum, min)
		sdk = maxVersion(sdk, s)
	}
	if strings.HasPrefix(o.Platform, "darwin-") && minimum == 0 {
		return b, fmt.Errorf("missing macOS deployment target")
	}
	manifest := filepath.Join(work, splitManifestName)
	_, statErr := os.Stat(manifest)
	split := strings.HasPrefix(o.Platform, "linux-")
	if split && statErr != nil {
		return b, fmt.Errorf("%s needs chdb-core's split static archive with %s", o.Platform, splitManifestName)
	}
	if !split && statErr == nil {
		return b, fmt.Errorf("%s is only expected in the split Linux archive", splitManifestName)
	}
	var shards []string
	if split {
		m, err := readSplitManifest(manifest, o.Platform, o.Release)
		if err != nil {
			return b, err
		}
		if err = checkSplitArchive(m, f, objects); err != nil {
			return b, err
		}
		if shards, err = copySplitShards(work, o.Platform, f, objects); err != nil {
			return b, err
		}
	} else if shards, err = prelinkShards(ctx, work, o.Platform, f, objects, minimum, sdk); err != nil {
		return b, err
	}
	return finishPackage(o, a, v, license, notices, metadataBytes, len(objects), shards)
}

// prelinkShards merges macOS archive members into relocatable shards. Linux
// shards come prebuilt from chdb-core and are never relinked.
func prelinkShards(ctx context.Context, work, platform string, f *os.File, objects []member, minimum, sdk uint32) ([]string, error) {
	objdir := filepath.Join(work, "objects")
	if err := os.Mkdir(objdir, 0755); err != nil {
		return nil, err
	}
	var shards []string
	var emit func([]member) error
	emit = func(ms []member) error {
		arch := "arm64"
		if strings.HasSuffix(platform, "amd64") {
			arch = "x86_64"
		}
		args := []string{"-r", "-keep_private_externs", "-arch", arch, "-platform_version", "macos", osVersion(minimum), osVersion(maxVersion(sdk, minimum))}
		for i, m := range ms {
			name := fmt.Sprintf("m%06d.o", i)
			data, err := io.ReadAll(io.NewSectionReader(f, m.offset, m.size))
			if err != nil {
				return err
			}
			data, err = anchorMachOSections(data, name)
			if err != nil {
				return fmt.Errorf("anchor %s: %w", m.name, err)
			}
			source := bytes.NewReader(data)
			dest, err := os.Create(filepath.Join(objdir, name))
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(dest, source)
			closeErr := dest.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			args = append(args, name)
		}
		out := filepath.Join(work, fmt.Sprintf("p%04d_%s.syso", len(shards), strings.ReplaceAll(platform, "-", "_")))
		args = append(args, "-o", out)
		cmd := exec.CommandContext(ctx, "ld", args...)
		cmd.Dir = objdir
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("prelink %s: %w\n%s", platform, err, output)
		}
		st, err := os.Stat(out)
		if err != nil {
			return err
		}
		if st.Size() > MaxFileBytes {
			if len(ms) == 1 {
				return fmt.Errorf("one prelinked object exceeds %d bytes", MaxFileBytes)
			}
			if err = os.Remove(out); err != nil {
				return err
			}
			mid := len(ms) / 2
			if err = emit(ms[:mid]); err != nil {
				return err
			}
			return emit(ms[mid:])
		}
		check, err := os.Open(out)
		if err != nil {
			return err
		}
		_, _, err = checkObject(check, platform)
		check.Close()
		if err != nil {
			return err
		}
		shards = append(shards, out)
		return nil
	}
	start := 0
	var size int64
	for i, m := range objects {
		if i > start && size+m.size > objectBudget {
			if err := emit(objects[start:i]); err != nil {
				return nil, err
			}
			start = i
			size = 0
		}
		size += m.size
	}
	if err := emit(objects[start:]); err != nil {
		return nil, err
	}
	return shards, nil
}

func finishPackage(o Options, a Asset, v string, license []byte, notices map[string][]byte, metadataBytes int64, objects int, shards []string) (Bundle, error) {
	b := Bundle{Schema: 1, Prefix: o.Prefix, Engine: o.Release.Engine, Version: v, Bindings: o.Bindings, Time: o.Release.Time, HeaderSHA256: o.Release.HeaderSHA256}
	b.Notices = noticeInventory(notices)
	n := Native{Platform: o.Platform, Objects: objects, Shards: len(shards), SourceSHA256: a.SHA256}
	groups := [][]string{{}}
	var size int64
	for _, p := range shards {
		st, err := os.Stat(p)
		if err != nil {
			return b, err
		}
		if size+st.Size() > MaxModuleBytes-metadataBytes {
			groups = append(groups, nil)
			size = 0
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], p)
		size += st.Size()
	}
	for i, group := range groups {
		module := fmt.Sprintf("%s/libchdb/%s/part%d", o.Prefix, o.Platform, i)
		n.Modules = append(n.Modules, module)
		files := map[string][]byte{"go.mod": []byte("module " + module + "\n\ngo 1.21\n"), "LICENSE": license}
		for name, data := range notices {
			files[name] = data
		}
		files["native.go"] = []byte(fmt.Sprintf("// Code generated by staticbundle; DO NOT EDIT.\n//go:build chdb_static && cgo && %s\n\npackage native\n\nconst Engine = %q\nconst Version = %q\n", strings.ReplaceAll(o.Platform, "-", " && "), b.Engine, b.Version))
		paths := map[string]string{}
		for _, p := range group {
			paths[filepath.Base(p)] = p
		}
		written, err := writeModule(o.Output, module, v, b.Time, files, paths)
		if err != nil {
			return b, err
		}
		b.Files = append(b.Files, written...)
	}
	b.Platforms = []Native{n}
	return b, writeJSON(filepath.Join(o.Output, "bundle.json"), b)
}

// releaseMembers is every file a chdb-core static tarball may carry, with its
// size limit. LICENSE.txt is accepted but not shipped: modules carry the
// repository's LICENSE. Anything else is rejected, never skipped.
var releaseMembers = map[string]int64{
	"libchdb.a":       4 << 30,
	"chdb.h":          16 << 20,
	"chdb.hpp":        16 << 20,
	splitManifestName: maxSplitManifestBytes,
	"LICENSE.txt":     16 << 20,
}

func extract(path, dir, headerSHA string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	seen := map[string]bool{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		limit, ok := releaseMembers[h.Name]
		if h.Typeflag != tar.TypeReg || !ok || h.Size < 0 || h.Size > limit {
			return fmt.Errorf("unexpected release member %q", h.Name)
		}
		if seen[h.Name] {
			return fmt.Errorf("duplicate release member %q", h.Name)
		}
		seen[h.Name] = true
		out, err := os.OpenFile(filepath.Join(dir, h.Name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		_, e := io.Copy(out, tr)
		ce := out.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
	}
	if !seen["libchdb.a"] || !seen["chdb.h"] {
		return fmt.Errorf("release is missing libchdb.a or chdb.h")
	}
	return checkDigest(filepath.Join(dir, "chdb.h"), headerSHA)
}

func writeModule(out, module, version string, stamp time.Time, files map[string][]byte, paths map[string]string) ([]File, error) {
	var names []string
	var total int64
	for n, b := range files {
		names = append(names, n)
		total += int64(len(b))
	}
	for n, p := range paths {
		if _, ok := files[n]; ok {
			return nil, fmt.Errorf("duplicate module file %s", n)
		}
		s, e := os.Stat(p)
		if e != nil {
			return nil, e
		}
		if !s.Mode().IsRegular() || s.Size() > MaxFileBytes {
			return nil, fmt.Errorf("invalid native file %s", p)
		}
		names = append(names, n)
		total += s.Size()
	}
	if total > MaxModuleBytes {
		return nil, fmt.Errorf("module %s has %d uncompressed bytes", module, total)
	}
	sort.Strings(names)
	dir := filepath.Join(out, "proxy", filepath.FromSlash(module), "@v")
	if e := os.MkdirAll(dir, 0755); e != nil {
		return nil, e
	}
	zipPath := filepath.Join(dir, version+".zip")
	f, e := os.OpenFile(zipPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if e != nil {
		return nil, e
	}
	zw := zip.NewWriter(f)
	zw.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) { return flate.NewWriter(w, flate.BestSpeed) })
	for _, n := range names {
		if path.Clean(n) != n || strings.HasPrefix(n, "/") || n == "." || n == ".." || strings.HasPrefix(n, "../") || strings.Contains(n, "\\") {
			f.Close()
			return nil, fmt.Errorf("invalid module filename %q", n)
		}
		h := &zip.FileHeader{Name: module + "@" + version + "/" + n, Method: zip.Deflate}
		h.SetMode(0644)
		w, err := zw.CreateHeader(h)
		if err != nil {
			f.Close()
			return nil, err
		}
		if b, ok := files[n]; ok {
			_, err = w.Write(b)
		} else {
			var in *os.File
			in, err = os.Open(paths[n])
			if err == nil {
				_, err = io.Copy(w, in)
				in.Close()
			}
		}
		if err != nil {
			f.Close()
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	st, e := os.Stat(zipPath)
	if e != nil {
		return nil, e
	}
	if st.Size() > 500<<20 {
		return nil, fmt.Errorf("module zip exceeds Go's 500 MiB limit")
	}
	info, e := json.Marshal(struct {
		Version string
		Time    time.Time
	}{version, stamp.UTC()})
	if e != nil {
		return nil, e
	}
	for suffix, data := range map[string][]byte{".mod": files["go.mod"], ".info": info} {
		if e = os.WriteFile(filepath.Join(dir, version+suffix), data, 0644); e != nil {
			return nil, e
		}
	}
	// Local file proxies need list for version queries. The production origin
	// derives it from immutable .info keys instead of overwriting an index.
	if e = os.WriteFile(filepath.Join(dir, "list"), []byte(version+"\n"), 0644); e != nil {
		return nil, e
	}
	var result []File
	for _, suffix := range []string{".zip", ".mod", ".info"} {
		path := filepath.Join(dir, version+suffix)
		r, err := describe(out, path)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, nil
}

func describe(base, path string) (File, error) {
	f, e := os.Open(path)
	if e != nil {
		return File{}, e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, f)
	if e != nil {
		return File{}, e
	}
	rel, e := filepath.Rel(base, path)
	return File{Path: filepath.ToSlash(rel), Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, e
}
func checkDigest(path, want string) error {
	got, e := describe(filepath.Dir(path), path)
	if e != nil {
		return e
	}
	if !isDigest(want) || got.SHA256 != want {
		return fmt.Errorf("SHA-256 mismatch for %s: got %s, want %s", path, got.SHA256, want)
	}
	return nil
}
func writeJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(path, append(b, '\n'), 0644)
}
func goSource(s string) ([]byte, error) { return format.Source([]byte(s)) }
