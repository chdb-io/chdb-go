package staticbundle

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const VercelCLIVersion = "65.11.0"

type BlobTarget struct {
	Team        string `json:"team"`
	TeamID      string `json:"team_id"`
	Project     string `json:"project"`
	ProjectID   string `json:"project_id"`
	StoreID     string `json:"store_id"`
	BlobBaseURL string `json:"blob_base_url"`
	SiteOrigin  string `json:"site_origin"`
}

type BlobPublication struct {
	Provider      string          `json:"provider"`
	Kind          string          `json:"kind"`
	Prefix        string          `json:"module_prefix"`
	Target        BlobTarget      `json:"target"`
	ApprovalID    string          `json:"approval_id"`
	ReceiptSHA256 string          `json:"receipt_sha256"`
	Modules       []ModuleVersion `json:"modules"`
	Files         []File          `json:"files"`
	TotalBytes    int64           `json:"total_bytes"`
	Notice        string          `json:"notice"`
}

var blobStoreRE = regexp.MustCompile(`^store_[A-Za-z0-9]+$`)
var resourceIDRE = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
var slugRE = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func ReadBlobTarget(path string) (BlobTarget, error) {
	var t BlobTarget
	b, err := os.ReadFile(path)
	if err != nil {
		return t, err
	}
	if err = json.Unmarshal(b, &t); err != nil {
		return t, err
	}
	return t, t.validate()
}
func (t BlobTarget) validate() error {
	if !slugRE.MatchString(t.Team) || !slugRE.MatchString(t.Project) || !resourceIDRE.MatchString(t.TeamID) || !strings.HasPrefix(t.TeamID, "team_") || len(t.TeamID) <= 5 || !resourceIDRE.MatchString(t.ProjectID) || !strings.HasPrefix(t.ProjectID, "prj_") || len(t.ProjectID) <= 4 || !blobStoreRE.MatchString(t.StoreID) {
		return fmt.Errorf("explicit team/project/store identities required")
	}
	want := "https://" + strings.ToLower(strings.TrimPrefix(t.StoreID, "store_")) + ".public.blob.vercel-storage.com"
	if t.BlobBaseURL != want {
		return fmt.Errorf("public Blob base URL does not match selected store")
	}
	u, err := url.Parse(t.SiteOrigin)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Host != u.Hostname() || !strings.Contains(u.Host, ".") || ValidatePrefix(u.Host) != nil {
		return fmt.Errorf("stable anonymous HTTPS site origin required")
	}
	return nil
}

func PlanBlob(dir string, t BlobTarget) (BlobPublication, error) {
	var p BlobPublication
	if err := t.validate(); err != nil {
		return p, err
	}
	d, err := readDistribution(dir)
	if err != nil {
		return p, err
	}
	if strings.Split(d.Prefix, "/")[0] != strings.TrimPrefix(t.SiteOrigin, "https://") {
		return p, fmt.Errorf("module prefix must use the stable site hostname")
	}
	scope, _ := json.Marshal(t)
	p = BlobPublication{Provider: "vercel-blob", Kind: d.Kind, Prefix: d.Prefix, Target: t, ReceiptSHA256: d.ReceiptSHA256, Modules: d.Modules, Files: orderedFiles(d.Files, d.Prefix), Notice: "PLAN ONLY. Fresh human approval is required for this exact inventory and destination. Public, immutable versioned paths only; no random suffix, overwrite, rollback deletion, batch retry, site deployment, DNS or Maven changes. The pinned Vercel SDK may internally retry transport/multipart requests."}
	p.ApprovalID = digestBytes([]byte("vercel-blob\n" + d.ReceiptSHA256 + "\n" + string(scope)))
	for _, f := range p.Files {
		p.TotalBytes += f.Size
	}
	return p, nil
}

type blobRun func(context.Context, string, []string, []string) ([]byte, error)

// PublishBlob is a remote write. Call only after fresh per-upload confirmation.
// OIDC credentials remain in child-process environment, never argv or plans.
func PublishBlob(ctx context.Context, dir string, t BlobTarget, confirmation, projectDir, cli, probeReport string) error {
	return publishBlob(ctx, dir, t, confirmation, projectDir, cli, probeReport, anonymousClient(), func(ctx context.Context, dir string, env, args []string) ([]byte, error) {
		name := "vercel"
		command := args
		if cli != "" {
			if strings.HasSuffix(cli, ".js") {
				name = "node"
				command = append([]string{cli}, args...)
			} else {
				name = cli
			}
		}
		cmd := exec.CommandContext(ctx, name, command...)
		cmd.Dir = dir
		cmd.Env = env
		// SDK diagnostics may contain authentication context. Do not echo them.
		return cmd.Output()
	})
}

func publishBlob(ctx context.Context, dir string, t BlobTarget, confirmation, projectDir, cli, probeReport string, client *http.Client, run blobRun) error {
	p, err := PlanBlob(dir, t)
	if err != nil {
		return err
	}
	if confirmation == "" || confirmation != p.ApprovalID {
		return fmt.Errorf("confirmation must match the destination-bound Blob approval_id")
	}
	if p.Kind == "engine" {
		if err = checkPublicProbeGate(probeReport, p.Prefix, t); err != nil {
			return err
		}
	}
	if err = checkVercelLink(projectDir, t); err != nil {
		return err
	}
	env, err := blobEnvironment(projectDir, t)
	if err != nil {
		return err
	}
	version, err := run(ctx, projectDir, env, []string{"--version"})
	if v := strings.TrimSpace(string(version)); err != nil || v != VercelCLIVersion && v != "Vercel CLI "+VercelCLIVersion {
		return fmt.Errorf("Vercel CLI %s is required; no upload started", VercelCLIVersion)
	}
	// All objects must be absent before the first write. Backend allowOverwrite
	// defaults false as a second guard against stale HEAD responses and races.
	for _, f := range p.Files {
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, t.BlobBaseURL+"/"+f.Path, nil)
		if err != nil {
			return err
		}
		res, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("anonymous Blob preflight failed; no upload started")
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			return fmt.Errorf("refuse existing or inaccessible destination %s (HTTP %d); no upload started", f.Path, res.StatusCode)
		}
	}
	for _, f := range p.Files {
		local := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err = checkDigest(local, f.SHA256); err != nil {
			return err
		}
		multipart := "--no-multipart"
		if f.Size > 64<<20 {
			multipart = "--multipart"
		}
		args := []string{"blob", "put", local, "--pathname", f.Path, "--access", "public", "--content-type", blobContentType(f.Path), "--cache-control-max-age", "31536000", multipart, "--scope", t.Team, "--non-interactive"}
		if _, err = run(ctx, projectDir, env, args); err != nil {
			return fmt.Errorf("Blob upload stopped at %s; outcome may be unknown, inspect the store before any retry; no rollback deletion performed", f.Path)
		}
		fmt.Printf("created %s/%s\n", t.BlobBaseURL, f.Path)
	}
	return nil
}

func blobContentType(path string) string {
	switch filepath.Ext(path) {
	case ".zip":
		return "application/zip"
	case ".info":
		return "application/json"
	default:
		return "text/plain; charset=utf-8"
	}
}
func anonymousClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func checkVercelLink(dir string, t BlobTarget) error {
	var link struct {
		ProjectID   string `json:"projectId"`
		OrgID       string `json:"orgId"`
		ProjectName string `json:"projectName"`
	}
	b, err := os.ReadFile(filepath.Join(dir, ".vercel", "project.json"))
	if err != nil {
		return fmt.Errorf("linked Vercel project required: %w", err)
	}
	if json.Unmarshal(b, &link) != nil || link.ProjectID != t.ProjectID || link.OrgID != t.TeamID || link.ProjectName != t.Project {
		return fmt.Errorf("linked Vercel project differs from approved target")
	}
	return nil
}

func blobEnvironment(dir string, t BlobTarget) ([]string, error) {
	b, err := os.ReadFile(filepath.Join(dir, ".env.local"))
	if err != nil {
		return nil, fmt.Errorf("refresh short-lived project credentials with vercel env pull .env.local --environment development --scope %s", t.Team)
	}
	vars := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || key != "VERCEL_OIDC_TOKEN" && key != "BLOB_STORE_ID" {
			continue
		}
		if _, ok = vars[key]; ok {
			return nil, fmt.Errorf("duplicate project credential variable")
		}
		if strings.HasPrefix(value, "\"") {
			value, err = strconv.Unquote(value)
			if err != nil {
				return nil, fmt.Errorf("invalid quoted project credential")
			}
		}
		vars[key] = value
	}
	if vars["BLOB_STORE_ID"] != t.StoreID {
		return nil, fmt.Errorf("project credentials select a different Blob store")
	}
	parts := strings.Split(vars["VERCEL_OIDC_TOKEN"], ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("short-lived OIDC token required; no long-lived token fallback")
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid OIDC token")
	}
	var claims struct {
		Expires int64 `json:"exp"`
	}
	if json.Unmarshal(data, &claims) != nil || claims.Expires < time.Now().Unix()+60 {
		return nil, fmt.Errorf("OIDC token expired or too close to expiry; refresh project credentials")
	}
	env := cleanEnv(os.Environ(), "BLOB_READ_WRITE_TOKEN", "BLOB_STORE_ID", "VERCEL_OIDC_TOKEN", "VERCEL_PROJECT_ID", "VERCEL_ORG_ID", "VERCEL_TELEMETRY_DISABLED", "CI", "NO_COLOR")
	return append(env, "BLOB_STORE_ID="+t.StoreID, "VERCEL_OIDC_TOKEN="+vars["VERCEL_OIDC_TOKEN"], "VERCEL_PROJECT_ID="+t.ProjectID, "VERCEL_ORG_ID="+t.TeamID, "VERCEL_TELEMETRY_DISABLED=1", "CI=1", "NO_COLOR=1"), nil
}
