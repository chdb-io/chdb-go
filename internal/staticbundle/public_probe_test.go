package staticbundle

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func publicProbeClient(t *testing.T, source string, target BlobTarget, requests *int) *http.Client {
	t.Helper()
	return &http.Client{Transport: blobRoundTrip(func(r *http.Request) (*http.Response, error) {
		*requests++
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Fatal("not anonymous GET")
		}
		if r.URL.Query().Get("go-get") == "1" {
			return response(200, []byte(`<meta name="go-import" content="go.example.com/chdb mod https://go.example.com">`)), nil
		}
		if strings.HasSuffix(r.URL.Path, "/@v/list") {
			return response(200, []byte("v0.0.1\nv0.0.2\n")), nil
		}
		blob, _ := url.Parse(target.BlobBaseURL)
		path := r.URL.Path
		if r.URL.Host != blob.Host {
			path = "/proxy" + path
		}
		b, err := os.ReadFile(filepath.Join(source, filepath.FromSlash(strings.TrimPrefix(path, "/"))))
		if err != nil {
			return response(404, nil), nil
		}
		return response(200, b), nil
	})}
}

func TestPublicProbeUsesDefaultProxyChecksumAndOfflineRebuild(t *testing.T) {
	repo, source, target := blobFixture(t)
	t.Setenv("GOPROXY", "https://private.invalid")
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOPRIVATE", "*")
	out := filepath.Join(t.TempDir(), "public-check")
	requests, gets, offline := 0, 0, 0
	current := ""
	run := func(_ context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
		for _, v := range env {
			if strings.HasPrefix(v, "GOSUMDB=") || strings.HasPrefix(v, "GOPRIVATE=") {
				t.Fatal("checksum/private override leaked into public test")
			}
		}
		joined := strings.Join(args, " ")
		if name == "go" {
			switch args[0] {
			case "env":
				for _, v := range env {
					if strings.HasPrefix(v, "GOPROXY=") {
						t.Fatal("online proxy is not default")
					}
				}
				return []byte(`{"GOPROXY":"https://proxy.golang.org,direct","GOSUMDB":"sum.golang.org","GOAUTH":"off","GOVERSION":"go1.26.5"}`), nil
			case "get":
				gets++
				if strings.HasSuffix(args[1], "@v0.0.1") {
					current = "v0.0.1"
				} else if strings.HasSuffix(args[1], "@latest") {
					current = "v0.0.2"
				} else {
					t.Fatal("unexpected module query")
				}
			case "list":
				return []byte(current + "\n"), nil
			case "build":
				if strings.Contains(joined, "consumer-offline") {
					if !strings.Contains(strings.Join(env, "\n"), "GOPROXY=off") || !strings.Contains(strings.Join(env, "\n"), "HTTPS_PROXY=http://127.0.0.1:1") || !strings.Contains(joined, "-a -mod=readonly") {
						t.Fatal("offline rebuild can use network or old executable")
					}
					offline++
				}
			}
			return nil, nil
		}
		return []byte("chdb-vercel-blob-probe:" + current), nil
	}
	p, err := verifyPublicProbe(context.Background(), repo, source, out, target, publicProbeClient(t, source, target, &requests), run)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != "passed" || !p.Anonymous || !p.Offline || gets != 2 || offline != 1 || requests != 15 {
		t.Fatalf("incomplete proof: %+v %d %d %d", p, gets, offline, requests)
	}
	if err = checkPublicProbeGate(filepath.Join(out, "public-verification.json"), "go.example.com/chdb", target); err != nil {
		t.Fatal(err)
	}
	other := target
	other.StoreID = "store_other"
	other.BlobBaseURL = "https://other.public.blob.vercel-storage.com"
	if err = checkPublicProbeGate(filepath.Join(out, "public-verification.json"), "go.example.com/chdb", other); err == nil {
		t.Fatal("proof reused for another store")
	}
}

func TestPublicProbeRejectsLoginRedirectBeforeGoGet(t *testing.T) {
	repo, source, target := blobFixture(t)
	called := false
	client := &http.Client{Transport: blobRoundTrip(func(*http.Request) (*http.Response, error) {
		r := response(302, nil)
		r.Header.Set("Location", "https://vercel.com/login")
		return r, nil
	}), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	_, err := verifyPublicProbe(context.Background(), repo, source, filepath.Join(t.TempDir(), "check"), target, client, func(context.Context, string, []string, string, ...string) ([]byte, error) {
		called = true
		return nil, nil
	})
	if err == nil || called {
		t.Fatal("protected deployment treated as public")
	}
}

func TestPublicProbeDoesNotRecordPassWhenGoDefaultsFail(t *testing.T) {
	repo, source, target := blobFixture(t)
	requests := 0
	out := filepath.Join(t.TempDir(), "check")
	_, err := verifyPublicProbe(context.Background(), repo, source, out, target, publicProbeClient(t, source, target, &requests), func(context.Context, string, []string, string, ...string) ([]byte, error) {
		return nil, fmt.Errorf("simulated public proxy/checksum error")
	})
	if err == nil {
		t.Fatal("default Go failure ignored")
	}
	if _, err = os.Stat(filepath.Join(out, "public-verification.json")); !os.IsNotExist(err) {
		t.Fatal("failed validation wrote a success report")
	}
}
