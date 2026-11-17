package staticbundle

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const arrowSnapshotVersion = "v0.0.0-static-ci"

func verifyArrow(ctx context.Context, repo, work string, b Bundle, env []string, proxy, arrowVersion string) error {
	module := RootModule + "/arrow"
	// Arrow is independently versioned and excluded from the root ZIP. Its
	// tests need an explicit import of the static entry to link payload syso
	// files; the build tag alone deliberately does not acquire native modules.
	generated := map[string][]byte{
		"zz_static_bundle_test.go": []byte(fmt.Sprintf("package chdbarrow\nimport _ %q\n", b.Prefix+"/static")),
	}
	if err := snapshotModule(filepath.Join(repo, "arrow"), work, module, arrowVersion, generated); err != nil {
		return err
	}
	if err := writeList(work, module, arrowVersion); err != nil {
		return err
	}
	consumer := filepath.Join(work, "arrow-consumer")
	if err := os.Mkdir(consumer, 0755); err != nil {
		return err
	}
	mod := fmt.Sprintf("module example.com/chdb-static-arrow-consumer\n\ngo 1.23.0\n\nrequire (\n%s %s\n%s %s\n%s/static %s\n)\n",
		RootModule, b.Bindings, module, arrowVersion, b.Prefix, b.Version)
	if err := os.WriteFile(filepath.Join(consumer, "go.mod"), []byte(mod), 0644); err != nil {
		return err
	}
	code, err := os.ReadFile(filepath.Join(repo, "arrow", "examples", "quickstart", "main.go"))
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(consumer, "main.go"), code, 0644); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(consumer, "static.go"), []byte(fmt.Sprintf("package main\nimport _ %q\n", b.Prefix+"/static")), 0644); err != nil {
		return err
	}
	// Never select a locally installed dynamic engine or inherit manual archive
	// flags. All binding/native dependencies must resolve through the staged
	// module graph. Only dependency acquisition is allowed to use the network.
	env = append(cleanEnv(env, "GOPROXY", "CHDB_ARROW_TEST_LIB", "CHDB_ARROW_TEST_STATIC", "CHDB_LIB_PATH", "CHDB_LIB_DIR"),
		"GOPROXY="+proxy, "CHDB_ARROW_TEST_STATIC=1")
	if err = run(ctx, consumer, env, "go", "mod", "tidy"); err != nil {
		return err
	}
	env = append(cleanEnv(env, "GOPROXY"), "GOPROXY=off")
	if err = run(ctx, consumer, env, "go", "build", "-trimpath", "-tags", "chdb_static", "-o", "arrow-consumer", "."); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, filepath.Join(consumer, "arrow-consumer"))
	cmd.Dir, cmd.Env = work, env
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("static Arrow quickstart: %w\n%s", err, output)
	}
	for _, want := range []string{"engine: " + strings.TrimPrefix(b.Engine, "v") + ", Arrow input: true, output: true\n", "sum: 12\n", "inserted: 4\t24\n", "typed table: 1 row(s)", "received "} {
		if !strings.Contains(string(output), want) {
			return fmt.Errorf("static Arrow quickstart missing %q: %s", want, output)
		}
	}
	fmt.Print(string(output))
	if err = scanBuildPaths(filepath.Join(consumer, "arrow-consumer"), repo, work); err != nil {
		return err
	}
	if err = run(ctx, consumer, env, "go", "vet", "-tags", "chdb_static", module); err != nil {
		return err
	}
	checked := append(cleanEnv(env, "GOEXPERIMENT", "GODEBUG"), "GOEXPERIMENT=cgocheck2", "GODEBUG=cgocheck=1")
	if err = run(ctx, consumer, checked, "go", "test", "-count=1", "-tags", "chdb_static", "-timeout=180s", module); err != nil {
		return err
	}
	if err = run(ctx, consumer, env, "go", "test", "-race", "-count=1", "-tags", "chdb_static", "-timeout=180s", module); err != nil {
		return err
	}
	if err = run(ctx, consumer, env, "go", "mod", "verify"); err != nil {
		return err
	}
	fmt.Println("packaged static Arrow quickstart, cgocheck2 and race tests passed (synthetic local source versions only)")
	return nil
}
