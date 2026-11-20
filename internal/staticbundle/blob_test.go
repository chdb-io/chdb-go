package staticbundle

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func blobFixture(t *testing.T) (string, string, BlobTarget) {
	t.Helper()
	repo := t.TempDir()
	root := t.TempDir()
	source := filepath.Join(root, "probe")
	target := BlobTarget{Team: "test-team", TeamID: "team_test", Project: "go-probe", ProjectID: "prj_test", StoreID: "store_Test123", BlobBaseURL: "https://test123.public.blob.vercel-storage.com", SiteOrigin: "https://go.example.com"}
	if _, err := PrepareProbe(repo, source, "go.example.com/chdb"); err != nil {
		t.Fatal(err)
	}
	return repo, source, target
}

type blobRoundTrip func(*http.Request) (*http.Response, error)

func (f blobRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(code int, b []byte) *http.Response {
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(b)))}
}
func noObjectsClient(t *testing.T, heads *int) *http.Client {
	t.Helper()
	return &http.Client{Transport: blobRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodHead || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Fatal("preflight must be anonymous HEAD")
		}
		*heads++
		return response(404, nil), nil
	})}
}
func linkedBlobProject(t *testing.T, target BlobTarget) (string, string) {
	t.Helper()
	dir := t.TempDir()
	if err := writeProjectLink(dir, target); err != nil {
		t.Fatal(err)
	}
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, time.Now().Unix()+3600)))
	token := "test-header." + payload + ".test-signature"
	put(t, filepath.Join(dir, ".env.local"), []byte("BLOB_STORE_ID=\""+target.StoreID+"\"\nVERCEL_OIDC_TOKEN=\""+token+"\"\n"))
	return dir, token
}
func writeProjectLink(dir string, t BlobTarget) error {
	if err := os.MkdirAll(filepath.Join(dir, ".vercel"), 0755); err != nil {
		return err
	}
	return writeJSON(filepath.Join(dir, ".vercel", "project.json"), map[string]string{"projectId": t.ProjectID, "orgId": t.TeamID, "projectName": t.Project})
}

func TestTinyProbeUsesLegalImmutableGoModules(t *testing.T) {
	_, source, target := blobFixture(t)
	p, err := PlanBlob(source, target)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != "probe" || len(p.Files) != 6 || p.TotalBytes > 4096 {
		t.Fatalf("probe not small: %+v", p)
	}
	for _, f := range p.Files {
		if strings.HasSuffix(f.Path, "/list") {
			t.Fatal("mutable list must not be uploaded to Blob")
		}
	}
	dir := t.TempDir()
	put(t, filepath.Join(dir, "go.mod"), []byte("module example.com/check\n\ngo 1.21\n"))
	cmd := exec.Command("go", "mod", "download", "-json", p.Prefix+"/probe@v0.0.1", p.Prefix+"/probe@v0.0.2")
	cmd.Dir = dir
	cmd.Env = append(cleanEnv(os.Environ(), "GOWORK", "GOPROXY", "GOSUMDB", "GOMODCACHE", "GOFLAGS"), "GOWORK=off", "GOPROXY=file://"+source+"/proxy", "GOSUMDB=off", "GOMODCACHE="+filepath.Join(dir, "cache"), "GOFLAGS=-modcacherw")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("real Go probe download: %v: %s", err, b)
	}
}
func TestBlobApprovalBindsStoreProjectAndBytes(t *testing.T) {
	_, source, target := blobFixture(t)
	a, err := PlanBlob(source, target)
	if err != nil {
		t.Fatal(err)
	}
	other := target
	other.ProjectID = "prj_other"
	b, err := PlanBlob(source, other)
	if err != nil {
		t.Fatal(err)
	}
	if a.ApprovalID == b.ApprovalID {
		t.Fatal("approval not project-bound")
	}
	other = target
	other.StoreID = "store_Other"
	other.BlobBaseURL = "https://other.public.blob.vercel-storage.com"
	b, err = PlanBlob(source, other)
	if err != nil {
		t.Fatal(err)
	}
	if a.ApprovalID == b.ApprovalID {
		t.Fatal("approval not store-bound")
	}
	heads := 0
	called := false
	err = publishBlob(context.Background(), source, target, "wrong", "", "", "", noObjectsClient(t, &heads), func(context.Context, string, []string, []string) ([]byte, error) { called = true; return nil, nil })
	if err == nil || heads != 0 || called {
		t.Fatal("invalid confirmation reached network or credentials")
	}
	put(t, filepath.Join(source, filepath.FromSlash(a.Files[0].Path)), []byte("changed"))
	if _, err = PlanBlob(source, target); err == nil {
		t.Fatal("accepted changed module artifact")
	}
}
func TestBlobUploadIsPublicCreateOnlyWithEnvironmentOIDC(t *testing.T) {
	_, source, target := blobFixture(t)
	p, err := PlanBlob(source, target)
	if err != nil {
		t.Fatal(err)
	}
	dir, token := linkedBlobProject(t, target)
	heads, uploads := 0, 0
	run := func(_ context.Context, cwd string, env, args []string) ([]byte, error) {
		if cwd != dir {
			t.Fatal("wrong linked project")
		}
		if strings.Contains(strings.Join(args, " "), token) {
			t.Fatal("token in argv")
		}
		if strings.Join(args, " ") == "--version" {
			return []byte(VercelCLIVersion), nil
		}
		if heads != 6 {
			t.Fatal("upload before whole-inventory preflight")
		}
		joined := " " + strings.Join(args, " ") + " "
		for _, bad := range []string{" --force ", " --allow-overwrite ", " --add-random-suffix ", " --oidc-token ", " del ", " deploy "} {
			if strings.Contains(joined, bad) {
				t.Fatalf("unsafe command: %s", bad)
			}
		}
		if !strings.Contains(joined, " --access public ") || !strings.Contains(joined, " --no-multipart ") {
			t.Fatal("wrong small-probe upload options")
		}
		if !strings.Contains(strings.Join(env, "\n"), "VERCEL_OIDC_TOKEN="+token) {
			t.Fatal("missing environment OIDC")
		}
		uploads++
		return []byte("unused public URL"), nil
	}
	if err = publishBlob(context.Background(), source, target, p.ApprovalID, dir, "", "", noObjectsClient(t, &heads), run); err != nil {
		t.Fatal(err)
	}
	if uploads != 6 {
		t.Fatalf("got %d uploads", uploads)
	}
	data, _ := json.Marshal(p)
	if strings.Contains(string(data), token) {
		t.Fatal("secret in plan")
	}
}
func TestBlobPreflightConflictAndUncertainWriteStop(t *testing.T) {
	_, source, target := blobFixture(t)
	p, err := PlanBlob(source, target)
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := linkedBlobProject(t, target)
	uploads := 0
	run := func(_ context.Context, _ string, _ []string, args []string) ([]byte, error) {
		if args[0] == "--version" {
			return []byte(VercelCLIVersion), nil
		}
		uploads++
		return nil, fmt.Errorf("uncertain transport")
	}
	client := &http.Client{Transport: blobRoundTrip(func(*http.Request) (*http.Response, error) { return response(200, nil), nil })}
	if err = publishBlob(context.Background(), source, target, p.ApprovalID, dir, "", "", client, run); err == nil || uploads != 0 {
		t.Fatal("overwrote existing destination")
	}
	heads := 0
	err = publishBlob(context.Background(), source, target, p.ApprovalID, dir, "", "", noObjectsClient(t, &heads), run)
	if err == nil || uploads != 1 || !strings.Contains(err.Error(), "outcome may be unknown") {
		t.Fatalf("uncertain upload was retried: %d %v", uploads, err)
	}
}
func TestBlobCredentialsAndOriginMustMatch(t *testing.T) {
	_, source, target := blobFixture(t)
	p, err := PlanBlob(source, target)
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := linkedBlobProject(t, target)
	put(t, filepath.Join(dir, ".env.local"), []byte("BLOB_STORE_ID=store_other\nVERCEL_OIDC_TOKEN=placeholder\n"))
	if _, err = blobEnvironment(dir, target); err == nil {
		t.Fatal("accepted different store credentials")
	}
	bad := target
	bad.BlobBaseURL = "https://private.example"
	if _, err = PlanBlob(source, bad); err == nil {
		t.Fatal("accepted unrelated Blob origin")
	}
	bad = target
	bad.SiteOrigin = "https://preview.example"
	if _, err = PlanBlob(source, bad); err == nil {
		t.Fatal("module namespace moved to unrelated hostname")
	}
	_ = p
}
func TestEngineBlobUploadRequiresPassedProbe(t *testing.T) {
	dir := completeFixture(t)
	_, _, target := blobFixture(t)
	p, err := PlanBlob(dir, target)
	if err != nil {
		t.Fatal(err)
	}
	err = publishBlob(context.Background(), dir, target, p.ApprovalID, "", "", "", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "-probe-report") {
		t.Fatalf("engine bypassed tiny-module gate: %v", err)
	}
}
