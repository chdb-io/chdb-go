package staticbundle

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestManualStaticCGOPreambleCompilesOnLinux(t *testing.T) {
	if _, err := exec.LookPath("cc"); err != nil {
		t.Skip("C compiler required")
	}
	d := t.TempDir()
	source, err := filepath.Abs("../../chdb-purego/binding_static_manual.go")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "tool", "cgo", "-objdir", d, source)
	cmd.Env = append(cleanEnv(os.Environ(), "GOOS", "GOARCH", "CGO_ENABLED", "CC", "CGO_CFLAGS", "CGO_CPPFLAGS", "CGO_LDFLAGS"), "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=1", "CC=cc")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate manual cgo preamble: %v\n%s", err, output)
	}
	// cgo emits this file even when a comment is invalid C. Compile the
	// generated translation unit, without needing Linux or the large archive.
	cmd = exec.Command("cc", "-fsyntax-only", filepath.Join(d, "binding_static_manual.cgo2.c"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile manual cgo preamble: %v\n%s", err, output)
	}
}

func TestArrowCIWorkspaceResolvesUnpublishedRootOffline(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq required by CI helper")
	}
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(d, "repo")
	rootMod := "module example.com/root/v2\n\ngo 1.21\n"
	// This fixture tests workspace resolution, not Arrow APIs. Keep it usable
	// with the root module's Go 1.21 minimum; real Arrow CI uses Go 1.23.2.
	arrowMod := "module example.com/root/v2/arrow\n\ngo 1.21\n\nrequire example.com/root/v2 v2.99.0-unpublished\n"
	put(t, filepath.Join(repo, "go.mod"), []byte(rootMod))
	put(t, filepath.Join(repo, "root.go"), []byte("package root\nconst Value = 42\n"))
	put(t, filepath.Join(repo, "arrow", "go.mod"), []byte(arrowMod))
	put(t, filepath.Join(repo, "arrow", "arrow.go"), []byte("package arrow\nimport root \"example.com/root/v2\"\nvar Value = root.Value\n"))
	helper, err := filepath.Abs("../../.github/scripts/prepare-arrow-workspace.sh")
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(d, "go.work")
	env := append(cleanEnv(os.Environ(), "GOWORK", "GOPROXY", "GOSUMDB", "GOFLAGS", "GOOS", "GOARCH", "GOTOOLCHAIN"), "GOWORK="+work, "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")
	cmd := exec.Command("bash", helper, repo)
	cmd.Env = env
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("prepare workspace: %v\n%s", err, output)
	}
	for _, args := range [][]string{{"list", "-m", "all"}, {"test", "./..."}} {
		cmd = exec.Command("go", args...)
		cmd.Dir, cmd.Env = filepath.Join(repo, "arrow"), env
		if output, err := cmd.CombinedOutput(); err != nil {
			data, _ := os.ReadFile(work)
			t.Fatalf("offline %v: %v\n%s\nworkspace:\n%s", args, err, output, data)
		}
	}
	data, err := os.ReadFile(work)
	if err != nil || !strings.Contains(string(data), "example.com/root/v2 v2.99.0-unpublished => "+repo) {
		t.Fatalf("missing version-specific workspace replacement: %v\n%s", err, data)
	}
	for path, want := range map[string]string{"go.mod": rootMod, "arrow/go.mod": arrowMod} {
		got, err := os.ReadFile(filepath.Join(repo, path))
		if err != nil || string(got) != want {
			t.Fatalf("CI helper modified source %s: %v\n%s", path, err, got)
		}
	}
}
