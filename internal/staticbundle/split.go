package staticbundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// splitManifestName is the manifest chdb-core ships beside the split Linux
// libchdb.a (chdb-io/chdb-core#342). Its shards are already isolated by
// split_static_lib.py; they are copied into .syso files as they are, never
// relinked, renamed or stripped here.
const splitManifestName = "libchdb-split.json"
const splitManifestFormat = 1
const maxSplitManifestBytes = 1 << 20

// Linux system libraries the static adapter links (chdb-purego/binding_static.go).
// A manifest that needs anything else must fail packaging, not the user's link.
var linkedLinuxLibraries = map[string]bool{"-lc": true, "-lm": true, "-lrt": true, "-lpthread": true, "-ldl": true}

type splitPart struct {
	Index  int    `json:"index"`
	Member string `json:"member"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type splitManifest struct {
	Format        int      `json:"format"`
	Platform      string   `json:"platform"`
	Version       string   `json:"version"`
	SourceCommit  string   `json:"source_commit"`
	Archive       string   `json:"archive"`
	ArchiveSize   int64    `json:"archive_size"`
	ArchiveSHA256 string   `json:"archive_sha256"`
	LinkOrder     []string `json:"link_order"`
	Link          struct {
		SystemLibraries []string `json:"system_libraries"`
	} `json:"link"`
	Parts []splitPart `json:"parts"`
}

func splitPlatform(platform string) string {
	switch platform {
	case "linux-amd64":
		return "linux-x86_64"
	case "linux-arm64":
		return "linux-aarch64"
	}
	return ""
}

// readSplitManifest checks the manifest's own claims against the pinned
// release. checkSplitArchive then checks them against the archive bytes.
func readSplitManifest(path, platform string, r Release) (splitManifest, error) {
	var m splitManifest
	st, err := os.Stat(path)
	if err != nil {
		return m, err
	}
	if !st.Mode().IsRegular() || st.Size() > maxSplitManifestBytes {
		return m, fmt.Errorf("invalid split manifest")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	if err = json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("split manifest: %w", err)
	}
	switch {
	case m.Format != splitManifestFormat:
		return m, fmt.Errorf("split manifest format %d, want %d", m.Format, splitManifestFormat)
	case splitPlatform(platform) == "" || m.Platform != splitPlatform(platform):
		return m, fmt.Errorf("split manifest is for %q, not %s", m.Platform, platform)
	case m.Version != strings.TrimPrefix(r.Engine, "v"):
		return m, fmt.Errorf("split manifest version %q, release pins %s", m.Version, r.Engine)
	case !isCommit(r.SourceCommit) || m.SourceCommit != r.SourceCommit:
		return m, fmt.Errorf("split manifest source commit %q, release pins %q", m.SourceCommit, r.SourceCommit)
	case m.Archive != "libchdb.a" || m.ArchiveSize <= 0 || !isDigest(m.ArchiveSHA256):
		return m, fmt.Errorf("split manifest must pin libchdb.a size and SHA-256")
	case len(m.Parts) == 0 || len(m.LinkOrder) != len(m.Parts):
		return m, fmt.Errorf("split manifest link order does not list every part")
	}
	seen := map[string]bool{}
	for i, p := range m.Parts {
		if p.Index != i || p.Member != m.LinkOrder[i] || seen[p.Member] || filepath.Base(p.Member) != p.Member || p.Size <= 0 || !isDigest(p.SHA256) {
			return m, fmt.Errorf("invalid split manifest part %d", i)
		}
		seen[p.Member] = true
		if p.Size > MaxFileBytes {
			return m, fmt.Errorf("split part %s has %d bytes, over the %d byte native file limit", p.Member, p.Size, MaxFileBytes)
		}
	}
	for _, lib := range m.Link.SystemLibraries {
		if !linkedLinuxLibraries[lib] {
			return m, fmt.Errorf("split manifest requires %s, which the static adapter does not link", lib)
		}
	}
	return m, nil
}

// checkSplitArchive requires the archive to be exactly the manifest's parts,
// in link order. Archive order is static-constructor order.
func checkSplitArchive(m splitManifest, f *os.File, objects []member) error {
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() != m.ArchiveSize {
		return fmt.Errorf("split archive has %d bytes, manifest pins %d", st.Size(), m.ArchiveSize)
	}
	h := sha256.New()
	if _, err = io.Copy(h, io.NewSectionReader(f, 0, st.Size())); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != m.ArchiveSHA256 {
		return fmt.Errorf("split archive SHA-256 %s, manifest pins %s", got, m.ArchiveSHA256)
	}
	if len(objects) != len(m.Parts) {
		return fmt.Errorf("split archive has %d members, manifest lists %d", len(objects), len(m.Parts))
	}
	for i, o := range objects {
		p := m.Parts[i]
		if o.name != p.Member || o.size != p.Size {
			return fmt.Errorf("split archive member %d is %s (%d bytes), manifest lists %s (%d bytes)", i, o.name, o.size, p.Member, p.Size)
		}
		h := sha256.New()
		if _, err = io.Copy(h, io.NewSectionReader(f, o.offset, o.size)); err != nil {
			return err
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != p.SHA256 {
			return fmt.Errorf("split archive member %s SHA-256 %s, manifest pins %s", p.Member, got, p.SHA256)
		}
	}
	return nil
}

// copySplitShards writes each verified part, unchanged, to its own .syso,
// numbered in link order.
func copySplitShards(work, platform string, f *os.File, objects []member) ([]string, error) {
	var out []string
	for i, o := range objects {
		if o.size > MaxFileBytes {
			return nil, fmt.Errorf("split part %s exceeds %d bytes", o.name, MaxFileBytes)
		}
		p := filepath.Join(work, fmt.Sprintf("p%04d_%s.syso", i, strings.ReplaceAll(platform, "-", "_")))
		dest, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err != nil {
			return nil, err
		}
		_, copyErr := io.Copy(dest, io.NewSectionReader(f, o.offset, o.size))
		closeErr := dest.Close()
		if copyErr != nil {
			return nil, copyErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		out = append(out, p)
	}
	return out, nil
}

func isCommit(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 20 && s == strings.ToLower(s)
}
