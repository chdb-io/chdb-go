package staticbundle

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testEndpoint = "https://00000000000000000000000000000000.r2.cloudflarestorage.com"

func completeFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	b := Bundle{Schema: 1, Prefix: "go.example.com/chdb", Engine: "v26.11.1", Version: "v0.261101.1", Bindings: "v2.99.0-test", Time: time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC), HeaderSHA256: strings.Repeat("a", 64), Complete: true}
	notices := fixtureNotices()
	b.Notices = noticeInventory(notices)
	sourceFiles := func(module, source string) map[string][]byte {
		files := map[string][]byte{"go.mod": []byte("module " + module + "\n"), "LICENSE": []byte("test license\n"), "native.go": []byte(source)}
		for name, data := range notices {
			files[name] = data
		}
		return files
	}
	for _, p := range Platforms {
		m := b.Prefix + "/libchdb/" + p + "/part0"
		b.Platforms = append(b.Platforms, Native{Platform: p, Modules: []string{m}, Objects: 1, Shards: 1, SourceSHA256: strings.Repeat("b", 64)})
		files, err := writeModule(dir, m, b.Version, b.Time, sourceFiles(m, "package native\n"), nil)
		if err != nil {
			t.Fatal(err)
		}
		b.Files = append(b.Files, files...)
	}
	m := b.Prefix + "/static"
	files, err := writeModule(dir, m, b.Version, b.Time, sourceFiles(m, "package static\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	b.Files = append(b.Files, files...)
	if err = writeJSON(filepath.Join(dir, "bundle.json"), b); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestPublicationRequiresExactDestinationBoundConfirmation(t *testing.T) {
	dir := completeFixture(t)
	p, err := PlanPublication(dir, testEndpoint, "test-bucket")
	if err != nil {
		t.Fatal(err)
	}
	other, err := PlanPublication(dir, testEndpoint, "other-bucket")
	if err != nil {
		t.Fatal(err)
	}
	if p.ApprovalID == other.ApprovalID {
		t.Fatal("approval not bound to bucket")
	}
	called := false
	err = publish(context.Background(), dir, testEndpoint, "other-bucket", p.ApprovalID, func(context.Context, []string) error { called = true; return nil })
	if err == nil || called {
		t.Fatal("upload started without exact confirmation")
	}
	// Receipt edits invalidate a previously reviewed approval, even when the
	// staged payload hashes remain valid.
	data, err := os.ReadFile(filepath.Join(dir, "bundle.json"))
	if err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(dir, "bundle.json"), append(data, '\n'))
	revised, err := PlanPublication(dir, testEndpoint, "test-bucket")
	if err != nil {
		t.Fatal(err)
	}
	if p.ApprovalID == revised.ApprovalID {
		t.Fatal("approval not bound to receipt bytes")
	}
}
func TestPublishIsCreateOnlyAndExposesEntryLast(t *testing.T) {
	dir := completeFixture(t)
	p, err := PlanPublication(dir, testEndpoint, "test-bucket")
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	err = publish(context.Background(), dir, testEndpoint, "test-bucket", p.ApprovalID, func(_ context.Context, args []string) error {
		arg := func(name string) string {
			for i, a := range args {
				if a == name && i+1 < len(args) {
					return args[i+1]
				}
			}
			return ""
		}
		if arg("--if-none-match") != "*" {
			t.Fatal("upload can overwrite published bytes")
		}
		if arg("--endpoint-url") != testEndpoint || arg("--bucket") != "test-bucket" {
			t.Fatal("wrong destination")
		}
		if _, err := os.Stat(arg("--body")); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, arg("--key"))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 15 {
		t.Fatalf("got %d writes", len(keys))
	}
	for i, k := range keys {
		if strings.Contains(k, "/static/") != (i >= 12) {
			t.Fatalf("entry exposed too soon: %v", keys)
		}
	}
	if !strings.HasSuffix(keys[len(keys)-1], ".info") {
		t.Fatal("entry .info is not last")
	}
	for _, n := range p.Bundle.Platforms {
		base := "proxy/" + n.Modules[0] + "/@v/"
		zip, mod, info := -1, -1, -1
		for i, k := range keys {
			if strings.HasPrefix(k, base) {
				switch filepath.Ext(k) {
				case ".zip":
					zip = i
				case ".mod":
					mod = i
				case ".info":
					info = i
				}
			}
		}
		if zip < 0 || mod <= zip || info <= mod {
			t.Fatal("native module advertised before its bytes")
		}
	}
}
func TestPublicationStopsAfterUncertainWriteWithoutRetry(t *testing.T) {
	dir := completeFixture(t)
	p, err := PlanPublication(dir, testEndpoint, "test-bucket")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	err = publish(context.Background(), dir, testEndpoint, "test-bucket", p.ApprovalID, func(context.Context, []string) error {
		calls++
		return fmt.Errorf("simulated connection loss after request")
	})
	if err == nil || calls != 1 || !strings.Contains(err.Error(), "earlier objects may exist") {
		t.Fatalf("unsafe retry/outcome: calls=%d, error=%v", calls, err)
	}
}
func TestPublicationRejectsNonR2Endpoints(t *testing.T) {
	dir := completeFixture(t)
	for _, endpoint := range []string{"http://00000000000000000000000000000000.r2.cloudflarestorage.com", "https://example.com", "https://user:secret@00000000000000000000000000000000.r2.cloudflarestorage.com", testEndpoint + "?token=secret", testEndpoint + "/other-account"} {
		if _, err := PlanPublication(dir, endpoint, "test-bucket"); err == nil {
			t.Fatalf("accepted endpoint %s", endpoint)
		}
	}
}

func TestAWSInvocationDisablesAutomaticRetries(t *testing.T) {
	dir := completeFixture(t)
	p, err := PlanPublication(dir, testEndpoint, "test-bucket")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	fake := filepath.Join(bin, "aws")
	// A local fake executable, never an AWS/R2 client. Exercise the actual
	// subprocess environment without reading or printing any credentials.
	put(t, fake, []byte("#!/bin/sh\n[ \"$AWS_MAX_ATTEMPTS\" = 1 ] || exit 41\n[ \"$AWS_RETRY_MODE\" = standard ] || exit 42\n[ \"$AWS_PAGER\" = '' ] || exit 43\n[ \"$AWS_CLI_AUTO_PROMPT\" = off ] || exit 44\nexit 0\n"))
	if err = os.Chmod(fake, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AWS_MAX_ATTEMPTS", "100")
	if err = Publish(context.Background(), dir, testEndpoint, "test-bucket", p.ApprovalID); err != nil {
		t.Fatal(err)
	}
}

func TestBundleRelativePathsAndSymlinkParents(t *testing.T) {
	dir := completeFixture(t)
	here, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(here, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ReadBundle(rel); err != nil {
		t.Fatalf("relative bundle: %v", err)
	}
	original := filepath.Join(dir, "proxy", "go.example.com", "chdb", "static")
	elsewhere := filepath.Join(t.TempDir(), "moved")
	if err = os.Rename(original, elsewhere); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(elsewhere, original); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadBundle(dir); err == nil {
		t.Fatal("accepted artifact path through a symlink parent")
	}
}
