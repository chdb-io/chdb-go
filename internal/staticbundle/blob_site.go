package staticbundle

import (
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type SiteCatalog struct {
	Schema      int             `json:"schema"`
	Prefix      string          `json:"module_prefix"`
	SiteOrigin  string          `json:"site_origin"`
	BlobBaseURL string          `json:"blob_base_url"`
	Modules     []ModuleVersion `json:"modules"`
}

type BlobSite struct {
	Target     BlobTarget `json:"target"`
	ApprovalID string     `json:"approval_id"`
	Files      []File     `json:"files"`
	Notice     string     `json:"notice"`
}

// PrepareBlobSite generates only tiny static discovery/list files and external
// rewrites. Native ZIPs never enter the deployment or a serverless function.
// Deployment is a separate remote action requiring a fresh confirmation.
func PrepareBlobSite(repo, source, out, previous string, t BlobTarget) (BlobSite, error) {
	var result BlobSite
	p, err := PlanBlob(source, t)
	if err != nil {
		return result, err
	}
	catalog := SiteCatalog{Schema: 1, Prefix: p.Prefix, SiteOrigin: t.SiteOrigin, BlobBaseURL: t.BlobBaseURL, Modules: append([]ModuleVersion(nil), p.Modules...)}
	if previous != "" {
		b, err := os.ReadFile(previous)
		if err != nil {
			return result, err
		}
		var old SiteCatalog
		if json.Unmarshal(b, &old) != nil || old.Schema != 1 || old.Prefix != catalog.Prefix || old.SiteOrigin != catalog.SiteOrigin || old.BlobBaseURL != catalog.BlobBaseURL {
			return result, fmt.Errorf("previous catalog identity differs from approved site/store")
		}
		catalog.Modules = append(catalog.Modules, old.Modules...)
	}
	seen := map[string]bool{}
	var modules []ModuleVersion
	for _, m := range catalog.Modules {
		if !validCatalogModule(catalog.Prefix, m.Path) || !validModuleVersion(m.Version) {
			return result, fmt.Errorf("invalid catalog module %s@%s", m.Path, m.Version)
		}
		key := m.Path + "@" + m.Version
		if !seen[key] {
			seen[key] = true
			modules = append(modules, m)
		}
	}
	sort.Slice(modules, func(i, j int) bool {
		if modules[i].Path != modules[j].Path {
			return modules[i].Path < modules[j].Path
		}
		return compareModuleVersions(modules[i].Version, modules[j].Version) < 0
	})
	catalog.Modules = modules
	if err = newOutput(out, repo); err != nil {
		return result, err
	}
	output := filepath.Join(out, ".vercel", "output")
	static := filepath.Join(output, "static")
	put := func(name string, b []byte) error {
		dest := filepath.Join(static, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return err
		}
		return os.WriteFile(dest, b, 0644)
	}
	meta := fmt.Sprintf("<!doctype html>\n<html><head><meta charset=\"utf-8\"><meta name=\"go-import\" content=\"%s mod %s\"><title>chDB Go modules</title></head><body><p>chDB Go module discovery. <a href=\"/\">Versions</a></p></body></html>\n", html.EscapeString(p.Prefix), html.EscapeString(t.SiteOrigin))
	if err = put("discovery.html", []byte(meta)); err != nil {
		return result, err
	}
	byModule := map[string][]string{}
	for _, m := range modules {
		byModule[m.Path] = append(byModule[m.Path], m.Version)
	}
	var paths []string
	for m := range byModule {
		paths = append(paths, m)
	}
	sort.Strings(paths)
	type route map[string]any
	var routes []route
	// Do not copy the Maven rollback behavior: Go checksums can outlive an
	// interrupted release. Versioned objects are never deleted automatically.
	namespace := strings.TrimPrefix(p.Prefix, strings.Split(p.Prefix, "/")[0])
	metaPaths := append([]string{namespace}, paths...)
	for i, s := range metaPaths {
		if i > 0 {
			s = "/" + strings.TrimPrefix(s, strings.Split(s, "/")[0]+"/")
		}
		if s == "" {
			s = "/"
		}
		routes = append(routes, route{"src": "^" + regexp.QuoteMeta(s) + "/?$", "dest": "/discovery.html", "headers": map[string]string{"Content-Type": "text/html; charset=utf-8", "Cache-Control": "public, max-age=60"}})
	}
	for _, m := range paths {
		list := strings.Join(byModule[m], "\n") + "\n"
		if err = put(m+"/@v/list", []byte(list)); err != nil {
			return result, err
		}
		routes = append(routes, route{"src": "^/" + regexp.QuoteMeta(m) + "/@v/list$", "headers": map[string]string{"Content-Type": "text/plain; charset=utf-8", "Cache-Control": "public, max-age=60"}, "continue": true})
	}
	routes = append(routes, route{"handle": "filesystem"})
	versionPattern := `v0\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-rc\.[1-9][0-9]*\.[1-9][0-9]*)?`
	for _, m := range paths {
		headers := map[string]string{"x-vercel-enable-rewrite-caching": "0", "Vercel-CDN-Cache-Control": "no-store", "X-Content-Type-Options": "nosniff"}
		routes = append(routes, route{"src": "^/" + regexp.QuoteMeta(m) + "/@v/(" + versionPattern + ")\\.(zip|mod|info)$", "dest": t.BlobBaseURL + "/proxy/" + m + "/@v/$1.$2", "headers": headers})
		latest := latestModuleVersion(byModule[m])
		routes = append(routes, route{"src": "^/" + regexp.QuoteMeta(m) + "/@latest$", "dest": t.BlobBaseURL + "/proxy/" + m + "/@v/" + latest + ".info", "headers": map[string]string{"x-vercel-enable-rewrite-caching": "0", "Vercel-CDN-Cache-Control": "no-store", "Cache-Control": "public, max-age=60", "X-Content-Type-Options": "nosniff"}})
	}
	routes = append(routes, route{"src": ".*", "status": 404})
	if err = writeJSON(filepath.Join(output, "config.json"), map[string]any{"version": 3, "routes": routes}); err != nil {
		return result, err
	}
	if err = writeJSON(filepath.Join(out, ".vercel", "project.json"), map[string]string{"projectId": t.ProjectID, "orgId": t.TeamID, "projectName": t.Project}); err != nil {
		return result, err
	}
	var body strings.Builder
	body.WriteString("<!doctype html><html><head><meta charset=\"utf-8\"><title>chDB Go modules</title></head><body><h1>chDB Go modules</h1><p>Prebuilt modules are downloaded by Go, not by a build hook.</p><ul>")
	for _, m := range paths {
		fmt.Fprintf(&body, "<li><code>%s</code>: %s</li>", html.EscapeString(m), html.EscapeString(strings.Join(byModule[m], ", ")))
	}
	body.WriteString("</ul></body></html>\n")
	if err = put("index.html", []byte(body.String())); err != nil {
		return result, err
	}
	if err = writeJSON(filepath.Join(static, "catalog.json"), catalog); err != nil {
		return result, err
	}
	result.Target = t
	result.Notice = "LOCAL ONLY. Review this exact static deployment separately after immutable Blob uploads. Use the stable production hostname; do not disable protection settings, deploy large archives, change DNS or modify Maven."
	err = filepath.Walk(filepath.Join(out, ".vercel"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		f, err := describe(out, path)
		if err == nil {
			result.Files = append(result.Files, f)
		}
		return err
	})
	if err != nil {
		return result, err
	}
	scope, _ := json.Marshal(struct {
		Target BlobTarget
		Files  []File
	}{result.Target, result.Files})
	result.ApprovalID = digestBytes(scope)
	return result, writeJSON(filepath.Join(out, "site-plan.json"), result)
}

func validCatalogModule(prefix, module string) bool {
	if module == prefix+"/probe" || module == prefix+"/static" {
		return true
	}
	re := regexp.MustCompile("^" + regexp.QuoteMeta(prefix) + `/libchdb/(darwin|linux)-(amd64|arm64)/part(0|[1-9][0-9]*)$`)
	return re.MatchString(module)
}
func validModuleVersion(v string) bool {
	if !moduleVersionRE.MatchString(v) {
		return false
	}
	for _, s := range strings.FieldsFunc(strings.TrimPrefix(v, "v"), func(r rune) bool { return r == '.' || r == '-' }) {
		if s == "rc" {
			continue
		}
		if _, err := strconv.ParseUint(s, 10, 64); err != nil {
			return false
		}
	}
	return true
}
func compareModuleVersions(a, b string) int {
	parts := func(s string) []string {
		return strings.FieldsFunc(strings.TrimPrefix(s, "v"), func(r rune) bool { return r == '.' || r == '-' })
	}
	aa, bb := parts(a), parts(b)
	for i := 0; i < 3; i++ {
		av, _ := strconv.ParseUint(aa[i], 10, 64)
		bv, _ := strconv.ParseUint(bb[i], 10, 64)
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	if len(aa) == 3 && len(bb) > 3 {
		return 1
	}
	if len(bb) == 3 && len(aa) > 3 {
		return -1
	}
	for i := 4; i < len(aa) && i < len(bb); i++ {
		av, _ := strconv.ParseUint(aa[i], 10, 64)
		bv, _ := strconv.ParseUint(bb[i], 10, 64)
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}
func latestModuleVersion(versions []string) string {
	var best string
	for _, v := range versions {
		if strings.Contains(v, "-") {
			continue
		}
		if best == "" || compareModuleVersions(v, best) > 0 {
			best = v
		}
	}
	if best != "" {
		return best
	}
	for _, v := range versions {
		if best == "" || compareModuleVersions(v, best) > 0 {
			best = v
		}
	}
	return best
}
