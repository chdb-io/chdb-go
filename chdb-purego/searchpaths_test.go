package chdbpurego

import (
	"path/filepath"
	"runtime"
	"testing"
)

// Before this package resolved paths itself, it passed a bare name to dlopen and
// so reached every install the loader knows about: registered with ldconfig, in
// /usr/lib/<triple> on a multiarch distribution, reached through a RUNPATH. A
// fixed list of directories cannot cover those, so the name has to stay a
// candidate or those installs stop working.
func TestSearchPathsDelegatesToTheLoaderLast(t *testing.T) {
	got := searchPaths()
	if len(got) == 0 {
		t.Fatal("no candidates at all")
	}

	var byLoader []attempt
	for _, a := range got {
		if a.byLoader {
			byLoader = append(byLoader, a)
		}
	}
	if len(byLoader) != len(libFileNames()) {
		t.Errorf("got %d candidates for the loader, want one per library name (%d)",
			len(byLoader), len(libFileNames()))
	}

	// Last, because every location this package can name is preferable: those
	// can be reported back to a caller, and a name cannot.
	tail := got[len(got)-len(libFileNames()):]
	for _, a := range tail {
		if !a.byLoader {
			t.Errorf("candidate %q (%s) comes after the loader ones", a.path, a.origin)
		}
	}

	// A name, not a path — a relative path would be rejected outright by a binary
	// built with the macOS hardened runtime.
	for _, a := range byLoader {
		if filepath.Base(a.path) != a.path {
			t.Errorf("loader candidate %q is a path rather than a bare name", a.path)
		}
	}
}

func TestSearchPathsIncludesLibraryPathEnv(t *testing.T) {
	env := "LD_LIBRARY_PATH"
	if runtime.GOOS == "darwin" {
		env = "DYLD_LIBRARY_PATH"
	}
	dir := t.TempDir()
	t.Setenv(env, dir)

	for _, a := range searchPaths() {
		if a.origin == env && filepath.Dir(a.path) == dir {
			return
		}
	}
	t.Errorf("a directory named by %s is never tried", env)
}

// A relative entry there would otherwise become a relative path handed to dlopen.
func TestSearchPathsMakesLibraryPathEntriesAbsolute(t *testing.T) {
	env := "LD_LIBRARY_PATH"
	if runtime.GOOS == "darwin" {
		env = "DYLD_LIBRARY_PATH"
	}
	t.Setenv(env, "relative/dir")

	seen := false
	for _, a := range searchPaths() {
		if a.origin != env {
			continue
		}
		seen = true
		if !filepath.IsAbs(a.path) {
			t.Errorf("%s candidate %q is not absolute", env, a.path)
		}
	}
	if !seen {
		t.Errorf("no candidate came from %s", env)
	}
}
