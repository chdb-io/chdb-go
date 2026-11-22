package staticbundle

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Snapshot packages the current source, including uncommitted implementation
// files, for local consumer tests. Nested modules, symlinks and native payloads
// are excluded. In CI the checkout is committed, so the same path tests exactly
// that commit. This is never a substitute for publishing the root Go module.
func Snapshot(repo, out, version string) error {
	if !bindingRE.MatchString(version) {
		return fmt.Errorf("invalid root module version")
	}
	return snapshotModule(repo, out, RootModule, version, nil)
}

// Generated files belong only to the isolated verification snapshot. They must
// never change the checkout or be reused as a public bindings release.
func snapshotModule(repo, out, module, version string, generated map[string][]byte) error {
	cmd := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	cmd.Dir = repo
	b, err := cmd.Output()
	if err != nil {
		return err
	}
	var nested []string
	names := strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
	for _, n := range names {
		if strings.HasSuffix(n, "/go.mod") {
			nested = append(nested, strings.TrimSuffix(n, "go.mod"))
		}
	}
	paths := map[string]string{}
	var mod []byte
	for _, n := range names {
		if n == "" {
			continue
		}
		skip := false
		for _, p := range nested {
			if strings.HasPrefix(n, p) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		if strings.HasSuffix(n, ".syso") || strings.HasSuffix(n, ".a") || strings.HasSuffix(n, ".so") || strings.HasSuffix(n, ".dylib") {
			return fmt.Errorf("native payload must not enter source module: %s", n)
		}
		p := filepath.Join(repo, filepath.FromSlash(n))
		st, e := os.Lstat(p)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		if !st.Mode().IsRegular() {
			continue
		}
		if n == "go.mod" {
			mod, e = os.ReadFile(p)
			if e != nil {
				return e
			}
		} else {
			paths[n] = p
		}
	}
	if !strings.HasPrefix(string(mod), "module "+module+"\n") {
		return fmt.Errorf("unexpected source module, want %s", module)
	}
	contents := map[string][]byte{"go.mod": mod}
	for name, data := range generated {
		if _, exists := paths[name]; exists || name == "go.mod" {
			return fmt.Errorf("generated verification file conflicts with source: %s", name)
		}
		contents[name] = data
	}
	_, err = writeModule(out, module, version, time.Unix(0, 0), contents, paths)
	return err
}

// Verify exercises an external application through an isolated, cold module
// cache, then rebuilds with the proxy disabled and all native-path flags gone.
// A separate purego consumer proves tidy/build do not acquire native modules.
func Verify(ctx context.Context, repo, bundle, work, arrowVersion string) error {
	if arrowVersion == "" {
		arrowVersion = arrowSnapshotVersion
	} else if !arrowVersionRE.MatchString(arrowVersion) {
		return fmt.Errorf("invalid Arrow snapshot version")
	}
	b, err := ReadBundle(bundle)
	if err != nil {
		return err
	}
	if err = newOutput(work, repo); err != nil {
		return err
	}
	hasEntry := false
	for _, f := range b.Files {
		if f.Path == "proxy/"+b.Prefix+"/static/@v/"+b.Version+".zip" {
			hasEntry = true
		}
		if err = copyFile(filepath.Join(bundle, filepath.FromSlash(f.Path)), filepath.Join(work, filepath.FromSlash(f.Path))); err != nil {
			return err
		}
	}
	if !hasEntry {
		return fmt.Errorf("verify requires an assembled static entry module")
	}
	for _, n := range b.Platforms {
		for _, m := range n.Modules {
			if err = writeList(work, m, b.Version); err != nil {
				return err
			}
		}
	}
	if err = writeList(work, b.Prefix+"/static", b.Version); err != nil {
		return err
	}
	if err = Snapshot(repo, work, b.Bindings); err != nil {
		return err
	}
	cacheCmd := exec.CommandContext(ctx, "go", "env", "GOMODCACHE")
	oldCache, _ := cacheCmd.Output()
	proxy := "file://" + filepath.ToSlash(filepath.Join(work, "proxy"))
	if c := strings.TrimSpace(string(oldCache)); c != "" {
		proxy += ",file://" + filepath.ToSlash(filepath.Join(c, "cache", "download"))
	}
	proxy += ",https://proxy.golang.org,direct"
	base := cleanEnv(os.Environ(), "GOWORK", "GOFLAGS", "GOPROXY", "GOSUMDB", "GOMODCACHE", "GOCACHE", "CGO_ENABLED", "CGO_CFLAGS", "CGO_CPPFLAGS", "CGO_CXXFLAGS", "CGO_LDFLAGS", "CGO_LDFLAGS_ALLOW", "CHDB_LIB_PATH", "CHDB_LIB_DIR", "GOOS", "GOARCH")
	base = append(base, "GOWORK=off", "GOFLAGS=-modcacherw", "GOSUMDB=off", "GOCACHE="+filepath.Join(work, "build-cache"))
	consumer := filepath.Join(work, "consumer")
	if err = os.Mkdir(consumer, 0755); err != nil {
		return err
	}
	source := fmt.Sprintf(`package main
import (
 "errors"
 "fmt"
 "log"
 "os"
 _ %q
 "github.com/chdb-io/chdb-go/v2/chdb"
 p "github.com/chdb-io/chdb-go/v2/chdb-purego"
)
func main() {
 if _, err := p.LoadedLibraryPath(); !errors.Is(err, p.ErrStaticallyLinked) { log.Fatalf("not static: %%v", err) }
 version, err := p.Version(); if err != nil { log.Fatal(err) }
 query := "SELECT sum(number), uniqExact(number) FROM numbers(1000)"
 if len(os.Args)>1 { query=os.Args[1] }
 r, err := chdb.Query(query,"CSV"); if err != nil { log.Fatal(err) }; defer r.Free()
 fmt.Printf("%%s:%%s",version,r.String())
}
`, b.Prefix+"/static")
	if err = os.WriteFile(filepath.Join(consumer, "main.go"), []byte(source), 0644); err != nil {
		return err
	}
	mod := fmt.Sprintf("module example.com/chdb-static-consumer\n\ngo 1.21\n\nrequire %s/static %s\n", b.Prefix, b.Version)
	if err = os.WriteFile(filepath.Join(consumer, "go.mod"), []byte(mod), 0644); err != nil {
		return err
	}
	env := append(append([]string{}, base...), "CGO_ENABLED=1", "GOMODCACHE="+filepath.Join(work, "mod-cache"), "GOPROXY="+proxy)
	if err = run(ctx, consumer, env, "go", "mod", "tidy"); err != nil {
		return err
	}
	env = append(cleanEnv(env, "GOPROXY"), "GOPROXY=off")
	if err = run(ctx, consumer, env, "go", "build", "-trimpath", "-tags", "chdb_static", "-o", "consumer", "."); err != nil {
		return err
	}
	if err = run(ctx, consumer, env, "go", "mod", "verify"); err != nil {
		return err
	}
	if err = scanBuildPaths(filepath.Join(consumer, "consumer"), repo, work, bundle); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, filepath.Join(consumer, "consumer"))
	cmd.Env = env
	cmd.Dir = work
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("consumer: %w\n%s", err, output)
	}
	want := strings.TrimPrefix(b.Engine, "v") + ":499500,1000\n"
	if string(output) != want {
		return fmt.Errorf("consumer returned %q, want %q", output, want)
	}
	fmt.Print(string(output))
	if runtime.GOOS == "darwin" {
		cmd = exec.CommandContext(ctx, "otool", "-L", filepath.Join(consumer, "consumer"))
	} else {
		cmd = exec.CommandContext(ctx, "readelf", "-d", filepath.Join(consumer, "consumer"))
	}
	output, err = cmd.CombinedOutput()
	if err != nil {
		return err
	}
	if strings.Contains(string(output), "libchdb.so") || strings.Contains(string(output), "libchdb.dylib") {
		return fmt.Errorf("dynamic libchdb dependency: %s", output)
	}
	if err = os.WriteFile(filepath.Join(work, "dependencies.txt"), output, 0644); err != nil {
		return err
	}
	if err = run(ctx, consumer, env, "go", "mod", "vendor"); err != nil {
		return err
	}
	if err = run(ctx, consumer, env, "go", "build", "-mod=vendor", "-trimpath", "-tags", "chdb_static", "-o", "consumer-vendor", "."); err != nil {
		return err
	}
	if err = scanBuildPaths(filepath.Join(consumer, "consumer-vendor"), repo, work, bundle); err != nil {
		return err
	}
	cmd = exec.CommandContext(ctx, filepath.Join(consumer, "consumer-vendor"))
	cmd.Dir = work
	cmd.Env = env
	output, err = cmd.CombinedOutput()
	if err != nil || string(output) != want {
		return fmt.Errorf("vendored consumer: %v, output %q", err, output)
	}
	if err = verifyArrow(ctx, repo, work, b, env, proxy, arrowVersion); err != nil {
		return err
	}
	fmt.Println("static and vendored consumers passed; checking purego isolation")
	pure := filepath.Join(work, "purego")
	if err = os.Mkdir(pure, 0755); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(pure, "go.mod"), []byte("module example.com/chdb-purego-consumer\n\ngo 1.21\n\nrequire "+RootModule+" "+b.Bindings+"\n"), 0644); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(pure, "main.go"), []byte("package main\nimport _ \""+RootModule+"/chdb\"\nfunc main() {}\n"), 0644); err != nil {
		return err
	}
	pureEnv := append(append([]string{}, base...), "CGO_ENABLED=0", "GOMODCACHE="+filepath.Join(work, "purego-cache"), "GOPROXY="+proxy)
	if err = run(ctx, pure, pureEnv, "go", "mod", "tidy"); err != nil {
		return err
	}
	pureEnv = append(cleanEnv(pureEnv, "GOPROXY"), "GOPROXY=off")
	if err = run(ctx, pure, pureEnv, "go", "build", "-o", "consumer", "."); err != nil {
		return err
	}
	if _, err = os.Stat(filepath.Join(work, "purego-cache", "cache", "download", filepath.FromSlash(b.Prefix))); !os.IsNotExist(err) {
		return fmt.Errorf("purego consumer accessed native modules: %v", err)
	}
	fmt.Println("purego tidy/build acquired no native modules")
	return nil
}
func run(ctx context.Context, dir string, env []string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %v: %w", name, args, err)
	}
	return nil
}
func cleanEnv(env []string, keys ...string) []string {
	var result []string
	for _, v := range env {
		k := strings.SplitN(v, "=", 2)[0]
		drop := false
		for _, s := range keys {
			if k == s {
				drop = true
				break
			}
		}
		if !drop {
			result = append(result, v)
		}
	}
	return result
}

func scanBuildPaths(binary string, paths ...string) error {
	f, err := os.Open(binary)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, 64<<10)
	var carry []byte
	for {
		n, readErr := f.Read(buf)
		chunk := append(carry, buf[:n]...)
		for _, p := range paths {
			if p != "" && bytes.Contains(chunk, []byte(p)) {
				return fmt.Errorf("binary contains build path %s", p)
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
		keep := 0
		for _, p := range paths {
			if len(p) > keep {
				keep = len(p)
			}
		}
		if keep > len(chunk) {
			keep = len(chunk)
		}
		carry = append([]byte(nil), chunk[len(chunk)-keep:]...)
	}
}
