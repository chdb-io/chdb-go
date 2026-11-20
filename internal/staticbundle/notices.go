package staticbundle

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

const MaxNoticeBytes int64 = 16 << 20

var noticeNames = []string{"SOURCE_PROVENANCE.json", "STATIC_RELINKING.md", "THIRD_PARTY_NOTICES.txt"}
var commitSHA = regexp.MustCompile(`^[a-f0-9]{40}$`)

// Notice checks establish delivery and byte integrity, not legal approval. The
// separately reviewed source/relink material must actually be made available.
func loadNotices(dir, engine string) (map[string][]byte, error) {
	files := map[string][]byte{}
	if dir == "" {
		return files, nil // local/legacy bundles remain usable but not publishable
	}
	var total int64
	for _, name := range noticeNames {
		p := filepath.Join(dir, name)
		st, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		if !st.Mode().IsRegular() || st.Size() < 1 || st.Size() > MaxNoticeBytes-total {
			return nil, fmt.Errorf("invalid or oversized release notice %s", name)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		if int64(len(data)) != st.Size() {
			return nil, fmt.Errorf("release notice changed while reading %s", name)
		}
		files[name] = data
		total += int64(len(data))
	}
	if err := checkNoticeProvenance(files, engine); err != nil {
		return nil, err
	}
	return files, nil
}

func checkNoticeProvenance(files map[string][]byte, engine string) error {
	var p struct {
		Engine string `json:"engine"`
		Commit string `json:"core_commit"`
	}
	if json.Unmarshal(files["SOURCE_PROVENANCE.json"], &p) != nil || p.Engine != engine || !commitSHA.MatchString(p.Commit) {
		return fmt.Errorf("notice source provenance must pin the selected engine and exact core commit")
	}
	return nil
}

func noticeInventory(files map[string][]byte) []File {
	var result []File
	for name, data := range files {
		result = append(result, File{Path: name, SHA256: digestBytes(data), Size: int64(len(data))})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result
}

func sameNotices(a, b []File) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func checkNoticeInventory(files []File) error {
	if len(files) == 0 {
		return nil
	}
	if len(files) != len(noticeNames) {
		return fmt.Errorf("release notice inventory must contain provenance, relinking guidance and third-party notices")
	}
	var total int64
	for i, f := range files {
		if f.Path != noticeNames[i] || !isDigest(f.SHA256) || f.Size < 1 || f.Size > MaxNoticeBytes-total {
			return fmt.Errorf("invalid release notice inventory")
		}
		total += f.Size
	}
	return nil
}

func zipNotices(filename, module, version string, expected []File, engine string) (map[string][]byte, error) {
	result := map[string][]byte{}
	if len(expected) == 0 {
		return result, nil
	}
	if err := checkNoticeInventory(expected); err != nil {
		return nil, err
	}
	z, err := zip.OpenReader(filename)
	if err != nil {
		return nil, err
	}
	defer z.Close()
	prefix := module + "@" + version + "/"
	hasLicense := false
	for _, f := range z.File {
		if f.Name == prefix+"LICENSE" && f.UncompressedSize64 > 0 {
			hasLicense = true
		}
		for _, want := range expected {
			if f.Name != prefix+want.Path {
				continue
			}
			if f.UncompressedSize64 != uint64(want.Size) {
				return nil, fmt.Errorf("module %s notice size differs: %s", module, want.Path)
			}
			r, err := f.Open()
			if err != nil {
				return nil, err
			}
			data, readErr := io.ReadAll(io.LimitReader(r, want.Size+1))
			r.Close()
			if readErr != nil || int64(len(data)) != want.Size || digestBytes(data) != want.SHA256 {
				return nil, fmt.Errorf("module %s notice content differs: %s", module, want.Path)
			}
			result[want.Path] = data
		}
	}
	if !hasLicense || len(result) != len(expected) {
		return nil, fmt.Errorf("module %s is missing LICENSE or release notice material", module)
	}
	return result, checkNoticeProvenance(result, engine)
}
