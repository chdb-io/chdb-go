package chdbpurego

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeEngine builds a small EmbeddedEngine so the extraction machinery can be
// tested without moving the several hundred megabytes a real engine weighs.
func fakeEngine(payload []byte) (*EmbeddedEngine, *int32) {
	return fakeEngineVersion("v0.0.0-test", payload)
}

func fakeEngineVersion(version string, payload []byte) (*EmbeddedEngine, *int32) {
	sum := sha256.Sum256(payload)
	var opens int32
	var mu sync.Mutex
	return &EmbeddedEngine{
		Version:  version,
		FileName: "libchdb.so",
		Digest:   hex.EncodeToString(sum[:])[:32],
		Size:     int64(len(payload)),
		Open: func() (io.ReadCloser, error) {
			mu.Lock()
			opens++
			mu.Unlock()
			return io.NopCloser(bytes.NewReader(payload)), nil
		},
	}, &opens
}

func okLoad(string) (uintptr, error) { return 1, nil }

func TestExtractWritesEngineUnderDigestDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv(CacheDirEnv, root)
	payload := []byte("pretend this is a shared library")
	e, _ := fakeEngine(payload)

	_, path, err := openEmbedded(e, okLoad)
	if err != nil {
		t.Fatal(err)
	}

	// The digest names the directory. That is what lets two builds carrying the
	// same engine share one extracted copy, and what makes a race harmless.
	if filepath.Base(filepath.Dir(path)) != e.Digest {
		t.Errorf("parent directory %q is not the digest %q", filepath.Base(filepath.Dir(path)), e.Digest)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Error("extracted content does not match the payload")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("extracted library is not executable: %v", info.Mode())
	}
}

func TestSecondStartSkipsExtraction(t *testing.T) {
	root := t.TempDir()
	t.Setenv(CacheDirEnv, root)
	e, opens := fakeEngine([]byte("payload"))

	for i := 0; i < 3; i++ {
		if _, _, err := openEmbedded(e, okLoad); err != nil {
			t.Fatal(err)
		}
	}
	// Every start after the first must be a stat, not a rewrite; otherwise a
	// few hundred megabytes are copied on every process launch.
	if *opens != 1 {
		t.Errorf("payload opened %d times, want 1", *opens)
	}
}

func TestConcurrentColdStartsWriteThePayloadOnce(t *testing.T) {
	root := t.TempDir()
	t.Setenv(CacheDirEnv, root)
	payload := bytes.Repeat([]byte("chdb"), 4096)
	e, opens := fakeEngine(payload)

	const racers = 12
	paths := make([]string, racers)
	errs := make([]error, racers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, paths[i], errs[i] = openEmbedded(e, okLoad)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("racer %d: %v", i, err)
		}
		if paths[i] != paths[0] {
			t.Fatalf("racer %d resolved %q, racer 0 resolved %q", i, paths[i], paths[0])
		}
	}

	got, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Error("the published copy is not the full payload")
	}

	// The whole point of taking a lock: one process writes the engine and the
	// rest use what it published. Without this, twelve cold starts write twelve
	// copies of a ~500 MiB library to publish one, which is how they ran a
	// machine out of disk.
	if *opens != 1 {
		t.Errorf("payload written %d times by %d concurrent cold starts, want 1", *opens, racers)
	}

	// A racer that loses must clean up after itself, or a machine restarting
	// many processes accumulates abandoned directories of engine-sized junk.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	published := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			t.Errorf("leftover temporary directory %q", entry.Name())
		}
		if entry.IsDir() {
			published++
		}
	}
	if published != 1 {
		t.Errorf("expected exactly one published directory, got %d", published)
	}
}

func TestUnavailableLockIsReportedAsSuchNotAsAnError(t *testing.T) {
	// Where the lock file cannot even be created — a filesystem without flock
	// support behaves the same way from the caller's side — locking has to
	// report that it is unavailable so extraction carries on without it.
	// Returning an error instead would turn an optimisation into a requirement.
	if unlock := lockExtraction(filepath.Join(t.TempDir(), "does-not-exist"), "deadbeef"); unlock != nil {
		unlock()
		t.Fatal("locking succeeded in a directory that does not exist")
	}
}

func TestLockIsExclusiveAndReleased(t *testing.T) {
	root := t.TempDir()

	unlock := lockExtraction(root, "deadbeef")
	if unlock == nil {
		t.Fatal("could not take the lock on a fresh directory")
	}

	// A second attempt has to give up rather than proceed, or the lock buys
	// nothing. Waiting out lockWait here would take minutes, so the contended
	// path is checked with a deadline of its own.
	contended := make(chan func(), 1)
	go func() { contended <- lockExtraction(root, "deadbeef") }()
	select {
	case got := <-contended:
		if got != nil {
			got()
			t.Fatal("two processes held the same extraction lock at once")
		}
		t.Fatal("the contended waiter gave up instead of waiting for the holder")
	case <-time.After(200 * time.Millisecond):
		// Still waiting, which is what it should be doing.
	}

	unlock()
	select {
	case got := <-contended:
		if got == nil {
			t.Fatal("the lock was not available after the holder released it")
		}
		got()
	case <-time.After(lockWait):
		t.Fatal("the waiter did not acquire the lock after it was released")
	}
}

func TestExplicitCacheDirFailureDoesNotFallThrough(t *testing.T) {
	// A regular file makes MkdirAll fail regardless of the user's privileges,
	// unlike a read-only directory which root can still write into.
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(CacheDirEnv, filepath.Join(blocker, "cache"))
	e, opens := fakeEngine([]byte("payload"))

	_, _, err := openEmbedded(e, okLoad)
	if err == nil {
		t.Fatal("extraction succeeded despite an unusable explicit cache dir")
	}
	if !strings.Contains(err.Error(), CacheDirEnv) {
		t.Errorf("error does not name %s: %v", CacheDirEnv, err)
	}
	// Honouring the variable means not quietly extracting somewhere else, so
	// the error reports one location rather than a list of attempts.
	if strings.Contains(err.Error(), "tried:") {
		t.Errorf("fell through to other candidates despite an explicit setting: %v", err)
	}
	if *opens != 0 {
		t.Errorf("payload was read %d times for a directory that cannot hold it", *opens)
	}
}

func TestUnloadableDirectoryMovesToNextCandidate(t *testing.T) {
	// Whether a directory permits mapping executable pages cannot be checked up
	// front, so a load failure has to demote the candidate. Simulated here by
	// refusing to load anything under the first candidate.
	first := t.TempDir()
	second := t.TempDir()
	redirectCacheDirs(t, first, second)

	e, _ := fakeEngine([]byte("payload"))
	load := func(path string) (uintptr, error) {
		if strings.HasPrefix(path, first) {
			return 0, errors.New("failed to map segment from shared object")
		}
		return 1, nil
	}

	_, path, err := openEmbedded(e, load)
	if err != nil {
		t.Fatalf("did not fall back to the second candidate: %v", err)
	}
	if !strings.HasPrefix(path, second) {
		t.Errorf("loaded from %q, expected something under %q", path, second)
	}
}

func TestAllCandidatesUnloadableReportsEachAndAFix(t *testing.T) {
	redirectCacheDirs(t, t.TempDir(), t.TempDir())

	e, _ := fakeEngine([]byte("payload"))
	load := func(string) (uintptr, error) { return 0, errors.New("failed to map segment from shared object") }

	_, _, err := openEmbedded(e, load)
	if err == nil {
		t.Fatal("expected an error when nothing can load")
	}
	msg := err.Error()
	for _, want := range []string{"could not be loaded", CacheDirEnv, LibPathEnv, e.Version} {
		if !strings.Contains(msg, want) {
			t.Errorf("error is missing %q:\n%s", want, msg)
		}
	}
}

func TestRegisteringTwoEnginesPanics(t *testing.T) {
	// Two platform payloads linked into one binary means the build is wrong.
	// Picking one silently would make the engine version unpredictable.
	saved := registeredEngine()
	t.Cleanup(func() {
		embeddedMu.Lock()
		embeddedEngine = saved
		embeddedMu.Unlock()
	})

	embeddedMu.Lock()
	embeddedEngine = nil
	embeddedMu.Unlock()

	one, _ := fakeEngineVersion("v0.0.0-first", []byte("a"))
	two, _ := fakeEngineVersion("v0.0.0-second", []byte("b"))
	RegisterEmbeddedEngine(*one)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("registering a second engine did not panic")
		}
		if !strings.Contains(fmt.Sprint(r), two.Version) {
			t.Errorf("panic does not mention the conflicting engine: %v", r)
		}
	}()
	RegisterEmbeddedEngine(*two)
}

// redirectCacheDirs points both default cache candidates at temporary
// directories. os.UserCacheDir consults $HOME on darwin and $XDG_CACHE_HOME
// then $HOME on linux, so setting only one of them lets a test write into the
// developer's real cache directory.
func redirectCacheDirs(t *testing.T, userCache, temp string) {
	t.Helper()
	t.Setenv(CacheDirEnv, "")
	t.Setenv("HOME", userCache)
	t.Setenv("XDG_CACHE_HOME", userCache)
	t.Setenv("TMPDIR", temp)
}
