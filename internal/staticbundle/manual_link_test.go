package staticbundle

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func linuxStaticLinkFlags(t *testing.T, manual bool) []string {
	t.Helper()
	tags := "chdb_static"
	if manual {
		tags += ",chdb_static_manual"
	}
	cmd := exec.Command("go", "list", "-json", "-tags", tags, "./chdb-purego")
	cmd.Dir = "../.."
	cmd.Env = append(cleanEnv(os.Environ(), "GOWORK", "GOFLAGS", "GOOS", "GOARCH", "CGO_ENABLED"), "GOWORK=off", "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("read Linux cgo flags: %v\n%s\n%s", err, output, stderr.Bytes())
	}
	var pkg struct{ CgoLDFLAGS []string }
	if err := json.Unmarshal(output, &pkg); err != nil {
		t.Fatalf("decode cgo flags: %v\n%s", err, output)
	}
	return pkg.CgoLDFLAGS
}

func TestLinuxStaticRuntimeLibrariesFollowManualArchive(t *testing.T) {
	for _, manual := range []bool{false, true} {
		flags := linuxStaticLinkFlags(t, manual)
		end := -1
		archives := 0
		for i, flag := range flags {
			if flag == "-l:libchdb.a" {
				archives++
			}
			if flag == "-Wl,--no-whole-archive" {
				end = i
			}
		}
		if manual && (archives != 1 || end == -1) || !manual && archives != 0 {
			t.Fatalf("manual=%v: unexpected archive selection: %v", manual, flags)
		}
		for _, lib := range []string{"-lc", "-lm", "-lrt", "-lpthread", "-ldl"} {
			count := 0
			for i, flag := range flags {
				if flag == lib {
					count++
					if manual && i <= end {
						t.Fatalf("%s precedes the manual archive: %v", lib, flags)
					}
				}
			}
			if count != 1 {
				t.Fatalf("manual=%v: want exactly one %s, got %v", manual, lib, flags)
			}
		}
	}
}

func TestLinuxManualArchiveLinksMathWithAsNeeded(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("GNU-compatible Linux system linker required")
	}
	for _, tool := range []string{"cc", "ar"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " required")
		}
	}
	d := t.TempDir()
	put(t, filepath.Join(d, "engine.c"), []byte("#include <math.h>\ndouble chdb_link_order_probe(double x) { return hypot(x, 4.0); }\n"))
	put(t, filepath.Join(d, "main.c"), []byte("extern double chdb_link_order_probe(double);\nint main(void) { return chdb_link_order_probe(3.0) == 5.0 ? 0 : 1; }\n"))
	runTool := func(name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = d
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, output)
		}
	}
	runTool("cc", "-O0", "-fno-builtin", "-c", "engine.c", "-o", "engine.o")
	runTool("ar", "rcs", "libchdb.a", "engine.o")
	runTool("cc", "-c", "main.c", "-o", "main.o")
	// Consume the adapter's real cgo flags, not a separately maintained list.
	// --as-needed reproduces the ordering-sensitive Ubuntu CI link policy.
	args := append([]string{"main.o", "-Wl,--as-needed", "-L" + d}, linuxStaticLinkFlags(t, true)...)
	args = append(args, "-o", "consumer")
	runTool("cc", args...)
	runTool(filepath.Join(d, "consumer"))
}
