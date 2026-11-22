package staticbundle

import (
	"os"
	"os/exec"
	"path"
	"strings"
	"testing"
)

// modulePath returns the path of a go.mod's module directive. It covers the
// plain and quoted forms; golang.org/x/mod is not a dependency of this module.
func modulePath(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "module" {
			return strings.Trim(f[1], "\"`")
		}
	}
	return ""
}

// Go finds a nested module's source and tags (dir/vX.Y.Z) in the directory its
// path names below the repository root. A /vN element is a major version only
// at the end of a path; anywhere else it is a directory name. The Arrow module
// github.com/chdb-io/chdb-go/v2/arrow once lived in arrow/, where no tag could
// publish it: Go looked in v2/arrow/ for tags v2/arrow/vX.Y.Z.
func TestNestedModulePathsMatchTheirDirectories(t *testing.T) {
	const repoPath = "github.com/chdb-io/chdb-go"
	out, err := exec.Command("git", "-C", "../..", "ls-files", "go.mod", "*/go.mod").Output()
	if err != nil {
		t.Skip("git checkout required")
	}
	seen := 0
	for _, file := range strings.Fields(string(out)) {
		data, err := os.ReadFile("../../" + file)
		if err != nil {
			t.Fatal(err)
		}
		dir := path.Dir(file)
		want := repoPath + "/" + dir
		if dir == "." {
			want = RootModule
		}
		if got := modulePath(data); got != want {
			t.Errorf("%s declares %s; tags %s/vX.Y.Z only publish %s", file, got, dir, want)
		}
		seen++
	}
	if seen < 2 {
		t.Fatalf("found %d modules", seen)
	}
}

// The root major-version subdirectory v2/ must never hold its own go.mod: Go
// would then take the root module v2 from v2/ instead of the repository root.
func TestRootModuleStaysAtRepositoryRoot(t *testing.T) {
	if _, err := os.Stat("../../v2/go.mod"); !os.IsNotExist(err) {
		t.Fatalf("v2/go.mod would move root module %s: %v", RootModule, err)
	}
	data, err := os.ReadFile("../../" + ArrowDir + "/go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if got := modulePath(data); got != RootModule+"/arrow" {
		t.Fatalf("%s/go.mod declares %s", ArrowDir, got)
	}
}
