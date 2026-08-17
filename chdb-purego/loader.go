package chdbpurego

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ebitengine/purego"
)

// LibPathEnv names the environment variable that points directly at a libchdb
// file. When it is set, it is the only location considered.
const LibPathEnv = "CHDB_LIB_PATH"

// libFileNames lists the file names libchdb is published under, in the order
// they should be tried.
//
// The macOS release archive names the file libchdb.so even though it is a
// Mach-O dynamic library, so on darwin both spellings have to be tried; a
// Homebrew install uses libchdb.dylib.
func libFileNames() []string {
	if runtime.GOOS == "darwin" {
		return []string{"libchdb.so", "libchdb.dylib"}
	}
	return []string{"libchdb.so"}
}

// attempt records one location that was considered and what came of it.
type attempt struct {
	origin string // where the path came from, for the error message
	path   string
	err    error // nil once a candidate loaded successfully

	// byLoader means path is a bare library name to hand to the dynamic loader
	// rather than a file to check for first. Its search — ld.so.cache, the
	// distribution's architecture directories, DT_RUNPATH — cannot be
	// enumerated from here, so it is delegated instead of reimplemented.
	byLoader bool
}

// notFoundError reports every location that was tried, so a failed load can be
// diagnosed without rebuilding with extra logging.
type notFoundError struct {
	attempts []attempt
}

func (e *notFoundError) Error() string {
	var b strings.Builder
	b.WriteString("chdb: libchdb not found\n\ntried:\n")
	for _, a := range e.attempts {
		reason := "ok"
		if a.err != nil {
			reason = trimDlopenNoise(a.err.Error())
		}
		fmt.Fprintf(&b, "  %-22s %-44s %s\n", a.origin, a.path, reason)
	}
	b.WriteString("\nfix one of:\n")
	fmt.Fprintf(&b, "  install the library system-wide:  curl -sL https://lib.chdb.io | bash\n")
	fmt.Fprintf(&b, "  place libchdb next to the executable\n")
	fmt.Fprintf(&b, "  point %s at an existing copy\n", LibPathEnv)
	return b.String()
}

// trimDlopenNoise shortens dyld's multi-path "tried:" list down to the first
// concrete reason. Without this the error text repeats every probe path the
// dynamic loader walked, which buries the useful part.
func trimDlopenNoise(msg string) string {
	if i := strings.Index(msg, "): tried: "); i >= 0 {
		msg = msg[i+len("): tried: "):]
	}
	if i := strings.Index(msg, "), "); i >= 0 {
		msg = msg[:i+1]
	}
	msg = strings.TrimSpace(strings.ReplaceAll(msg, "\n", " "))
	if len(msg) > 120 {
		msg = msg[:120] + "..."
	}
	return msg
}

// searchPaths returns the candidate locations, in priority order, for the case
// where CHDB_LIB_PATH is not set.
//
// The executable's own directory comes before any system location so that a
// release archive containing both the program and libchdb works without
// installing anything, and so that a program shipping a known-good library is
// never silently overridden by an unrelated copy in /usr/local/lib.
func searchPaths() []attempt {
	var out []attempt
	names := libFileNames()

	if exe, err := os.Executable(); err == nil {
		if exe, err = filepath.EvalSymlinks(exe); err == nil {
			dir := filepath.Dir(exe)
			for _, n := range names {
				out = append(out, attempt{origin: "next to executable", path: filepath.Join(dir, n)})
			}
		}
	}

	// Kept for compatibility with installs that put libchdb on PATH. Note this
	// finds the file only if it carries the executable bit, which is why
	// update_libchdb.sh chmod +x's a shared library.
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			if abs, err := filepath.Abs(p); err == nil {
				out = append(out, attempt{origin: "PATH", path: abs})
			}
		}
	}

	for _, dir := range []string{"/usr/local/lib", "/opt/homebrew/lib", "/usr/lib"} {
		for _, n := range names {
			out = append(out, attempt{origin: "system path", path: filepath.Join(dir, n)})
		}
	}

	// The library-path environment variables are named explicitly so that a
	// directory a user pointed at appears in the diagnostic as itself, rather
	// than disappearing into "the loader looked and did not find it".
	for _, env := range []string{"LD_LIBRARY_PATH", "DYLD_LIBRARY_PATH", "DYLD_FALLBACK_LIBRARY_PATH"} {
		for _, dir := range filepath.SplitList(os.Getenv(env)) {
			if dir == "" {
				continue
			}
			abs, err := filepath.Abs(dir)
			if err != nil {
				continue
			}
			for _, n := range names {
				out = append(out, attempt{origin: env, path: filepath.Join(abs, n)})
			}
		}
	}

	// Last, and by name rather than by path: whatever the dynamic loader itself
	// can find. Before this package resolved paths of its own, a bare name was
	// all it ever passed to dlopen, so every install the loader knows about —
	// registered with ldconfig, in /usr/lib/<triple> on a multiarch
	// distribution, reached through a RUNPATH — worked. Enumerating those here
	// would mean reimplementing the loader's search and getting it wrong on some
	// distribution, so the name is handed over instead. It comes last because
	// every location this package can name is preferable: a caller can be told
	// which file it got.
	for _, n := range names {
		out = append(out, attempt{origin: "dynamic loader", path: n, byLoader: true})
	}
	return out
}

// dlopenLibrary is the single place the loader flags are chosen. RTLD_GLOBAL is
// required: the engine's own internal references resolve against the global
// namespace once it is loaded.
func dlopenLibrary(path string) (uintptr, error) {
	return purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
}

// openLibrary loads libchdb and returns its handle and the absolute path it was
// loaded from.
//
// Three rules shape this function. Every path handed to the dynamic loader is
// absolute, because a program built with the macOS hardened runtime rejects
// relative library paths outright and reports it as a path error rather than a
// permissions one — the one thing passed by name instead of by path is the final
// candidate, which exists to delegate to the loader's own search and is a name
// rather than a relative path. A location that was named explicitly is never silently
// replaced by a different one: if CHDB_LIB_PATH is set and does not load, that
// is the error, because loading some other build of the engine instead is far
// harder to diagnose than failing. And a binary carrying its own engine uses
// that engine, so the version is fixed by the build rather than by whatever the
// host happens to have installed.
func openLibrary() (uintptr, string, error) {
	if raw := os.Getenv(LibPathEnv); raw != "" {
		abs, err := filepath.Abs(raw)
		if err != nil {
			return 0, "", fmt.Errorf("chdb: %s=%q cannot be resolved to an absolute path: %w", LibPathEnv, raw, err)
		}
		h, err := dlopenLibrary(abs)
		if err != nil {
			return 0, "", fmt.Errorf(
				"chdb: %s=%q could not be loaded: %s\n\n%s is an explicit override, so no other location was tried. Unset it to use the default search",
				LibPathEnv, abs, trimDlopenNoise(err.Error()), LibPathEnv)
		}
		return h, abs, nil
	}

	// Past the explicit override above, a build carrying its own engine does not
	// consult the machine at all. That is the point of embedding it: the version
	// is decided when the binary is built, and quietly preferring some other copy
	// found on the host would undo that guarantee in a way nobody would notice
	// until the behaviour differed.
	if e := registeredEngine(); e != nil {
		return openEmbedded(e, dlopenLibrary)
	}

	candidates := searchPaths()
	for i := range candidates {
		c := &candidates[i]
		// A name for the loader has no file to check: the point of it is that
		// where the file lives is the loader's business.
		if !c.byLoader {
			if _, err := os.Stat(c.path); err != nil {
				c.err = fmt.Errorf("no such file")
				continue
			}
		}
		h, err := dlopenLibrary(c.path)
		if err != nil {
			c.err = err
			continue
		}
		return h, c.path, nil
	}
	return 0, "", &notFoundError{attempts: candidates}
}
