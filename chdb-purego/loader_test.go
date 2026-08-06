package chdbpurego

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestMain fails the whole package with the loader's diagnostic instead of
// letting individual tests skip on a confusing precondition. A test run that
// cannot load the engine is a broken environment, not a skipped feature.
func TestMain(m *testing.M) {
	if os.Getenv("CHDB_LOADER_CHILD") == "" {
		if err := ensureLoaded(); err != nil {
			os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}

func TestLibFileNamesCoversPublishedNaming(t *testing.T) {
	names := libFileNames()
	if len(names) == 0 {
		t.Fatal("no candidate file names")
	}
	// The macOS release archive ships the Mach-O library named libchdb.so, so
	// darwin must try that name and not only the .dylib spelling a Homebrew
	// install uses. Getting this wrong makes a correctly placed library
	// invisible.
	if !contains(names, "libchdb.so") {
		t.Errorf("libchdb.so missing from candidates %v", names)
	}
	if runtime.GOOS == "darwin" && !contains(names, "libchdb.dylib") {
		t.Errorf("libchdb.dylib missing from darwin candidates %v", names)
	}
}

func TestSearchPathsPrefersExecutableDirectory(t *testing.T) {
	paths := searchPaths()
	if len(paths) == 0 {
		t.Fatal("no search paths")
	}

	firstExeDir, firstSystem := -1, -1
	for i, p := range paths {
		if p.origin == "next to executable" && firstExeDir < 0 {
			firstExeDir = i
		}
		if p.origin == "system path" && firstSystem < 0 {
			firstSystem = i
		}
	}
	if firstExeDir < 0 {
		t.Fatal("executable directory is never searched")
	}
	if firstSystem < 0 {
		t.Fatal("system paths are never searched")
	}
	// A program shipping a known-good engine beside itself must not be
	// silently overridden by an unrelated copy in /usr/local/lib.
	if firstExeDir > firstSystem {
		t.Errorf("executable directory (%d) searched after system paths (%d)", firstExeDir, firstSystem)
	}
}

func TestSearchPathsAreAbsolute(t *testing.T) {
	// A binary built with the macOS hardened runtime refuses relative library
	// paths and reports it as a path error, which sends debugging in the wrong
	// direction. Every candidate must therefore already be absolute.
	for _, p := range searchPaths() {
		if !filepath.IsAbs(p.path) {
			t.Errorf("%s candidate is not absolute: %q", p.origin, p.path)
		}
	}
}

func TestNotFoundErrorListsEveryAttemptAndAFix(t *testing.T) {
	err := &notFoundError{attempts: []attempt{
		{origin: "next to executable", path: "/app/libchdb.so", err: errString("no such file")},
		{origin: "system path", path: "/usr/local/lib/libchdb.so", err: errString("no such file")},
	}}
	msg := err.Error()

	for _, want := range []string{"/app/libchdb.so", "/usr/local/lib/libchdb.so", "no such file", LibPathEnv} {
		if !strings.Contains(msg, want) {
			t.Errorf("error text is missing %q:\n%s", want, msg)
		}
	}
}

func TestTrimDlopenNoiseKeepsFirstConcreteReason(t *testing.T) {
	// dyld reports every probe path it walked, which buries the real reason.
	raw := `dlopen(/c/libchdb.so, 0x0006): tried: '/c/libchdb.so' (no such file), ` +
		`'/System/Volumes/Preboot/Cryptexes/OS/c/libchdb.so' (no such file)`
	got := trimDlopenNoise(raw)
	if !strings.Contains(got, "no such file") {
		t.Errorf("reason lost: %q", got)
	}
	if strings.Contains(got, "Cryptexes") {
		t.Errorf("probe-path noise kept: %q", got)
	}
}

// TestLibPathEnvOverrideIsTerminal checks that an explicit CHDB_LIB_PATH which
// cannot be loaded produces an error naming that variable, rather than quietly
// loading some other copy of the engine. Loading happens once per process, so
// this runs in a child process with the environment set.
func TestLibPathEnvOverrideIsTerminal(t *testing.T) {
	if os.Getenv("CHDB_LOADER_CHILD") == "1" {
		_, err := NewConnectionFromConnString(":memory:")
		if err == nil {
			os.Stderr.WriteString("connected despite a bogus " + LibPathEnv + "\n")
			os.Exit(3)
		}
		os.Stderr.WriteString(err.Error())
		os.Exit(4)
	}

	bogus := filepath.Join(t.TempDir(), "not-a-library.so")
	if err := os.WriteFile(bogus, []byte("this is not a shared library"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestLibPathEnvOverrideIsTerminal", "-test.v")
	cmd.Env = append(os.Environ(), "CHDB_LOADER_CHILD=1", LibPathEnv+"="+bogus)
	out, err := cmd.CombinedOutput()

	exit := 0
	if ee, ok := err.(*exec.ExitError); ok {
		exit = ee.ExitCode()
	}
	if exit == 3 {
		t.Fatalf("a bogus %s did not prevent loading:\n%s", LibPathEnv, out)
	}
	if exit != 4 {
		t.Fatalf("child exited %d, want 4:\n%s", exit, out)
	}

	msg := string(out)
	if !strings.Contains(msg, LibPathEnv) {
		t.Errorf("error does not name %s:\n%s", LibPathEnv, msg)
	}
	// The whole point of the override being terminal: no other location may
	// appear in the diagnostic, because none was tried.
	if strings.Contains(msg, "/usr/local/lib") {
		t.Errorf("fell back to the default search despite an explicit override:\n%s", msg)
	}
}

// TestLoadedLibraryPathIsReportable is what a build-verification job uses to
// confirm which engine a binary resolved.
func TestLoadedLibraryPathIsReportable(t *testing.T) {
	path, err := LoadedLibraryPath()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(path) {
		t.Errorf("reported path is not absolute: %q", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("reported path does not exist: %v", err)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
