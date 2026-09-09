package durable

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// AWS publishes the intermediate signing key for one worked example. It is the
// only independent check available for the HMAC chain, and it is worth having:
// a wrong link produces a signature the service simply rejects, with nothing
// in the rejection to say which link was wrong.
//
// From "Examples of how to derive a signing key for Signature Version 4".
func TestSigningKeyMatchesTheAWSExample(t *testing.T) {
	got := hex.EncodeToString(signingKey(
		"wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "20120215", "us-east-1", "iam"))
	const want = "f4780e2d9f65fa895f9c67b32ce1baf0b0d8a43505a000a1a9e090d414db404d"
	if got != want {
		t.Fatalf("derived key\n got %s\nwant %s", got, want)
	}
}

// Canonicalisation escapes everything outside the unreserved set and leaves
// "/" alone. net/url gets both of these wrong for this purpose: QueryEscape
// turns a space into "+", and EscapedPath keeps whatever the caller wrote.
func TestURIEncodePathFollowsTheCanonicalRules(t *testing.T) {
	cases := map[string]string{
		"/wal/3-9-acde5678.jsonl": "/wal/3-9-acde5678.jsonl",
		"/a b":                    "/a%20b",
		"/test$file.text":         "/test%24file.text",
		"/a+b":                    "/a%2Bb",
		"/a~b-c.d_e":              "/a~b-c.d_e",
		"/nested/key/with/slash":  "/nested/key/with/slash",
		"/héllo":                  "/h%C3%A9llo",
	}
	for input, want := range cases {
		if got := uriEncodePath(input); got != want {
			t.Errorf("uriEncodePath(%q) = %q, want %q", input, got, want)
		}
	}
}

// A stand-in S3 that implements exactly the two operations this backend uses,
// with real preconditions — and checks the signature envelope of everything it
// receives.
type fakeS3 struct {
	t *testing.T

	mu      sync.Mutex
	objects map[string][]byte
	etags   map[string]string
	counter int

	// failNext makes the next matching request answer with a status, once.
	failNext map[string]int

	requests []string
}

func newFakeS3(t *testing.T) (*fakeS3, *httptest.Server) {
	s := &fakeS3{
		t:        t,
		objects:  map[string][]byte{},
		etags:    map[string]string{},
		failNext: map[string]int{},
	}
	server := httptest.NewServer(s)
	t.Cleanup(server.Close)
	return s, server
}

func (s *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, r.Method+" "+r.URL.Path)

	body, _ := io.ReadAll(r.Body)

	// Every request must be signed, and the payload hash must describe the
	// body that actually arrived — the whole reason the backend is handed a
	// digest instead of computing one.
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=") {
		s.t.Errorf("%s %s arrived unsigned: %q", r.Method, r.URL.Path, auth)
	}
	if !strings.Contains(auth, "SignedHeaders=") || !strings.Contains(auth, "Signature=") {
		s.t.Errorf("the Authorization header is malformed: %q", auth)
	}
	sum := sha256.Sum256(body)
	if got := r.Header.Get("X-Amz-Content-Sha256"); got != hex.EncodeToString(sum[:]) {
		s.t.Errorf("%s %s declared payload hash %q, body hashes to %q",
			r.Method, r.URL.Path, got, hex.EncodeToString(sum[:]))
	}
	if r.Header.Get("X-Amz-Date") == "" {
		s.t.Errorf("%s %s carries no X-Amz-Date", r.Method, r.URL.Path)
	}
	// A precondition decides whether the request mutates anything, so it has
	// to be inside the signature.
	for _, header := range []string{"If-None-Match", "If-Match"} {
		if r.Header.Get(header) != "" &&
			!strings.Contains(strings.ToLower(auth), strings.ToLower(header)) {
			s.t.Errorf("%s was sent but not signed: %q", header, auth)
		}
	}

	key := r.URL.Path
	if status, ok := s.failNext[r.Method+" "+key]; ok {
		delete(s.failNext, r.Method+" "+key)
		w.WriteHeader(status)
		return
	}

	switch r.Method {
	case http.MethodGet:
		data, ok := s.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("ETag", s.etags[key])
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	case http.MethodPut:
		_, exists := s.objects[key]
		if r.Header.Get("If-None-Match") == "*" && exists {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		if match := r.Header.Get("If-Match"); match != "" {
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if s.etags[key] != match {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
		}
		s.counter++
		etag := fmt.Sprintf("%q", fmt.Sprintf("etag-%d", s.counter))
		s.objects[key] = body
		s.etags[key] = etag
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *fakeS3) get(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[key]
	return data, ok
}

func (s *fakeS3) fail(method, path string, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext[method+" "+path] = status
}

func s3Fixture(t *testing.T) (*fakeS3, *S3Backend) {
	t.Helper()
	fake, server := newFakeS3(t)
	backend, err := NewS3Backend(S3Options{
		Bucket:      "my-bucket",
		Prefix:      "durable/orders",
		Region:      "eu-west-1",
		Endpoint:    server.URL,
		Credentials: credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "secret"},
		HTTPClient:  server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return fake, backend
}

func TestS3BackendConditionalCreateAndReplace(t *testing.T) {
	ctx := context.Background()
	fake, backend := s3Fixture(t)

	outcome, err := backend.PutBytesIfAbsent(ctx, HeadKey, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if outcome != PutCreated {
		t.Fatalf("create reported %s", outcome)
	}
	if data, ok := fake.get("/my-bucket/durable/orders/head.json"); !ok || string(data) != `{"v":1}` {
		t.Fatalf("the object landed at the wrong key or with the wrong body: %q ok=%v", data, ok)
	}

	// A precondition failure is a compare-and-swap outcome, not a transport
	// error. Everything the state machine does with already-exists depends on
	// that distinction being made here.
	outcome, err = backend.PutBytesIfAbsent(ctx, HeadKey, []byte(`{"v":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if outcome != PutAlreadyExists {
		t.Fatalf("a second create reported %s, want already-exists", outcome)
	}

	data, etag, found, err := backend.GetBytesWithETag(ctx, HeadKey)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if string(data) != `{"v":1}` {
		t.Fatalf("read back %q", data)
	}

	replaced, err := backend.ReplaceIfMatch(ctx, HeadKey, []byte(`{"v":3}`), etag)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Status != ReplaceDone || replaced.ETag == "" {
		t.Fatalf("replace reported %+v", replaced)
	}

	stale, err := backend.ReplaceIfMatch(ctx, HeadKey, []byte(`{"v":4}`), etag)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Status != ReplaceNotMatched {
		t.Fatalf("a stale token reported %s, want not-replaced", stale.Status)
	}
}

func TestS3BackendReportsAMissingKey(t *testing.T) {
	ctx := context.Background()
	_, backend := s3Fixture(t)

	if _, found, err := backend.GetBytes(ctx, HeadKey); err != nil || found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if _, found, err := backend.OpenReader(ctx, "wal/1-1-aaaa1111.jsonl"); err != nil || found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	// A compare-and-swap against a key that is gone did not match; reporting
	// it otherwise would send the caller looking for its own intent in a head
	// that no longer exists.
	outcome, err := backend.ReplaceIfMatch(ctx, HeadKey, []byte(`{}`), `"etag-1"`)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != ReplaceNotMatched {
		t.Fatalf("replacing a missing key reported %s", outcome.Status)
	}
}

// A 5xx may or may not have committed, and the honest answer is the one the
// state machine can resolve by re-reading.
func TestS3BackendReportsServerErrorsAsAmbiguous(t *testing.T) {
	ctx := context.Background()
	fake, backend := s3Fixture(t)

	fake.fail(http.MethodPut, "/my-bucket/durable/orders/head.json", http.StatusInternalServerError)
	outcome, err := backend.PutBytesIfAbsent(ctx, HeadKey, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if outcome != PutAmbiguous {
		t.Fatalf("a 500 on create reported %s, want ambiguous", outcome)
	}

	if _, err := backend.PutBytesIfAbsent(ctx, HeadKey, []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	_, etag, _, err := backend.GetBytesWithETag(ctx, HeadKey)
	if err != nil {
		t.Fatal(err)
	}
	fake.fail(http.MethodPut, "/my-bucket/durable/orders/head.json", http.StatusServiceUnavailable)
	replaced, err := backend.ReplaceIfMatch(ctx, HeadKey, []byte(`{"v":2}`), etag)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Status != ReplaceAmbiguous {
		t.Fatalf("a 503 on replace reported %s, want ambiguous", replaced.Status)
	}
}

func TestS3BackendSurfacesRealFailures(t *testing.T) {
	ctx := context.Background()
	fake, backend := s3Fixture(t)

	fake.fail(http.MethodGet, "/my-bucket/durable/orders/head.json", http.StatusForbidden)
	if _, _, err := backend.GetBytes(ctx, HeadKey); !errors.Is(err, ErrBackend) {
		t.Fatalf("a 403 gave category %q, want backend: %v", CategoryOf(err), err)
	}
}

// A connection that was refused never reached the service, so a caller may
// retry it freely. Anything less certain counts as in doubt.
func TestS3BackendDistinguishesTransportFailuresThatNeverLanded(t *testing.T) {
	ctx := context.Background()
	fake, server := newFakeS3(t)
	_ = fake
	client := server.Client()
	server.Close()

	backend, err := NewS3Backend(S3Options{
		Bucket:      "my-bucket",
		Region:      "eu-west-1",
		Endpoint:    server.URL,
		Credentials: credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "secret"},
		HTTPClient:  client,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.PutBytesIfAbsent(ctx, HeadKey, []byte(`{}`)); !errors.Is(err, ErrBackend) {
		t.Fatalf("a refused connection gave category %q, want backend: %v", CategoryOf(err), err)
	}
}

func TestS3BackendUploadsAFileWithoutRehashingIt(t *testing.T) {
	ctx := context.Background()
	fake, backend := s3Fixture(t)

	archive := filepath.Join(t.TempDir(), "checkpoint.tar.gz")
	body := strings.Repeat("archive bytes ", 1000)
	if err := os.WriteFile(archive, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := digestOf([]byte(body))

	outcome, err := backend.PutFileIfAbsent(ctx, "checkpoints/1-1-aaaa1111.tar.gz", archive, digest)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != PutCreated {
		t.Fatalf("upload reported %s", outcome)
	}
	// The fake asserts that the declared payload hash matches the body it
	// received, so reaching here proves the manifest's digest signed the
	// upload.
	stored, ok := fake.get("/my-bucket/durable/orders/checkpoints/1-1-aaaa1111.tar.gz")
	if !ok || string(stored) != body {
		t.Fatalf("the archive did not arrive intact: ok=%v %d bytes", ok, len(stored))
	}
}

func TestS3BackendRefusesAnObjectOverTheSinglePutCeiling(t *testing.T) {
	ctx := context.Background()
	_, backend := s3Fixture(t)
	// The refusal happens before the file is opened, which is why the path
	// need not exist: a checkpoint too large for one PutObject is a limit, not
	// a missing file.
	_, err := backend.PutFileIfAbsent(ctx, "checkpoints/1-1-aaaa1111.tar.gz", "unused",
		Digest{Size: MaxSinglePutBytes + 1, SHA256: strings.Repeat("0", 64)})
	if !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("category is %q, want limit_exceeded: %v", CategoryOf(err), err)
	}
}

func TestS3BackendURLShapes(t *testing.T) {
	base := S3Options{
		Bucket:      "my-bucket",
		Prefix:      "durable/orders",
		Region:      "eu-west-1",
		Credentials: credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "secret"},
	}
	pathStyle := true
	virtual := false

	cases := []struct {
		name    string
		options S3Options
		want    string
	}{
		{
			name:    "AWS is virtual-hosted",
			options: base,
			want:    "https://my-bucket.s3.eu-west-1.amazonaws.com/durable/orders/head.json",
		},
		{
			name:    "AWS path-style on request",
			options: withPathStyle(base, &pathStyle),
			want:    "https://s3.eu-west-1.amazonaws.com/my-bucket/durable/orders/head.json",
		},
		{
			// What a local MinIO wants, and the reason an endpoint implies it.
			name:    "a custom endpoint is path-style by default",
			options: withEndpoint(base, "http://127.0.0.1:9000"),
			want:    "http://127.0.0.1:9000/my-bucket/durable/orders/head.json",
		},
		{
			name:    "a custom endpoint can be virtual-hosted",
			options: withPathStyle(withEndpoint(base, "https://account.r2.cloudflarestorage.com"), &virtual),
			want:    "https://my-bucket.account.r2.cloudflarestorage.com/durable/orders/head.json",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend, err := NewS3Backend(tc.options)
			if err != nil {
				t.Fatal(err)
			}
			got, _, err := backend.urlFor(HeadKey)
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != tc.want {
				t.Fatalf("URL is %s, want %s", got, tc.want)
			}
		})
	}
}

func withEndpoint(options S3Options, endpoint string) S3Options {
	options.Endpoint = endpoint
	return options
}

func withPathStyle(options S3Options, pathStyle *bool) S3Options {
	options.PathStyle = pathStyle
	return options
}

// The namespace URL is the sort of thing that gets logged, so it carries no
// credentials — and Describe, which goes into error messages, carries none
// either.
func TestS3SchemeParsesANamespaceURL(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDEXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")

	u, err := url.Parse("s3://my-bucket/durable?region=us-west-2&endpoint=http://127.0.0.1:9000")
	if err != nil {
		t.Fatal(err)
	}
	factory, ok := lookupScheme("s3")
	if !ok {
		t.Fatal("the s3 scheme is not registered")
	}
	backend, err := factory(context.Background(), u, "orders")
	if err != nil {
		t.Fatal(err)
	}
	s3, ok := backend.(*S3Backend)
	if !ok {
		t.Fatalf("the factory built a %T", backend)
	}
	if s3.region != "us-west-2" || s3.prefix != "durable/orders" || s3.bucket != "my-bucket" {
		t.Fatalf("the URL parsed to %+v", s3)
	}
	if strings.Contains(s3.Describe(), "secret") || strings.Contains(s3.Describe(), "AKIDEXAMPLE") {
		t.Fatalf("Describe leaks a credential: %s", s3.Describe())
	}
}

func TestS3BackendNeedsCredentials(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "absent"))

	_, err := NewS3Backend(S3Options{Bucket: "my-bucket"})
	if !errors.Is(err, ErrBackend) {
		t.Fatalf("category is %q, want backend: %v", CategoryOf(err), err)
	}
	// The message has to say what to do about it.
	if !strings.Contains(err.Error(), "AWS_ACCESS_KEY_ID") {
		t.Errorf("the message does not name a way to fix it: %v", err)
	}
}

func TestCredentialsComeFromTheSharedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials")
	body := `
# a comment
[default]
aws_access_key_id = DEFAULTKEY
aws_secret_access_key = defaultsecret

[profile staging]
aws_access_key_id = STAGINGKEY
aws_secret_access_key = stagingsecret
aws_session_token = stagingtoken
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", path)

	t.Setenv("AWS_PROFILE", "")
	got, err := resolveCredentials(credentials{})
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessKeyID != "DEFAULTKEY" || got.SecretAccessKey != "defaultsecret" {
		t.Fatalf("the default profile resolved to %+v", got)
	}

	t.Setenv("AWS_PROFILE", "staging")
	got, err = resolveCredentials(credentials{})
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessKeyID != "STAGINGKEY" || got.SessionToken != "stagingtoken" {
		t.Fatalf("the staging profile resolved to %+v", got)
	}

	// Explicit credentials outrank everything, which is how a caller that
	// needs SSO or an instance role supplies what this signer will not fetch.
	got, err = resolveCredentials(credentials{AccessKeyID: "EXPLICIT", SecretAccessKey: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessKeyID != "EXPLICIT" {
		t.Fatalf("explicit credentials were overridden by %+v", got)
	}
}

// The same state machine, driven end to end against the S3 wire protocol
// rather than a directory. This is the check that the two backends are
// interchangeable, which is what "the object is a folder you can move between
// clouds" rests on.
func TestObjectLifecycleOverS3(t *testing.T) {
	ctx := context.Background()
	_, server := newFakeS3(t)

	engine := newFakeEngine()
	ns, err := NewNamespace("s3://my-bucket/durable?region=eu-west-1&endpoint="+server.URL,
		NamespaceOptions{
			EngineFactory: engine.factory(),
			BackendFactory: func(_ context.Context, objectID string) (Backend, error) {
				return NewS3Backend(S3Options{
					Bucket:      "my-bucket",
					Prefix:      "durable/" + objectID,
					Region:      "eu-west-1",
					Endpoint:    server.URL,
					Credentials: credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "secret"},
					HTTPClient:  server.Client(),
				})
			},
		})
	if err != nil {
		t.Fatal(err)
	}

	obj, existed, err := ns.Open(ctx, "orders", OpenOptions{Database: "mem", ScratchRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if existed {
		t.Fatal("a cold object reported as existing")
	}
	mustExecute(t, obj, "CREATE TABLE events (id UInt64) ENGINE = MergeTree ORDER BY id")
	mustExecute(t, obj, "INSERT INTO events VALUES (1)")
	if _, err := obj.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if _, err := obj.Checkpoint(ctx); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	mustExecute(t, obj, "INSERT INTO events VALUES (2)")
	if err := obj.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopen with a fresh engine: everything has to come back through the
	// bucket.
	recovered := newFakeEngine()
	ns2, err := NewNamespace("s3://my-bucket/durable?region=eu-west-1&endpoint="+server.URL,
		NamespaceOptions{
			EngineFactory: recovered.factory(),
			BackendFactory: func(_ context.Context, objectID string) (Backend, error) {
				return NewS3Backend(S3Options{
					Bucket:      "my-bucket",
					Prefix:      "durable/" + objectID,
					Region:      "eu-west-1",
					Endpoint:    server.URL,
					Credentials: credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "secret"},
					HTTPClient:  server.Client(),
				})
			},
		})
	if err != nil {
		t.Fatal(err)
	}
	reopened, existed, err := ns2.Open(ctx, "orders", OpenOptions{ScratchRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close(ctx)
	if !existed {
		t.Fatal("the object exists")
	}
	if got := recovered.statements(); len(got) != 3 {
		t.Fatalf("recovery over S3 produced %v", got)
	}
}
