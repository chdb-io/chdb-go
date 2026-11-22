package staticbundle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type PublicVerification struct {
	Schema        int       `json:"schema"`
	Status        string    `json:"status"`
	Kind          string    `json:"kind"`
	Prefix        string    `json:"module_prefix"`
	SiteOrigin    string    `json:"site_origin"`
	BlobBaseURL   string    `json:"blob_base_url"`
	ReceiptSHA256 string    `json:"receipt_sha256"`
	GoVersion     string    `json:"go_version"`
	GoProxy       string    `json:"goproxy"`
	GoSumDB       string    `json:"gosumdb"`
	Anonymous     bool      `json:"anonymous"`
	Offline       bool      `json:"offline"`
	Versions      []string  `json:"versions"`
	Completed     time.Time `json:"completed_at"`
}

type publicRun func(context.Context, string, []string, string, ...string) ([]byte, error)

// VerifyPublicProbe does not set GOPROXY/GOSUMDB for online acquisition. It uses
// a cold cache, no saved Go environment/workspace, and disables origin auth.
// The offline rebuild also blocks outgoing HTTP proxies, leaving checksums on.
func VerifyPublicProbe(ctx context.Context, repo, source, out string, t BlobTarget) (PublicVerification, error) {
	return verifyPublicProbe(ctx, repo, source, out, t, anonymousClient(), func(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = dir
		cmd.Env = env
		output, err := cmd.CombinedOutput()
		if err != nil {
			return output, fmt.Errorf("%s %v: %w\n%s", name, args, err, output)
		}
		return output, nil
	})
}

func verifyPublicProbe(ctx context.Context, repo, source, out string, t BlobTarget, client *http.Client, run publicRun) (PublicVerification, error) {
	var result PublicVerification
	p, err := PlanBlob(source, t)
	if err != nil {
		return result, err
	}
	if p.Kind != "probe" {
		return result, fmt.Errorf("verify-public first requires a tiny probe, not engine payloads")
	}
	if err = newOutput(out, repo); err != nil {
		return result, err
	}
	get := func(url string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		res, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("anonymous HTTP %d at %s (no deployment-auth bypass is used)", res.StatusCode, url)
		}
		b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20+1))
		if err != nil {
			return nil, err
		}
		if len(b) > 1<<20 {
			return nil, fmt.Errorf("tiny probe response too large")
		}
		return b, nil
	}
	module := p.Prefix + "/probe"
	namespace := strings.TrimPrefix(p.Prefix, strings.Split(p.Prefix, "/")[0])
	for _, path := range []string{namespace, strings.TrimPrefix(module, strings.Split(module, "/")[0])} {
		body, err := get(t.SiteOrigin + path + "?go-get=1")
		if err != nil {
			return result, err
		}
		if !bytes.Contains(body, []byte(`name="go-import" content="`+p.Prefix+" mod "+t.SiteOrigin+`"`)) {
			return result, fmt.Errorf("missing exact module discovery metadata at %s", path)
		}
	}
	body, err := get(t.SiteOrigin + "/" + module + "/@v/list")
	if err != nil {
		return result, err
	}
	if string(body) != "v0.0.1\nv0.0.2\n" {
		return result, fmt.Errorf("unexpected tiny module version list %q", body)
	}
	for _, f := range p.Files {
		for _, url := range []string{t.BlobBaseURL + "/" + f.Path, t.SiteOrigin + "/" + strings.TrimPrefix(f.Path, "proxy/")} {
			body, err = get(url)
			if err != nil {
				return result, err
			}
			if int64(len(body)) != f.Size || digestBytes(body) != f.SHA256 {
				return result, fmt.Errorf("anonymous download differs from staged bytes at %s", url)
			}
		}
	}
	consumer := filepath.Join(out, "consumer")
	if err = os.Mkdir(consumer, 0755); err != nil {
		return result, err
	}
	if err = os.WriteFile(filepath.Join(consumer, "go.mod"), []byte("module example.com/chdb-public-probe-consumer\n\ngo 1.21\n"), 0644); err != nil {
		return result, err
	}
	code := "package main\nimport (\"fmt\"; probe \"" + module + "\")\nfunc main() {fmt.Print(probe.Message())}\n"
	if err = os.WriteFile(filepath.Join(consumer, "main.go"), []byte(code), 0644); err != nil {
		return result, err
	}
	env := cleanEnv(os.Environ(), "GOENV", "GOWORK", "GOFLAGS", "GOTOOLCHAIN", "GO111MODULE", "GOPROXY", "GOSUMDB", "GOPRIVATE", "GONOPROXY", "GONOSUMDB", "GOINSECURE", "GOAUTH", "GOMODCACHE", "GOCACHE", "GOOS", "GOARCH", "CGO_ENABLED")
	env = append(env, "GOENV=off", "GOWORK=off", "GOTOOLCHAIN=local", "GO111MODULE=on", "GOAUTH=off", "CGO_ENABLED=0", "GOMODCACHE="+filepath.Join(out, "mod-cache"), "GOCACHE="+filepath.Join(out, "build-cache"))
	data, err := run(ctx, consumer, env, "go", "env", "-json", "GOPROXY", "GOSUMDB", "GOAUTH", "GOVERSION")
	if err != nil {
		return result, err
	}
	var settings map[string]string
	if json.Unmarshal(data, &settings) != nil || settings["GOPROXY"] != "https://proxy.golang.org,direct" || settings["GOSUMDB"] != "sum.golang.org" || settings["GOAUTH"] != "off" {
		return result, fmt.Errorf("public validation requires compiled-in Go proxy/checksum defaults and a Go toolchain supporting GOAUTH=off")
	}
	result = PublicVerification{Schema: 1, Kind: "probe", Prefix: p.Prefix, SiteOrigin: t.SiteOrigin, BlobBaseURL: t.BlobBaseURL, ReceiptSHA256: p.ReceiptSHA256, GoVersion: settings["GOVERSION"], GoProxy: settings["GOPROXY"], GoSumDB: settings["GOSUMDB"], Anonymous: true}
	for _, v := range []string{"v0.0.1", "v0.0.2"} {
		query := v
		if v == "v0.0.2" {
			query = "latest"
		}
		if _, err = run(ctx, consumer, env, "go", "get", module+"@"+query); err != nil {
			return result, err
		}
		data, err = run(ctx, consumer, env, "go", "list", "-m", "-f", "{{.Version}}", module)
		if err != nil {
			return result, err
		}
		if strings.TrimSpace(string(data)) != v {
			return result, fmt.Errorf("selected %q, expected %s", data, v)
		}
		if _, err = run(ctx, consumer, env, "go", "build", "-o", "consumer", "."); err != nil {
			return result, err
		}
		data, err = run(ctx, consumer, env, filepath.Join(consumer, "consumer"))
		if err != nil {
			return result, err
		}
		if string(data) != "chdb-vercel-blob-probe:"+v {
			return result, fmt.Errorf("probe executable returned %q", data)
		}
		result.Versions = append(result.Versions, v)
	}
	if _, err = run(ctx, consumer, env, "go", "mod", "verify"); err != nil {
		return result, err
	}
	offline := append(cleanEnv(env, "GOPROXY", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"), "GOPROXY=off", "HTTP_PROXY=http://127.0.0.1:1", "HTTPS_PROXY=http://127.0.0.1:1", "ALL_PROXY=http://127.0.0.1:1", "NO_PROXY=")
	if _, err = run(ctx, consumer, offline, "go", "build", "-a", "-mod=readonly", "-o", "consumer-offline", "."); err != nil {
		return result, err
	}
	data, err = run(ctx, consumer, offline, filepath.Join(consumer, "consumer-offline"))
	if err != nil {
		return result, err
	}
	if string(data) != "chdb-vercel-blob-probe:v0.0.2" {
		return result, fmt.Errorf("offline executable returned %q", data)
	}
	result.Status = "passed"
	result.Offline = true
	result.Completed = time.Now().UTC()
	return result, writeJSON(filepath.Join(out, "public-verification.json"), result)
}

func checkPublicProbeGate(path, prefix string, t BlobTarget) error {
	if path == "" {
		return fmt.Errorf("engine uploads require -probe-report from a passed public tiny-module verification")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var p PublicVerification
	if json.Unmarshal(b, &p) != nil || p.Schema != 1 || p.Kind != "probe" || p.Status != "passed" || p.Prefix != prefix || p.SiteOrigin != t.SiteOrigin || p.BlobBaseURL != t.BlobBaseURL || !p.Anonymous || !p.Offline || p.GoProxy != "https://proxy.golang.org,direct" || p.GoSumDB != "sum.golang.org" || len(p.Versions) != 2 || p.Versions[0] != "v0.0.1" || p.Versions[1] != "v0.0.2" || p.Completed.IsZero() {
		return fmt.Errorf("public probe report does not establish the selected origin/store and default Go download path")
	}
	return nil
}
