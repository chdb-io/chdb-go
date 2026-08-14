package chdbpurego

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// CacheDirEnv names the environment variable that chooses where the embedded
// engine is extracted to.
const CacheDirEnv = "CHDB_CACHE_DIR"

// cacheCandidate is one directory the engine could be extracted into.
type cacheCandidate struct {
	origin   string
	dir      string
	explicit bool // set by the user, so failing here must not fall through
}

// cacheCandidates lists the roots to try, in order.
//
// An explicit CHDB_CACHE_DIR is honoured exactly: extracting somewhere else
// after the user named a directory hides a misconfiguration that is much easier
// to fix when reported.
func cacheCandidates() []cacheCandidate {
	if dir := os.Getenv(CacheDirEnv); dir != "" {
		if abs, err := filepath.Abs(dir); err == nil {
			return []cacheCandidate{{origin: CacheDirEnv, dir: abs, explicit: true}}
		}
		return []cacheCandidate{{origin: CacheDirEnv, dir: dir, explicit: true}}
	}

	var out []cacheCandidate
	// UserCacheDir fails when HOME is unset, which is normal inside minimal
	// containers, so this is a candidate rather than a requirement.
	if dir, err := os.UserCacheDir(); err == nil {
		out = append(out, cacheCandidate{origin: "user cache dir", dir: filepath.Join(dir, "chdb-go")})
	}
	// Absolute for the same reason CHDB_CACHE_DIR is: the extracted library's
	// path goes to the dynamic loader, and a program built with the macOS
	// hardened runtime rejects a relative library path outright. TMPDIR is a
	// user-supplied string and can be relative.
	if dir, err := filepath.Abs(filepath.Join(os.TempDir(), "chdb-go")); err == nil {
		out = append(out, cacheCandidate{origin: "temp dir", dir: dir})
	}
	return out
}

// requirePrivateRoot rejects a cache root that someone other than this user could
// have put a file into.
//
// The extraction is content-addressed, and the fast path on every start after the
// first is a single stat of <root>/<digest>/<name> followed by handing that path
// to dlopen. Neither the digest nor the layout is secret — the digest ships in the
// module — so on a shared root such as /tmp, another user can create that exact
// path first and have this process load their library instead. Nothing later in
// the extraction notices: there is nothing to overwrite, so the file that is
// already there is the one that gets used.
//
// Verifying the payload's SHA-256 on every start would also close it, and would
// cost a few hundred milliseconds of hashing per process for a threat that only
// exists when the directory is writable by someone else. Requiring the directory
// to be private instead costs one stat and rules the case out rather than
// detecting it: if only this user can write there, only this user's bytes can be
// found there.
//
// A root that already exists and is owned by this user is accepted even if it is
// group- or world-readable, because 0755 is what a previous version created and
// readable is not writable.
func requirePrivateRoot(root string) error {
	fi, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", root)
	}
	if perm := fi.Mode().Perm(); perm&0o022 != 0 {
		return fmt.Errorf("%s is writable by other users (mode %#o), so a library placed "+
			"there could not be trusted to be the one this build carries", root, perm)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if uid := os.Getuid(); uint64(st.Uid) != uint64(uid) {
		return fmt.Errorf("%s belongs to uid %d rather than to this user (uid %d), so a "+
			"library placed there could not be trusted to be the one this build carries",
			root, st.Uid, uid)
	}
	return nil
}

// openEmbedded extracts the embedded engine and loads it, returning the library
// handle and the path it was loaded from.
//
// The destination directory is named after the payload's digest, and it is
// published by renaming a fully written temporary directory into place. Those
// two choices together remove the need for locking between processes: every
// process computes the same destination and produces byte-identical content,
// the rename is atomic so no one can observe a half-written directory, and a
// process that loses the race just uses what the winner published. Several
// processes starting at once may each extract once; correctness does not depend
// on preventing that.
//
// Loading is attempted per candidate rather than once at the end, because
// whether a directory permits mapping executable pages cannot be determined in
// advance. access(X_OK) is unreliable in both directions — on macOS it reports
// a noexec mount as non-executable even though dlopen from it succeeds, and on
// Linux it does not reflect the mount flag that does block the mapping. Trying
// the real load is the only dependable check, so a failure here moves to the
// next candidate.
func openEmbedded(e *EmbeddedEngine, load func(string) (uintptr, error)) (uintptr, string, error) {
	var problems []string

	for _, c := range cacheCandidates() {
		path, err := extractInto(c.dir, e)
		if err == nil {
			var handle uintptr
			handle, err = load(path)
			if err == nil {
				return handle, path, nil
			}
			err = fmt.Errorf("extracted but could not be loaded: %s", trimDlopenNoise(err.Error()))
		}
		if c.explicit {
			return 0, "", fmt.Errorf("chdb: %s=%q cannot serve the embedded engine (%s, needs %s): %w",
				CacheDirEnv, c.dir, e.Version, humanSize(e.Size), err)
		}
		problems = append(problems, fmt.Sprintf("  %-16s %-44s %v", c.origin, c.dir, err))
	}

	return 0, "", fmt.Errorf(
		"chdb: cannot use the embedded engine (%s, needs %s)\n\ntried:\n%s\n\nfix one of:\n"+
			"  %s=<writable dir>   must also allow mapping executable pages\n"+
			"  %s=<libchdb path>   use a copy you manage, skipping extraction",
		e.Version, humanSize(e.Size), strings.Join(problems, "\n"), CacheDirEnv, LibPathEnv)
}

// extractInto performs the extraction under one root.
func extractInto(root string, e *EmbeddedEngine) (string, error) {
	// 0o700 rather than 0o755: nothing outside this process needs to read the
	// cache, and a root only this user can write to is what makes the stat below
	// enough on its own. The check runs after the create, not instead of it,
	// because a root this process did not create is exactly the case that
	// matters — MkdirAll succeeds on a directory that already exists whoever
	// owns it.
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	if err := requirePrivateRoot(root); err != nil {
		return "", err
	}

	final := filepath.Join(root, e.Digest)
	lib := filepath.Join(final, e.FileName)

	// Fast path. Reached by every start after the first, so it must stay a
	// single stat with no locking.
	if _, err := os.Stat(lib); err == nil {
		return lib, nil
	}

	// Correctness does not need the extraction to be serialised — the digest
	// names the destination, every process writes identical bytes and the
	// rename is atomic — but space does. Without this, twelve processes
	// starting cold at once each write their own ~500 MiB temporary copy before
	// eleven of them are discarded: six gigabytes of writes to publish half a
	// gigabyte. Measured on a machine with 4.7 GiB free, all twelve failed with
	// ENOSPC while one would have succeeded, and a container with a small
	// writable layer hits the same wall with far fewer processes.
	//
	// The lock is an optimisation and is treated as one. If it cannot be taken,
	// because the filesystem does not support it or because the holder is
	// wedged, extraction proceeds unsynchronised — exactly what would happen
	// without any of this.
	if unlock := lockExtraction(root, e.Digest); unlock != nil {
		defer unlock()
		// The holder may have published while we waited for it.
		if _, err := os.Stat(lib); err == nil {
			return lib, nil
		}
	}

	// The temporary directory has to sit under the same root: os.Rename across
	// filesystems fails with EXDEV rather than falling back to a copy, and a
	// copy would not be atomic anyway.
	tmp, err := os.MkdirTemp(root, ".tmp-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)

	if err := writeLibrary(filepath.Join(tmp, e.FileName), e); err != nil {
		return "", err
	}

	if err := os.Rename(tmp, final); err != nil {
		// Losing the race is the expected outcome for every process but one.
		if _, statErr := os.Stat(lib); statErr == nil {
			return lib, nil
		}
		return "", err
	}
	return lib, nil
}

// writeLibrary streams the payload to disk and makes sure it is durable before
// the caller publishes the directory.
//
// Syncing matters more than it looks. Without it a power loss can leave a
// directory whose name claims the content is complete while the bytes are not,
// and because the fast path only stats the file, that state never repairs
// itself — every later start loads a truncated library.
func writeLibrary(path string, e *EmbeddedEngine) error {
	src, err := e.Open()
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return err
	}

	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	if err := dst.Sync(); err != nil {
		dst.Close()
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	// Directory sync is not supported on every filesystem; the rename is still
	// atomic, so a refusal here is not fatal. Anything else is: the caller is
	// about to report the extraction as durable.
	if err := d.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return err
	}
	return nil
}

func humanSize(n int64) string {
	switch {
	case n <= 0:
		return "unknown size"
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.0f KiB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.0f MiB", float64(n)/(1024*1024))
	}
}
