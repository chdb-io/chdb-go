package staticbundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Publication struct {
	Endpoint   string `json:"endpoint"`
	Bucket     string `json:"bucket"`
	ApprovalID string `json:"approval_id"`
	Bundle     Bundle `json:"bundle"`
	Notice     string `json:"notice"`
}

var endpointHostRE = regexp.MustCompile(`^[a-f0-9]{32}(?:\.eu)?\.r2\.cloudflarestorage\.com$`)
var bucketRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

// PlanPublication is local/read-only. Its confirmation token binds the exact
// validated inventory, endpoint and bucket, not merely the engine version.
func PlanPublication(dir, endpoint, bucket string) (Publication, error) {
	var p Publication
	b, err := ReadBundle(dir)
	if err != nil {
		return p, err
	}
	if !b.Complete {
		return p, fmt.Errorf("partial development bundles cannot be published")
	}
	if len(b.Notices) == 0 {
		return p, fmt.Errorf("public engine modules require prepared release notices")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || !endpointHostRE.MatchString(u.Host) || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || !bucketRE.MatchString(bucket) {
		return p, fmt.Errorf("explicit HTTPS R2 account endpoint and bucket name required")
	}
	data, err := os.ReadFile(filepath.Join(dir, "bundle.json"))
	if err != nil {
		return p, err
	}
	h := sha256.Sum256(append(data, []byte("\n"+endpoint+"\n"+bucket)...))
	return Publication{Endpoint: endpoint, Bucket: bucket, ApprovalID: hex.EncodeToString(h[:]), Bundle: b, Notice: "PLAN ONLY. Obtain fresh human confirmation for this exact destination and inventory before publish. Publish requires AWS CLI v2, create-only conditional writes, and no automatic retry."}, nil
}

// Publish is a remote write, unlike every staging command. The caller must
// first obtain per-publication user confirmation of PlanPublication's exact
// destination/inventory. No CI workflow invokes it automatically.
func Publish(ctx context.Context, dir, endpoint, bucket, confirmation string) error {
	return publish(ctx, dir, endpoint, bucket, confirmation, func(ctx context.Context, args []string) error {
		cmd := exec.CommandContext(ctx, "aws", args...)
		cmd.Env = append(cleanEnv(os.Environ(), "AWS_MAX_ATTEMPTS", "AWS_RETRY_MODE", "AWS_PAGER", "AWS_CLI_AUTO_PROMPT"), "AWS_MAX_ATTEMPTS=1", "AWS_RETRY_MODE=standard", "AWS_PAGER=", "AWS_CLI_AUTO_PROMPT=off")
		// Do not print SDK output/credential diagnostics into a shared log.
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("AWS CLI failed; inspect the destination before retrying: %w", err)
		}
		return nil
	})
}

func publish(ctx context.Context, dir, endpoint, bucket, confirmation string, put func(context.Context, []string) error) error {
	p, err := PlanPublication(dir, endpoint, bucket)
	if err != nil {
		return err
	}
	if confirmation == "" || confirmation != p.ApprovalID {
		return fmt.Errorf("publication confirmation must match plan approval_id for the exact bundle, endpoint and bucket")
	}
	files := append([]File(nil), p.Bundle.Files...)
	staticRoot := "proxy/" + p.Bundle.Prefix + "/static/"
	// Every native module completes before the entry is exposed; each module's
	// .info is last so the origin's version list only advertises finished data.
	rank := func(f File) int {
		r := 0
		if strings.HasPrefix(f.Path, staticRoot) {
			r = 10
		}
		switch filepath.Ext(f.Path) {
		case ".zip":
			return r
		case ".mod":
			return r + 1
		default:
			return r + 2
		}
	}
	sort.Slice(files, func(i, j int) bool {
		a, b := rank(files[i]), rank(files[j])
		if a != b {
			return a < b
		}
		return files[i].Path < files[j].Path
	})
	for _, f := range files {
		// ReadBundle verified every artifact before the first write. Recheck the
		// particular file immediately before upload as well.
		local := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err = checkDigest(local, f.SHA256); err != nil {
			return err
		}
		args := []string{"--endpoint-url", endpoint, "--region", "auto", "--no-cli-pager", "--no-cli-auto-prompt", "s3api", "put-object", "--bucket", bucket, "--key", f.Path, "--body", local, "--if-none-match", "*"}
		if err = put(ctx, args); err != nil {
			return fmt.Errorf("stopped at s3://%s/%s (earlier objects may exist; do not overwrite or blindly replay): %w", bucket, f.Path, err)
		}
		fmt.Printf("created s3://%s/%s\n", bucket, f.Path)
	}
	return nil
}
