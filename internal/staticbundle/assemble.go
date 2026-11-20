package staticbundle

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chdb-io/chdb-go/v2/internal/enginetag"
)

// ReadBundle checks every byte described by the receipt, and accepts only the
// exact versioned proxy paths owned by the modules in that receipt. Mutable
// list indexes, traversal paths, symlinks and unrelated uploads are refused.
func ReadBundle(dir string) (Bundle, error) {
	var b Bundle
	realRoot, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return b, err
	}
	realRoot, err = filepath.Abs(realRoot)
	if err != nil {
		return b, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "bundle.json"))
	if err != nil {
		return b, err
	}
	if err = json.Unmarshal(data, &b); err != nil {
		return b, err
	}
	if b.Schema != 1 || ValidatePrefix(b.Prefix) != nil || !bindingRE.MatchString(b.Bindings) || !isDigest(b.HeaderSHA256) || b.Time.IsZero() {
		return b, fmt.Errorf("invalid bundle identity")
	}
	engine, counter, err := enginetag.Engine(b.Version)
	if err != nil || engine != b.Engine {
		return b, fmt.Errorf("invalid bundle version")
	}
	canonical, err := enginetag.Module(engine, counter)
	if err != nil || canonical != b.Version {
		return b, fmt.Errorf("noncanonical bundle version")
	}
	if b.Complete && len(b.Platforms) != len(Platforms) {
		return b, fmt.Errorf("complete bundle requires all platforms")
	}
	if err = checkNoticeInventory(b.Notices); err != nil {
		return b, err
	}
	allowed := map[string]bool{}
	if b.Complete {
		addPaths(allowed, b.Prefix+"/static", b.Version)
	}
	seen := map[string]bool{}
	for _, n := range b.Platforms {
		if !supported(n.Platform) || seen[n.Platform] || n.Objects < 1 || n.Shards < 1 || len(n.Modules) < 1 || !isDigest(n.SourceSHA256) {
			return b, fmt.Errorf("invalid platform %s", n.Platform)
		}
		seen[n.Platform] = true
		for i, m := range n.Modules {
			if m != fmt.Sprintf("%s/libchdb/%s/part%d", b.Prefix, n.Platform, i) {
				return b, fmt.Errorf("invalid native module %s", m)
			}
			addPaths(allowed, m, b.Version)
		}
	}
	// Partial assemblies contain a static entry module too, but cannot be
	// published. Detect it by the receipt's explicit path, not a directory walk.
	staticZip := filepath.ToSlash(filepath.Join("proxy", b.Prefix, "static", "@v", b.Version+".zip"))
	for _, f := range b.Files {
		if f.Path == staticZip {
			addPaths(allowed, b.Prefix+"/static", b.Version)
		}
	}
	if len(allowed) != len(b.Files) {
		return b, fmt.Errorf("incomplete or duplicated proxy file inventory")
	}
	for _, f := range b.Files {
		if !allowed[f.Path] || !isDigest(f.SHA256) || f.Size < 0 {
			return b, fmt.Errorf("unexpected bundle file %q", f.Path)
		}
		delete(allowed, f.Path)
		p := filepath.Join(dir, filepath.FromSlash(f.Path))
		st, err := os.Lstat(p)
		if err != nil {
			return b, err
		}
		if !st.Mode().IsRegular() || st.Size() != f.Size {
			return b, fmt.Errorf("invalid bundle file %s", f.Path)
		}
		realFile, err := filepath.EvalSymlinks(p)
		if err == nil {
			realFile, err = filepath.Abs(realFile)
		}
		if err != nil || realFile != filepath.Join(realRoot, filepath.FromSlash(f.Path)) {
			return b, fmt.Errorf("bundle path traverses a symlink: %s", f.Path)
		}
		if err = checkDigest(p, f.SHA256); err != nil {
			return b, err
		}
		if strings.HasSuffix(f.Path, ".zip") {
			module := strings.TrimSuffix(strings.TrimPrefix(f.Path, "proxy/"), "/@v/"+b.Version+".zip")
			if err = checkZipDirectory(p, module, b.Version); err != nil {
				return b, err
			}
			if _, err = zipNotices(p, module, b.Version, b.Notices, b.Engine); err != nil {
				return b, err
			}
		}
	}
	return b, nil
}

func checkZipDirectory(filename, module, version string) error {
	z, err := zip.OpenReader(filename)
	if err != nil {
		return err
	}
	defer z.Close()
	if z.File == nil {
		return fmt.Errorf("empty module zip")
	}
	st, err := os.Stat(filename)
	if err != nil {
		return err
	}
	if st.Size() > 500<<20 {
		return fmt.Errorf("module zip exceeds 500 MiB")
	}
	var total uint64
	seen := map[string]bool{}
	prefix := module + "@" + version + "/"
	for _, f := range z.File {
		n := strings.TrimPrefix(f.Name, prefix)
		if n == f.Name || n == "" || seen[n] || path.Clean(n) != n || strings.HasPrefix(n, "/") || strings.HasPrefix(n, "../") || n == ".." || strings.Contains(n, "\\") || !f.Mode().IsRegular() {
			return fmt.Errorf("invalid module zip entry %q", f.Name)
		}
		seen[n] = true
		limit := uint64(MaxFileBytes)
		if n == "go.mod" || n == "LICENSE" {
			limit = 16 << 20
		}
		if f.UncompressedSize64 > limit || f.UncompressedSize64 > uint64(MaxModuleBytes)-total {
			return fmt.Errorf("module zip exceeds uncompressed size limits")
		}
		total += f.UncompressedSize64
	}
	if !seen["go.mod"] {
		return fmt.Errorf("module zip is missing go.mod")
	}
	return nil
}
func supported(p string) bool {
	for _, s := range Platforms {
		if s == p {
			return true
		}
	}
	return false
}
func addPaths(m map[string]bool, module, version string) {
	for _, ext := range []string{".zip", ".mod", ".info"} {
		m["proxy/"+module+"/@v/"+version+ext] = true
	}
}

// Assemble combines native-runner receipts and generates the tiny opt-in
// module. Production requires all four targets. A partial development build
// has explicit compile failures for absent targets and is never publishable.
func Assemble(repo, out string, inputs []string, release Release, partial bool) (Bundle, error) {
	var result Bundle
	want, err := engVersion(release)
	if err != nil {
		return result, err
	}
	var bundles []Bundle
	var dirs []string
	seen := map[string]bool{}
	for _, dir := range inputs {
		b, err := ReadBundle(dir)
		if err != nil {
			return result, err
		}
		if b.Complete || len(b.Platforms) != 1 {
			return result, fmt.Errorf("assemble requires per-platform package receipts")
		}
		if b.Engine != release.Engine || b.Version != want || b.HeaderSHA256 != release.HeaderSHA256 || !b.Time.Equal(release.Time) {
			return result, fmt.Errorf("receipt differs from pinned release")
		}
		if result.Prefix == "" {
			result = b
			result.Platforms = nil
			result.Files = nil
		} else if result.Prefix != b.Prefix || result.Bindings != b.Bindings || !sameNotices(result.Notices, b.Notices) {
			return result, fmt.Errorf("mixed module prefixes, bindings versions or release notices")
		}
		n := b.Platforms[0]
		if seen[n.Platform] || n.SourceSHA256 != release.Platforms[n.Platform].SHA256 {
			return result, fmt.Errorf("duplicate or unpinned platform %s", n.Platform)
		}
		seen[n.Platform] = true
		result.Platforms = append(result.Platforms, n)
		bundles = append(bundles, b)
		dirs = append(dirs, dir)
	}
	if len(bundles) == 0 || !partial && len(seen) != len(Platforms) {
		return result, fmt.Errorf("all four native platform receipts are required (use -allow-partial only for local validation)")
	}
	if err = checkDigest(filepath.Join(repo, "chdb-purego", "static_chdb.h"), release.HeaderSHA256); err != nil {
		return result, err
	}
	if err = newOutput(out, repo); err != nil {
		return result, err
	}
	sort.Slice(result.Platforms, func(i, j int) bool { return result.Platforms[i].Platform < result.Platforms[j].Platform })
	for i, b := range bundles {
		for _, f := range b.Files {
			dest := filepath.Join(out, filepath.FromSlash(f.Path))
			if err = copyFile(filepath.Join(dirs[i], filepath.FromSlash(f.Path)), dest); err != nil {
				return result, err
			}
			result.Files = append(result.Files, f)
		}
		for _, n := range b.Platforms {
			for _, m := range n.Modules {
				if err = writeList(out, m, b.Version); err != nil {
					return result, err
				}
			}
		}
	}
	license, err := os.ReadFile(filepath.Join(repo, "LICENSE.txt"))
	if err != nil {
		return result, err
	}
	files := map[string][]byte{"LICENSE": license}
	first := bundles[0]
	firstModule := first.Platforms[0].Modules[0]
	notices, err := zipNotices(filepath.Join(dirs[0], "proxy", filepath.FromSlash(firstModule), "@v", first.Version+".zip"), firstModule, first.Version, first.Notices, first.Engine)
	if err != nil {
		return result, err
	}
	for name, data := range notices {
		files[name] = data
	}
	files["doc.go"] = []byte("// Package static opts into the prebuilt, statically linked chDB engine.\n// Build with CGO_ENABLED=1 and -tags chdb_static. No downloads run at init or runtime.\npackage static\n")
	mod := "module " + result.Prefix + "/static\n\ngo 1.21\n\nrequire (\n\t" + RootModule + " " + result.Bindings + "\n"
	for _, n := range result.Platforms {
		s := fmt.Sprintf("// Code generated by staticbundle; DO NOT EDIT.\n//go:build chdb_static && cgo && %s\n\npackage static\n\nimport (\n _ %q\n", strings.ReplaceAll(n.Platform, "-", " && "), RootModule+"/chdb-purego")
		for i, m := range n.Modules {
			s += fmt.Sprintf(" p%d %q\n", i, m)
			mod += "\t" + m + " " + result.Version + "\n"
		}
		s += ")\nfunc init() {\n"
		for i := range n.Modules {
			s += fmt.Sprintf("if p%d.Engine != %q || p%d.Version != %q { panic(%q) }\n", i, result.Engine, i, result.Version, "chdb: mixed native modules; require the exact static module release")
		}
		s += "}\n"
		files["select_"+strings.ReplaceAll(n.Platform, "-", "_")+".go"], err = goSource(s)
		if err != nil {
			return result, err
		}
	}
	for _, p := range Platforms {
		if !seen[p] {
			files["missing_"+strings.ReplaceAll(p, "-", "_")+".go"] = []byte("//go:build chdb_static && cgo && " + strings.ReplaceAll(p, "-", " && ") + "\n\npackage static\n\nvar _ = chdb_static_payload_not_packaged_for_this_platform\n")
		}
	}
	files["unsupported.go"] = []byte("//go:build !chdb_static || !cgo || !(linux || darwin) || !(amd64 || arm64)\n\npackage static\n\nvar _ = chdb_static_requires_tag_chdb_static_and_cgo_on_linux_or_darwin_amd64_or_arm64\n")
	files["go.mod"] = []byte(mod + ")\n")
	written, err := writeModule(out, result.Prefix+"/static", result.Version, result.Time, files, nil)
	if err != nil {
		return result, err
	}
	result.Files = append(result.Files, written...)
	result.Complete = len(seen) == len(Platforms)
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
	return result, writeJSON(filepath.Join(out, "bundle.json"), result)
}
func writeList(out, module, version string) error {
	return os.WriteFile(filepath.Join(out, "proxy", filepath.FromSlash(module), "@v", "list"), []byte(version+"\n"), 0644)
}
func copyFile(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	_, e := io.Copy(out, in)
	ce := out.Close()
	if e != nil {
		return e
	}
	return ce
}

// Kept here so tests can exercise the same engine-version policy as packaging.
func engVersion(r Release) (string, error) { return enginetag.Module(r.Engine, r.Packaging) }
