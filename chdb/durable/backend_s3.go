package durable

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

// S3-compatible backend — AWS S3, Cloudflare R2, MinIO, and anything else
// that speaks the same two operations.
//
// This is the backend that makes the whole thing mean something. A local
// directory cannot be a remote authority: when the machine holding it is gone,
// so is the object. Recovering a database on a *different* machine needs the
// head, the checkpoints and the WAL to live somewhere neither machine owns.
//
// # Conditional writes are the whole contract
//
// The protocol needs a real atomic create and a real atomic compare-and-swap;
// simulating either with a HEAD followed by a PUT is not a weaker version, it
// is the bug the protocol exists to prevent. S3 provides both as preconditions
// on PutObject:
//
//	PutBytesIfAbsent  ->  PUT with If-None-Match: *
//	ReplaceIfMatch    ->  PUT with If-Match: <etag>
//
// A precondition failure is a compare-and-swap outcome, not a transport error,
// and it is reported as one. Everything the state machine does with
// PutAlreadyExists and ReplaceNotMatched depends on that distinction being
// made here rather than upstream.
//
// # No retries here, on purpose
//
// The object layer already knows how to settle an uncertain write: the keys it
// publishes are unique per attempt, so it re-reads and compares a digest, and
// its head commits re-read and look for their own intent. A retry loop in the
// backend would sit underneath all of that and turn a request that landed into
// a precondition failure — which is recoverable, but only because the layer
// above never trusts a status code on its own. Leaving the retry to the layer
// that can prove what happened keeps one deadline and one attempt count for
// the whole commit, which is what the contract asks for (§5.8).
//
// # ETags stay opaque, and carry one assumption worth naming
//
// An S3 ETag is quoted, and it is only an MD5 for single-part uploads — not on
// R2, not for multipart, not necessarily forever. It is stored and handed back
// exactly as received and never parsed, which is what the contract requires.
//
// Being a content hash has a consequence a version counter would not have:
// writing *byte-identical* content does not advance the ETag, so the token
// used for that write stays valid afterwards and a second racer holding it can
// also win. Durable is safe from this because every head write changes the
// bytes — a lease acquisition moves the generation, a heartbeat moves
// expires_at, and a flush or checkpoint moves manifest.seq. That is a real
// dependency rather than a coincidence, so it is written down here: anything
// that made a head write idempotent at the byte level would break
// compare-and-swap on any content-hash-ETag provider.
//
// # V1 limits
//
// Single PutObject only, so an object caps at 5 GiB and a larger checkpoint
// fails with limit_exceeded rather than silently truncating. Multipart upload
// is the fix and is not here yet.

// MaxSinglePutBytes is the ceiling for one PutObject. Beyond it a checkpoint
// needs multipart upload.
const MaxSinglePutBytes int64 = 5 * 1024 * 1024 * 1024

// S3Options configures an S3-compatible backend.
type S3Options struct {
	Bucket string

	// Prefix is the key prefix for this object, without a leading slash. It
	// may be empty.
	Prefix string

	Region string

	// Endpoint overrides the AWS endpoint, for MinIO, R2, or an S3-compatible
	// gateway. It must include a scheme.
	Endpoint string

	// PathStyle puts the bucket in the path rather than the hostname. It
	// defaults to true when Endpoint is set, because that is what a local
	// MinIO wants, and false against AWS.
	PathStyle *bool

	// Credentials, when set, take precedence over the environment and the
	// shared credentials file.
	Credentials credentials

	// HTTPClient overrides the client. The default has a 5-minute timeout,
	// which a checkpoint of a large database may need to raise.
	HTTPClient *http.Client
}

// S3Backend stores one object under a bucket prefix.
type S3Backend struct {
	bucket    string
	prefix    string
	region    string
	endpoint  *url.URL
	pathStyle bool
	creds     credentials
	client    *http.Client
	describe  string
}

func init() {
	// s3://<bucket>/<prefix>?region=&endpoint=&pathStyle=
	//
	// The query parameters are what make one implementation serve three
	// providers:
	//
	//	AWS    s3://my-bucket/durable?region=eu-west-1
	//	R2     s3://my-bucket/durable?region=auto&endpoint=https://<id>.r2.cloudflarestorage.com
	//	MinIO  s3://my-bucket/durable?endpoint=http://127.0.0.1:9000
	//
	// Credentials are deliberately not among them. They come from the
	// environment or the shared credentials file, because a namespace URL is
	// the sort of thing that gets logged, put in a config file and pasted into
	// an issue.
	RegisterBackendScheme("s3", func(_ context.Context, u *url.URL, objectID string) (Backend, error) {
		bucket := u.Host
		if bucket == "" {
			return nil, newError(CategoryBackend, "durable: an s3 namespace URL needs a bucket: %s", u)
		}
		basePrefix := strings.Trim(u.Path, "/")
		prefix := objectID
		if basePrefix != "" {
			prefix = basePrefix + "/" + objectID
		}
		query := u.Query()
		options := S3Options{
			Bucket:   bucket,
			Prefix:   prefix,
			Region:   query.Get("region"),
			Endpoint: query.Get("endpoint"),
		}
		if raw := query.Get("pathStyle"); raw != "" {
			pathStyle := raw == "true" || raw == "1"
			options.PathStyle = &pathStyle
		}
		return NewS3Backend(options)
	})
}

// NewS3Backend binds a backend to one object's key prefix.
func NewS3Backend(options S3Options) (*S3Backend, error) {
	if options.Bucket == "" {
		return nil, newError(CategoryBackend, "durable: the S3 backend needs a bucket")
	}
	creds, err := resolveCredentials(options.Credentials)
	if err != nil {
		return nil, err
	}
	region := options.Region
	if region == "" {
		region = os.Getenv("AWS_REGION")
	}
	if region == "" {
		region = os.Getenv("AWS_DEFAULT_REGION")
	}
	if region == "" {
		// The signature needs a region whether or not the provider cares which
		// one. us-east-1 is what an S3-compatible store that ignores regions
		// expects to be told.
		region = "us-east-1"
	}

	backend := &S3Backend{
		bucket: options.Bucket,
		prefix: strings.Trim(options.Prefix, "/"),
		region: region,
		creds:  creds,
		client: options.HTTPClient,
	}
	if backend.client == nil {
		backend.client = &http.Client{Timeout: 5 * time.Minute}
	}
	if options.Endpoint != "" {
		endpoint, err := url.Parse(options.Endpoint)
		if err != nil {
			return nil, wrapError(CategoryBackend, err, "durable: %q is not an endpoint URL",
				options.Endpoint)
		}
		if endpoint.Scheme == "" || endpoint.Host == "" {
			return nil, newError(CategoryBackend,
				"durable: an S3 endpoint needs a scheme and a host, got %q", options.Endpoint)
		}
		backend.endpoint = endpoint
		backend.pathStyle = true
	}
	if options.PathStyle != nil {
		backend.pathStyle = *options.PathStyle
	}
	// Never the credentials, and never a presigned anything: this string ends
	// up in error messages and logs.
	backend.describe = "s3://" + backend.bucket + "/" + backend.prefix
	return backend, nil
}

// Describe implements Backend.
func (b *S3Backend) Describe() string { return b.describe }

// keyFor prefixes a protocol key, refusing one that is not a plain relative
// key.
func (b *S3Backend) keyFor(key string) (string, error) {
	if !IsValidObjectKey(key) {
		return "", newError(CategoryBackend, "durable: refusing to resolve invalid key %q", key)
	}
	if b.prefix == "" {
		return key, nil
	}
	return b.prefix + "/" + key, nil
}

// urlFor builds the request URL and the Host header value for a key.
func (b *S3Backend) urlFor(key string) (*url.URL, string, error) {
	fullKey, err := b.keyFor(key)
	if err != nil {
		return nil, "", err
	}
	out := &url.URL{}
	switch {
	case b.endpoint != nil && b.pathStyle:
		out.Scheme = b.endpoint.Scheme
		out.Host = b.endpoint.Host
		out.Path = strings.TrimSuffix(b.endpoint.Path, "/") + "/" + b.bucket + "/" + fullKey
	case b.endpoint != nil:
		out.Scheme = b.endpoint.Scheme
		out.Host = b.bucket + "." + b.endpoint.Host
		out.Path = strings.TrimSuffix(b.endpoint.Path, "/") + "/" + fullKey
	case b.pathStyle:
		out.Scheme = "https"
		out.Host = "s3." + b.region + ".amazonaws.com"
		out.Path = "/" + b.bucket + "/" + fullKey
	default:
		out.Scheme = "https"
		out.Host = b.bucket + ".s3." + b.region + ".amazonaws.com"
		out.Path = "/" + fullKey
	}
	return out, out.Host, nil
}

// send signs and performs one request.
func (b *S3Backend) send(req *http.Request, payloadHash string) (*http.Response, error) {
	signRequest(req, b.creds, b.region, payloadHash, time.Now())
	return b.client.Do(req)
}

func (b *S3Backend) newRequest(ctx context.Context, method, key string, body io.Reader) (*http.Request, error) {
	target, host, err := b.urlFor(key)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, wrapError(CategoryBackend, err, "durable: cannot build a request for %s", key)
	}
	req.Host = host
	return req, nil
}

// GetBytes implements Backend.
func (b *S3Backend) GetBytes(ctx context.Context, key string) ([]byte, bool, error) {
	data, _, found, err := b.GetBytesWithETag(ctx, key)
	return data, found, err
}

// GetBytesWithETag implements Backend.
func (b *S3Backend) GetBytesWithETag(ctx context.Context, key string) ([]byte, string, bool, error) {
	req, err := b.newRequest(ctx, http.MethodGet, key, nil)
	if err != nil {
		return nil, "", false, err
	}
	resp, err := b.send(req, sha256Hex(nil))
	if err != nil {
		return nil, "", false, b.transportError("reading "+key, err)
	}
	defer drainAndClose(resp)

	if resp.StatusCode == http.StatusNotFound {
		return nil, "", false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", false, b.statusError("reading "+key, resp)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		// An absent ETag leaves nothing to compare-and-swap against, so it is
		// a hard failure rather than an empty token that silently never
		// matches.
		return nil, "", false, newError(CategoryBackend,
			"durable: %s came back from %s without an ETag; this provider cannot support "+
				"compare-and-swap", key, b.describe)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", false, b.transportError("reading the body of "+key, err)
	}
	return data, etag, true, nil
}

// OpenReader implements Backend. The caller closes the reader, which is also
// what releases the connection.
func (b *S3Backend) OpenReader(ctx context.Context, key string) (io.ReadCloser, bool, error) {
	req, err := b.newRequest(ctx, http.MethodGet, key, nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := b.send(req, sha256Hex(nil))
	if err != nil {
		return nil, false, b.transportError("opening "+key, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		drainAndClose(resp)
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		err := b.statusError("opening "+key, resp)
		drainAndClose(resp)
		return nil, false, err
	}
	return resp.Body, true, nil
}

// PutBytesIfAbsent implements Backend.
func (b *S3Backend) PutBytesIfAbsent(ctx context.Context, key string, data []byte) (PutOutcome, error) {
	if err := b.assertWithinSinglePut(key, int64(len(data))); err != nil {
		return PutAmbiguous, err
	}
	req, err := b.newRequest(ctx, http.MethodPut, key, strings.NewReader(string(data)))
	if err != nil {
		return PutAmbiguous, err
	}
	req.ContentLength = int64(len(data))
	req.Header.Set("If-None-Match", "*")
	return b.conditionalPut(req, key, sha256Hex(data))
}

// PutFileIfAbsent implements Backend.
func (b *S3Backend) PutFileIfAbsent(ctx context.Context, key, localPath string, digest Digest) (PutOutcome, error) {
	if err := b.assertWithinSinglePut(key, digest.Size); err != nil {
		return PutAmbiguous, err
	}
	f, err := os.Open(localPath)
	if err != nil {
		return PutAmbiguous, wrapError(CategoryBackend, err, "durable: cannot read %s", localPath)
	}
	defer f.Close()

	req, err := b.newRequest(ctx, http.MethodPut, key, f)
	if err != nil {
		return PutAmbiguous, err
	}
	// Not optional: without a length the request goes out chunked, and SigV4
	// header signing of a chunked body is a different signing scheme.
	req.ContentLength = digest.Size
	req.Header.Set("If-None-Match", "*")
	// The digest the manifest records is the digest that signs the upload, so
	// a checkpoint archive is never read twice.
	return b.conditionalPut(req, key, digest.SHA256)
}

// ReplaceIfMatch implements Backend.
func (b *S3Backend) ReplaceIfMatch(ctx context.Context, key string, data []byte, etag string) (ReplaceOutcome, error) {
	if err := b.assertWithinSinglePut(key, int64(len(data))); err != nil {
		return ReplaceOutcome{}, err
	}
	req, err := b.newRequest(ctx, http.MethodPut, key, strings.NewReader(string(data)))
	if err != nil {
		return ReplaceOutcome{}, err
	}
	req.ContentLength = int64(len(data))
	req.Header.Set("If-Match", etag)

	resp, err := b.send(req, sha256Hex(data))
	if err != nil {
		if isAmbiguousTransportError(err) {
			return ReplaceOutcome{Status: ReplaceAmbiguous}, nil
		}
		return ReplaceOutcome{}, b.transportError("replacing "+key, err)
	}
	defer drainAndClose(resp)

	if isPreconditionFailure(resp.StatusCode) {
		return ReplaceOutcome{Status: ReplaceNotMatched}, nil
	}
	if resp.StatusCode == http.StatusNotFound {
		// The target is gone, so the compare-and-swap did not match. Reporting
		// it as anything else would send the caller looking for its own intent
		// in a head that no longer exists.
		return ReplaceOutcome{Status: ReplaceNotMatched}, nil
	}
	if resp.StatusCode >= 500 {
		return ReplaceOutcome{Status: ReplaceAmbiguous}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return ReplaceOutcome{}, b.statusError("replacing "+key, resp)
	}
	newETag := resp.Header.Get("ETag")
	if newETag == "" {
		// The write landed but the new token is unknown, so the next
		// compare-and-swap would have nothing to present. Re-reading is the
		// honest way out.
		return ReplaceOutcome{Status: ReplaceAmbiguous}, nil
	}
	return ReplaceOutcome{Status: ReplaceDone, ETag: newETag}, nil
}

func (b *S3Backend) conditionalPut(req *http.Request, key, payloadHash string) (PutOutcome, error) {
	resp, err := b.send(req, payloadHash)
	if err != nil {
		if isAmbiguousTransportError(err) {
			return PutAmbiguous, nil
		}
		return PutAmbiguous, b.transportError("creating "+key, err)
	}
	defer drainAndClose(resp)

	switch {
	case isPreconditionFailure(resp.StatusCode):
		return PutAlreadyExists, nil
	case resp.StatusCode >= 500:
		return PutAmbiguous, nil
	case resp.StatusCode == http.StatusOK:
		return PutCreated, nil
	default:
		return PutAmbiguous, b.statusError("creating "+key, resp)
	}
}

func (b *S3Backend) assertWithinSinglePut(key string, size int64) error {
	if size > MaxSinglePutBytes {
		return &Error{
			Category: CategoryLimitExceeded,
			Message: fmt.Sprintf("durable: %s is %d bytes, over the %d-byte ceiling for a single "+
				"PutObject; multipart upload is not implemented, so checkpoint more often or "+
				"reduce the database", key, size, MaxSinglePutBytes),
			Limit:    MaxSinglePutBytes,
			Observed: size,
		}
	}
	return nil
}

// isPreconditionFailure reports whether a status means someone else got there
// first.
//
// S3 answers 412 for If-Match and for If-None-Match: * against an object that
// already exists; a race between two conditional writes can also surface as
// 409 ConditionalRequestConflict. Both mean the same thing to the protocol.
func isPreconditionFailure(status int) bool {
	return status == http.StatusPreconditionFailed || status == http.StatusConflict
}

// isAmbiguousTransportError reports whether a request that failed in transport
// could still have been committed.
//
// The distinction is the difference between a retry and a commit_ambiguous. A
// timeout or a reset connection may have reached the service; a refused
// connection or an unresolvable host did not. Guessing "did not" when it did
// is how a caller ends up publishing twice, so anything genuinely in doubt
// counts as in doubt — the two cases below are the only ones ruled out.
func isAmbiguousTransportError(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return false
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return false
	}
	return true
}

func (b *S3Backend) transportError(what string, err error) error {
	// The message names what failed and where, never the credentials that
	// signed it. The cause is attached for a caller that wants the detail.
	return wrapError(CategoryBackend, err, "durable: %s failed against %s", what, b.describe)
}

// statusError turns an unexpected status into a backend error, carrying the
// beginning of the provider's own message. S3 answers with an XML document
// whose Code and Message are what an operator needs; the cap is there because
// an error body is not a place to trust a length.
func (b *S3Backend) statusError(what string, resp *http.Response) error {
	var detail string
	if body, err := io.ReadAll(io.LimitReader(resp.Body, 2048)); err == nil && len(body) > 0 {
		detail = ": " + strings.TrimSpace(string(body))
	}
	return newError(CategoryBackend, "durable: %s failed against %s (HTTP %d)%s",
		what, b.describe, resp.StatusCode, detail)
}

// drainAndClose releases a response so its connection can be reused. Closing
// without reading leaves the connection unusable, which turns every request
// after an error into a fresh TCP handshake.
func drainAndClose(resp *http.Response) {
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	resp.Body.Close()
}
