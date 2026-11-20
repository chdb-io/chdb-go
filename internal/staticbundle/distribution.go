package staticbundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type ModuleVersion struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

type Probe struct {
	Schema  int             `json:"schema"`
	Kind    string          `json:"kind"`
	Prefix  string          `json:"module_prefix"`
	Modules []ModuleVersion `json:"modules"`
	Files   []File          `json:"files"`
}

type distribution struct {
	Kind, Prefix, ReceiptSHA256 string
	Modules                     []ModuleVersion
	Files                       []File
}

var moduleVersionRE = regexp.MustCompile(`^v0\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-rc\.([1-9][0-9]*)\.([1-9][0-9]*))?$`)

// PrepareProbe reuses the existing module ZIP writer. Only six immutable files
// are publishable: list/latest belong to the small Vercel site, not Blob.
func PrepareProbe(repo, out, prefix string) (Probe, error) {
	p := Probe{Schema: 1, Kind: "probe", Prefix: prefix}
	if err := ValidatePrefix(prefix); err != nil {
		return p, err
	}
	if err := newOutput(out, repo); err != nil {
		return p, err
	}
	module := prefix + "/probe"
	for _, v := range []string{"v0.0.1", "v0.0.2"} {
		files := map[string][]byte{
			"go.mod":   []byte("module " + module + "\n\ngo 1.21\n"),
			"probe.go": []byte("package probe\n\nfunc Message() string { return \"chdb-vercel-blob-probe:" + v + "\" }\n"),
			"LICENSE":  []byte("SPDX-License-Identifier: CC0-1.0\nThis tiny connectivity probe is dedicated to the public domain under CC0 1.0.\n"),
		}
		written, err := writeModule(out, module, v, time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC), files, nil)
		if err != nil {
			return p, err
		}
		p.Modules = append(p.Modules, ModuleVersion{module, v})
		p.Files = append(p.Files, written...)
	}
	if err := os.WriteFile(filepath.Join(out, "proxy", filepath.FromSlash(module), "@v", "list"), []byte("v0.0.1\nv0.0.2\n"), 0644); err != nil {
		return p, err
	}
	return p, writeJSON(filepath.Join(out, "probe.json"), p)
}

func readDistribution(dir string) (distribution, error) {
	var d distribution
	_, probeErr := os.Stat(filepath.Join(dir, "probe.json"))
	_, bundleErr := os.Stat(filepath.Join(dir, "bundle.json"))
	if probeErr == nil && bundleErr == nil {
		return d, fmt.Errorf("ambiguous staging directory: both probe and engine receipts")
	}
	if probeErr == nil {
		data, err := os.ReadFile(filepath.Join(dir, "probe.json"))
		if err != nil {
			return d, err
		}
		var p Probe
		if err = json.Unmarshal(data, &p); err != nil {
			return d, err
		}
		if p.Schema != 1 || p.Kind != "probe" || ValidatePrefix(p.Prefix) != nil || len(p.Modules) != 2 {
			return d, fmt.Errorf("invalid tiny probe receipt")
		}
		for i, v := range []string{"v0.0.1", "v0.0.2"} {
			if p.Modules[i] != (ModuleVersion{p.Prefix + "/probe", v}) {
				return d, fmt.Errorf("unexpected probe module identity")
			}
		}
		if err = checkDistributionFiles(dir, p.Files, p.Modules, true); err != nil {
			return d, err
		}
		d = distribution{Kind: "probe", Prefix: p.Prefix, ReceiptSHA256: digestBytes(data), Modules: p.Modules, Files: p.Files}
		return d, nil
	}
	if !os.IsNotExist(probeErr) {
		return d, probeErr
	}
	b, err := ReadBundle(dir)
	if err != nil {
		return d, err
	}
	if !b.Complete {
		return d, fmt.Errorf("partial engine bundles cannot be published; finish the tiny public probe before engine release validation")
	}
	if len(b.Notices) == 0 {
		return d, fmt.Errorf("public engine modules require prepared -notices material; local validation bundles without it are not publishable")
	}
	data, err := os.ReadFile(filepath.Join(dir, "bundle.json"))
	if err != nil {
		return d, err
	}
	d = distribution{Kind: "engine", Prefix: b.Prefix, ReceiptSHA256: digestBytes(data), Files: b.Files}
	for _, n := range b.Platforms {
		for _, m := range n.Modules {
			d.Modules = append(d.Modules, ModuleVersion{m, b.Version})
		}
	}
	d.Modules = append(d.Modules, ModuleVersion{b.Prefix + "/static", b.Version})
	return d, nil
}

func digestBytes(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

func checkDistributionFiles(dir string, files []File, modules []ModuleVersion, tiny bool) error {
	allowed := map[string]bool{}
	for _, m := range modules {
		addPaths(allowed, m.Path, m.Version)
	}
	if len(allowed) != len(files) {
		return fmt.Errorf("incomplete or duplicated module file inventory")
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return err
	}
	var total int64
	for _, f := range files {
		if !allowed[f.Path] || !isDigest(f.SHA256) || f.Size < 0 {
			return fmt.Errorf("unexpected distribution file %q", f.Path)
		}
		delete(allowed, f.Path)
		total += f.Size
		if tiny && total > 1<<20 {
			return fmt.Errorf("tiny probe must remain below 1 MiB")
		}
		p := filepath.Join(root, filepath.FromSlash(f.Path))
		st, err := os.Lstat(p)
		if err != nil {
			return err
		}
		actual, err := filepath.EvalSymlinks(p)
		if err != nil {
			return err
		}
		if !st.Mode().IsRegular() || st.Size() != f.Size || actual != p {
			return fmt.Errorf("invalid staged file or symlink: %s", f.Path)
		}
		if err = checkDigest(p, f.SHA256); err != nil {
			return err
		}
		if strings.HasSuffix(f.Path, ".zip") {
			for _, m := range modules {
				if f.Path == "proxy/"+m.Path+"/@v/"+m.Version+".zip" {
					if err = checkZipDirectory(p, m.Path, m.Version); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func orderedFiles(input []File, prefix string) []File {
	files := append([]File(nil), input...)
	rank := func(f File) int {
		r := 0
		if strings.HasPrefix(f.Path, "proxy/"+prefix+"/static/") {
			r = 10
		}
		switch filepath.Ext(f.Path) {
		case ".zip":
			return r
		case ".mod":
			return r + 1
		default:
			return r + 2
		}
	}
	sort.Slice(files, func(i, j int) bool {
		a, b := rank(files[i]), rank(files[j])
		if a != b {
			return a < b
		}
		return files[i].Path < files[j].Path
	})
	return files
}
