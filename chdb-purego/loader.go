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
	return out
}

// openLibrary loads libchdb and returns its handle and the absolute path it was
// loaded from.
//
// Two rules shape this function. Every path handed to the dynamic loader is
// absolute, because a program built with the macOS hardened runtime rejects
// relative library paths outright and reports it as a path error rather than a
// permissions one. And a location that was named explicitly is never silently
// replaced by a different one: if CHDB_LIB_PATH is set and does not load, that
// is the error, because loading some other build of the engine instead is far
// harder to diagnose than failing.
func openLibrary() (uintptr, string, error) {
	if raw := os.Getenv(LibPathEnv); raw != "" {
		abs, err := filepath.Abs(raw)
		if err != nil {
			return 0, "", fmt.Errorf("chdb: %s=%q cannot be resolved to an absolute path: %w", LibPathEnv, raw, err)
		}
		h, err := purego.Dlopen(abs, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			return 0, "", fmt.Errorf(
				"chdb: %s=%q could not be loaded: %s\n\n%s is an explicit override, so no other location was tried. Unset it to use the default search",
				LibPathEnv, abs, trimDlopenNoise(err.Error()), LibPathEnv)
		}
		return h, abs, nil
	}

	candidates := searchPaths()
	for i := range candidates {
		c := &candidates[i]
		if _, err := os.Stat(c.path); err != nil {
			c.err = fmt.Errorf("no such file")
			continue
		}
		h, err := purego.Dlopen(c.path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			c.err = err
			continue
		}
		return h, c.path, nil
	}
	return 0, "", &notFoundError{attempts: candidates}
}
