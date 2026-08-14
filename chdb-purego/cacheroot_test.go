package chdbpurego

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequirePrivateRootAccepts(t *testing.T) {
	// 0o755 is what an earlier version of this package created, and readable by
	// others is not writable by them, so an existing cache must keep working.
	for _, mode := range []os.FileMode{0o700, 0o750, 0o755} {
		root := filepath.Join(t.TempDir(), "chdb-go")
		if err := os.Mkdir(root, mode); err != nil {
			t.Fatal(err)
		}
		// Mkdir applies the umask, so set the mode we mean explicitly.
		if err := os.Chmod(root, mode); err != nil {
			t.Fatal(err)
		}
		if err := requirePrivateRoot(root); err != nil {
			t.Errorf("mode %#o was rejected: %v", mode, err)
		}
	}
}

func TestRequirePrivateRootRejectsWritableByOthers(t *testing.T) {
	// The case this exists for: a cache root on a shared /tmp that another user
	// can create the digest directory inside, so the fast path would load a
	// library this build never carried.
	for _, mode := range []os.FileMode{0o777, 0o770, 0o707, 0o702, 0o720} {
		root := filepath.Join(t.TempDir(), "chdb-go")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(root, mode); err != nil {
			t.Fatal(err)
		}
		err := requirePrivateRoot(root)
		if err == nil {
			t.Errorf("mode %#o was accepted", mode)
			continue
		}
		if !strings.Contains(err.Error(), "writable by other users") {
			t.Errorf("mode %#o rejected for the wrong reason: %v", mode, err)
		}
	}
}

func TestRequirePrivateRootRejectsNonDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "chdb-go")
	if err := os.WriteFile(root, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := requirePrivateRoot(root); err == nil {
		t.Error("a regular file was accepted as a cache root")
	}
}

// The whole point of the check is that extraction refuses such a root rather than
// loading whatever is found in it, so assert it at the caller too.
func TestExtractIntoRefusesRootWritableByOthers(t *testing.T) {
	root := filepath.Join(t.TempDir(), "chdb-go")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}

	engine, _ := fakeEngine([]byte("payload"))

	// Plant the file the fast path would otherwise return, the way another user
	// on a shared root could.
	planted := filepath.Join(root, engine.Digest)
	if err := os.MkdirAll(planted, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planted, engine.FileName), []byte("not the engine"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatal(err)
	}

	if path, err := extractInto(root, engine); err == nil {
		t.Errorf("extraction accepted a world-writable root and returned %s", path)
	}
}

// Owning the root is not enough: whoever can write to a directory on the way to it
// can rename it aside and leave a private-looking one of their own in its place.
func TestRequirePrivateRootRejectsSwappableAncestor(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "chdb-go")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := requirePrivateRoot(root); err != nil {
		t.Fatalf("a private chain was rejected: %v", err)
	}

	// World-writable and not sticky: anyone can rename chdb-go aside.
	if err := os.Chmod(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	err := requirePrivateRoot(root)
	if err == nil {
		t.Fatal("a world-writable non-sticky ancestor was accepted")
	}
	if !strings.Contains(err.Error(), "not sticky") {
		t.Errorf("rejected for the wrong reason: %v", err)
	}

	// Sticky is what makes /tmp usable: only an entry's owner may rename it.
	if err := os.Chmod(parent, 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	if err := requirePrivateRoot(root); err != nil {
		t.Errorf("a world-writable sticky ancestor was rejected: %v", err)
	}
}

// The check is only worth having if it leaves the roots real users get. Rejecting
// those would be worse than the attack it prevents.
func TestRequireUnswappableAncestorsAcceptsTheRealCandidates(t *testing.T) {
	t.Setenv(CacheDirEnv, "")
	for _, c := range cacheCandidates() {
		if err := requireUnswappableAncestors(c.dir); err != nil {
			t.Errorf("the %s candidate %s would be refused: %v", c.origin, c.dir, err)
		}
	}
}

func TestCacheCandidatesAreAbsolute(t *testing.T) {
	// A relative TMPDIR would otherwise produce a relative library path, which a
	// binary built with the macOS hardened runtime refuses to load.
	t.Setenv(CacheDirEnv, "")
	t.Setenv("TMPDIR", "relative/tmp")
	for _, c := range cacheCandidates() {
		if !filepath.IsAbs(c.dir) {
			t.Errorf("%s candidate %q is not absolute", c.origin, c.dir)
		}
	}
}
