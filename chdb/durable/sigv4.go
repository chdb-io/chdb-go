package durable

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// AWS Signature Version 4, for the six requests the S3 backend makes.
//
// Signing is here rather than delegated to the AWS SDK on purpose. The durable
// control plane needs exactly two S3 operations — GetObject and PutObject,
// the latter with a precondition — and the SDK that provides them brings a
// dozen modules into the go.mod of everyone who imports this package, whether
// or not they ever touch S3. The roadmap asks the cloud backends not to do
// that to the core module, and in Go a dependency in a subpackage is a
// dependency of the module.
//
// So the surface is small, and staying small is the point: no multipart, no
// listing, no chunked signing, no presigning. What is here is the SigV4
// header-signing form for a request whose payload hash is already known —
// which it always is, because the protocol computes the SHA-256 of everything
// it publishes before publishing it (contract §4.5). The digest a manifest
// records is the digest that signs the upload; there is no second pass over a
// checkpoint archive to produce it.

// There is deliberately no UNSIGNED-PAYLOAD constant here. Every request this
// package signs knows its payload hash, and leaving the escape hatch out means
// a future operation cannot reach for it without first thinking about whether
// the provider accepts it over plain HTTP.
const (
	sigV4Algorithm = "AWS4-HMAC-SHA256"
	s3Service      = "s3"
)

// credentials are what SigV4 needs to sign. SessionToken is set only for
// temporary credentials.
type credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

func (c credentials) valid() bool {
	return c.AccessKeyID != "" && c.SecretAccessKey != ""
}

// resolveCredentials finds credentials the way the AWS tools do, in the order
// they do, stopping at the first complete pair.
//
// Three sources, and the list stops where a hand-written signer stops being
// the right tool:
//
//  1. Given explicitly to the backend.
//  2. The standard environment variables.
//  3. The shared credentials file, honouring AWS_PROFILE.
//
// Not here: SSO, assumed roles, the EC2 and ECS metadata services, and
// anything else that needs a token refresh loop. Those are the AWS SDK's job,
// and a caller who needs them supplies its own Backend or its own credentials
// — which is why explicit credentials come first in the list rather than last.
func resolveCredentials(explicit credentials) (credentials, error) {
	if explicit.valid() {
		return explicit, nil
	}
	env := credentials{
		AccessKeyID:     os.Getenv("AWS_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
		SessionToken:    os.Getenv("AWS_SESSION_TOKEN"),
	}
	if env.valid() {
		return env, nil
	}
	if fromFile, ok := credentialsFromSharedFile(); ok {
		return fromFile, nil
	}
	return credentials{}, newError(CategoryBackend,
		"durable: no S3 credentials found; set AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY, "+
			"configure a shared credentials file, or pass credentials to the backend. "+
			"SSO and instance-role credentials are not resolved here — supply them as environment "+
			"variables or use a Backend built on the AWS SDK")
}

// credentialsFromSharedFile reads the requested profile out of the shared
// credentials file. It is deliberately a small parser: the INI dialect that
// file uses has corners (nested properties, quoted values) that only matter
// for settings this signer does not read.
func credentialsFromSharedFile() (credentials, bool) {
	path := os.Getenv("AWS_SHARED_CREDENTIALS_FILE")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return credentials{}, false
		}
		path = filepath.Join(home, ".aws", "credentials")
	}
	f, err := os.Open(path)
	if err != nil {
		return credentials{}, false
	}
	defer f.Close()

	wanted := os.Getenv("AWS_PROFILE")
	if wanted == "" {
		wanted = "default"
	}
	var (
		inProfile bool
		found     credentials
	)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			name := strings.TrimSpace(line[1 : len(line)-1])
			// A file written by the SSO tooling prefixes profile names.
			name = strings.TrimPrefix(name, "profile ")
			inProfile = name == wanted
			continue
		}
		if !inProfile {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch key {
		case "aws_access_key_id":
			found.AccessKeyID = value
		case "aws_secret_access_key":
			found.SecretAccessKey = value
		case "aws_session_token":
			found.SessionToken = value
		}
	}
	return found, found.valid()
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// signingKey derives the date/region/service-scoped key.
//
// service is a parameter even though this package only ever signs for S3, so
// the derivation can be checked against AWS's own published example — which
// uses a different service. Getting one link of this HMAC chain wrong produces
// a signature that is simply rejected, with nothing to say which link it was.
func signingKey(secret, datestamp, region, service string) []byte {
	k := hmacSHA256([]byte("AWS4"+secret), []byte(datestamp))
	k = hmacSHA256(k, []byte(region))
	k = hmacSHA256(k, []byte(service))
	return hmacSHA256(k, []byte("aws4_request"))
}

// uriEncodePath percent-encodes a path the way S3 canonicalisation wants:
// every byte outside the unreserved set is escaped, and "/" is left alone as a
// separator.
//
// net/url's escaping is not usable here. EscapedPath preserves whatever the
// caller wrote, and QueryEscape turns a space into "+", which produces a
// signature that does not match the request S3 received.
func uriEncodePath(path string) string {
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~/"
	var b strings.Builder
	for i := 0; i < len(path); i++ {
		c := path[i]
		if strings.IndexByte(unreserved, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		const hexDigits = "0123456789ABCDEF"
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&0xf])
	}
	return b.String()
}

// signRequest adds the SigV4 headers to req in place.
//
// payloadHash is the lowercase hex SHA-256 of the body — known before the call
// in every case this package has, which is what keeps the signer to its
// header-signing form. now is passed in so a test can sign at a fixed instant.
func signRequest(req *http.Request, creds credentials, region string, payloadHash string, now time.Time) {
	utc := now.UTC()
	amzDate := utc.Format("20060102T150405Z")
	datestamp := utc.Format("20060102")

	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if creds.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", creds.SessionToken)
	}
	if req.Host == "" {
		req.Host = req.URL.Host
	}

	// Sign host and every header this package sets itself. Signing the
	// preconditions is not required by S3, but they are the headers that
	// decide whether the request mutates anything, so leaving them out of the
	// signature would let something between here and the bucket turn a
	// conditional create into an overwrite.
	signed := map[string]string{"host": req.Host}
	for name := range req.Header {
		lower := strings.ToLower(name)
		switch {
		case strings.HasPrefix(lower, "x-amz-"),
			lower == "if-match", lower == "if-none-match", lower == "content-type":
			signed[lower] = strings.TrimSpace(req.Header.Get(name))
		}
	}
	names := make([]string, 0, len(signed))
	for name := range signed {
		names = append(names, name)
	}
	sort.Strings(names)

	var canonicalHeaders strings.Builder
	for _, name := range names {
		canonicalHeaders.WriteString(name)
		canonicalHeaders.WriteByte(':')
		canonicalHeaders.WriteString(signed[name])
		canonicalHeaders.WriteByte('\n')
	}
	signedHeaders := strings.Join(names, ";")

	// The protocol's keys carry no query parameters, so the canonical query
	// string is whatever the URL holds, in the encoding net/url produced.
	canonicalQuery := req.URL.Query().Encode()

	canonicalRequest := strings.Join([]string{
		req.Method,
		uriEncodePath(req.URL.Path),
		canonicalQuery,
		canonicalHeaders.String(),
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := strings.Join([]string{datestamp, region, s3Service, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		sigV4Algorithm,
		amzDate,
		scope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	signature := hex.EncodeToString(hmacSHA256(
		signingKey(creds.SecretAccessKey, datestamp, region, s3Service),
		[]byte(stringToSign)))

	req.Header.Set("Authorization", sigV4Algorithm+
		" Credential="+creds.AccessKeyID+"/"+scope+
		", SignedHeaders="+signedHeaders+
		", Signature="+signature)
}
