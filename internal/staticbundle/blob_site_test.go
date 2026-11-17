package staticbundle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestBlobSiteContainsOnlyDiscoveryListsAndExternalRewrites(t *testing.T) {
	repo, source, target := blobFixture(t)
	out := filepath.Join(t.TempDir(), "site")
	plan, err := PrepareBlobSite(repo, source, out, "", target)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range plan.Files {
		if strings.HasSuffix(f.Path, ".zip") || strings.HasSuffix(f.Path, ".syso") || strings.HasSuffix(f.Path, ".env.local") {
			t.Fatal("payload/credentials in static deployment")
		}
	}
	static := filepath.Join(out, ".vercel", "output", "static")
	b, err := os.ReadFile(filepath.Join(static, "discovery.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `name="go-import" content="go.example.com/chdb mod https://go.example.com"`) {
		t.Fatalf("bad metadata: %s", b)
	}
	b, err = os.ReadFile(filepath.Join(static, "go.example.com/chdb/probe/@v/list"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "v0.0.1\nv0.0.2\n" {
		t.Fatalf("bad version list %q", b)
	}
	var config struct {
		Version int              `json:"version"`
		Routes  []map[string]any `json:"routes"`
	}
	b, err = os.ReadFile(filepath.Join(out, ".vercel", "output", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &config); err != nil {
		t.Fatal(err)
	}
	if config.Version != 3 {
		t.Fatal("not Build Output API v3")
	}
	versionRoute, latestRoute, discovery := false, false, false
	for _, r := range config.Routes {
		src, _ := r["src"].(string)
		if src == "" {
			continue
		}
		re, err := regexp.Compile(src)
		if err != nil {
			t.Fatal(err)
		}
		dest, _ := r["dest"].(string)
		if dest == "/discovery.html" && re.MatchString("/chdb/probe") {
			discovery = true
		}
		if strings.HasPrefix(dest, target.BlobBaseURL) {
			headers := r["headers"].(map[string]any)
			if headers["x-vercel-enable-rewrite-caching"] != "0" || headers["Vercel-CDN-Cache-Control"] != "no-store" {
				t.Fatal("external Range response may be cached twice")
			}
		}
		if re.MatchString("/go.example.com/chdb/probe/@v/v0.0.1.zip") && strings.HasPrefix(dest, "https:") {
			actual := re.ReplaceAllString("/go.example.com/chdb/probe/@v/v0.0.1.zip", dest)
			if actual != target.BlobBaseURL+"/proxy/go.example.com/chdb/probe/@v/v0.0.1.zip" {
				t.Fatalf("wrong rewrite: %s", actual)
			}
			versionRoute = true
		}
		if re.MatchString("/go.example.com/chdb/probe/@latest") && strings.HasPrefix(dest, "https:") {
			if !strings.HasSuffix(dest, "v0.0.2.info") {
				t.Fatal("wrong latest version")
			}
			latestRoute = true
		}
	}
	if !versionRoute || !latestRoute || !discovery {
		t.Fatal("missing module protocol routes")
	}
}

func TestBlobCatalogPreservesOldVersionsAndRejectsStoreSwitch(t *testing.T) {
	repo, source, target := blobFixture(t)
	previous := filepath.Join(t.TempDir(), "catalog.json")
	old := SiteCatalog{Schema: 1, Prefix: "go.example.com/chdb", SiteOrigin: target.SiteOrigin, BlobBaseURL: target.BlobBaseURL, Modules: []ModuleVersion{{"go.example.com/chdb/probe", "v0.0.0"}}}
	if err := writeJSON(previous, old); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "site")
	if _, err := PrepareBlobSite(repo, source, out, previous, target); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(out, ".vercel/output/static/go.example.com/chdb/probe/@v/list"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "v0.0.0\nv0.0.1\nv0.0.2\n" {
		t.Fatal("old version removed")
	}
	old.BlobBaseURL = "https://other.public.blob.vercel-storage.com"
	if err = writeJSON(previous, old); err != nil {
		t.Fatal(err)
	}
	if _, err = PrepareBlobSite(repo, source, filepath.Join(t.TempDir(), "bad-site"), previous, target); err == nil {
		t.Fatal("accepted cross-store catalog")
	}
}
func TestGoModuleVersionOrdering(t *testing.T) {
	for _, pair := range [][2]string{{"v0.0.2", "v0.0.10"}, {"v0.261101.0-rc.2.1", "v0.261101.0-rc.10.1"}, {"v0.261101.0-rc.10.1", "v0.261101.0"}} {
		if compareModuleVersions(pair[0], pair[1]) >= 0 {
			t.Fatal(pair)
		}
	}
	if latestModuleVersion([]string{"v0.0.2", "v0.0.3-rc.1.1"}) != "v0.0.2" {
		t.Fatal("prerelease selected over stable")
	}
}
