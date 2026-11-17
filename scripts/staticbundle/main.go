// staticbundle stages and verifies native Go modules locally. Only the explicit
// publish/blob-publish commands write remotes, after destination-bound approval.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/chdb-io/chdb-go/v2/internal/staticbundle"
)

func main() {
	if err := mainErr(); err != nil {
		fmt.Fprintln(os.Stderr, "staticbundle:", err)
		os.Exit(1)
	}
}
func mainErr() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: staticbundle release-plan|package|assemble|verify|probe|blob-plan|blob-site|blob-publish|verify-public|plan|publish [flags]")
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
	repo := fs.String("repo", ".", "bindings checkout (read only)")
	releasePath := fs.String("release", "static-release.json", "pinned release manifest, relative to repo")
	out := fs.String("out", "", "new output directory outside the checkout")
	archive := fs.String("archive", "", "authenticated upstream tar.gz to package")
	platform := fs.String("platform", runtime.GOOS+"-"+runtime.GOARCH, "native platform")
	prefix := fs.String("module-prefix", "", "authorized vanity module prefix; required for package")
	bindings := fs.String("bindings-version", "", "root bindings version that will be available when published")
	input := fs.String("input", "", "comma-separated native bundle directories")
	bundle := fs.String("bundle", "", "assembled bundle directory")
	partial := fs.Bool("allow-partial", false, "development only: assemble fewer than four platforms; cannot publish")
	endpoint := fs.String("endpoint", "", "explicit R2 account HTTPS S3 endpoint (plan/publish)")
	bucket := fs.String("bucket", "", "R2 bucket name (plan/publish)")
	confirmation := fs.String("confirm", "", "approval_id from a freshly reviewed publication plan; publish is a remote write")
	blobTarget := fs.String("blob-target", "", "public Vercel project/store target JSON; never a credential file")
	projectDir := fs.String("project-dir", "", "linked Vercel project directory with fresh .env.local OIDC credentials (publish only)")
	vercelCLI := fs.String("vercel-cli", "", "pinned Vercel CLI executable or vc.js; empty uses vercel from PATH")
	previousCatalog := fs.String("previous-catalog", "", "previously deployed catalog.json to preserve older versions in the new site")
	probeReport := fs.String("probe-report", "", "passed public tiny-module verification report, required before engine Blob uploads")
	publicationPath := fs.String("publication-plan", "", "selected publication JSON, relative to repo; local validation and stable publication are separate")
	phase := fs.String("phase", "validation", "publication plan phase: validation or release")
	notices := fs.String("notices", "", "prepared release notice directory; required for public engine publication")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	abs, err := filepath.Abs(*repo)
	if err != nil {
		return err
	}
	*repo = abs
	if *out != "" {
		*out, err = filepath.Abs(*out)
		if err != nil {
			return err
		}
	}
	if !filepath.IsAbs(*releasePath) {
		*releasePath = filepath.Join(*repo, *releasePath)
	}
	ctx := context.Background()
	var publication *staticbundle.PublicationPlan
	arrowVersion := ""
	if *publicationPath != "" {
		if !filepath.IsAbs(*publicationPath) {
			*publicationPath = filepath.Join(*repo, *publicationPath)
		}
		r, err := staticbundle.ReadRelease(*releasePath)
		if err != nil {
			return err
		}
		p, err := staticbundle.ReadPublicationPlan(*publicationPath, r)
		if err != nil {
			return err
		}
		selectedPrefix, versions, err := p.Select(*phase)
		if err != nil {
			return err
		}
		if os.Args[1] == "probe" {
			selectedPrefix = p.Prefix // revalidate the final domain, never the CI namespace
		}
		if *prefix != "" && *prefix != selectedPrefix || *bindings != "" && *bindings != versions.Bindings {
			return fmt.Errorf("explicit module prefix or bindings version conflicts with publication plan")
		}
		*prefix, *bindings, arrowVersion = selectedPrefix, versions.Bindings, versions.Arrow
		publication = &p
		if *bundle != "" && os.Args[1] != "verify-public" {
			if _, err := os.Stat(filepath.Join(*bundle, "bundle.json")); err == nil {
				b, err := staticbundle.ReadBundle(*bundle)
				if err != nil {
					return err
				}
				checkedPhase := *phase
				if strings.HasPrefix(os.Args[1], "blob-") || os.Args[1] == "publish" {
					checkedPhase = "release"
				}
				if err = p.CheckBundle(b, checkedPhase); err != nil {
					return err
				}
			}
		}
	}
	switch os.Args[1] {
	case "release-plan":
		if publication == nil {
			return fmt.Errorf("release-plan requires -publication-plan")
		}
		return printJSON(struct {
			Plan     staticbundle.PublicationPlan `json:"plan"`
			Phase    string                       `json:"phase"`
			Prefix   string                       `json:"selected_prefix"`
			Bindings string                       `json:"selected_bindings"`
			Arrow    string                       `json:"selected_arrow"`
			Notice   string                       `json:"notice"`
		}{*publication, *phase, *prefix, *bindings, arrowVersion, "LOCAL PLAN ONLY. RC snapshots use the CI namespace. Publish stable static entry only after stable root bindings resolve; never overwrite the selected static version to change its RC dependency. No tags, DNS, uploads or deployments are performed."})
	case "probe":
		if *out == "" {
			return fmt.Errorf("probe requires -out and -module-prefix")
		}
		p, err := staticbundle.PrepareProbe(*repo, *out, *prefix)
		if err != nil {
			return err
		}
		return printJSON(p)
	case "blob-plan", "blob-site", "blob-publish", "verify-public":
		if *bundle == "" || *blobTarget == "" {
			return fmt.Errorf("%s requires -bundle and -blob-target", os.Args[1])
		}
		t, err := staticbundle.ReadBlobTarget(*blobTarget)
		if err != nil {
			return err
		}
		if publication != nil && t.SiteOrigin != publication.SiteOrigin {
			return fmt.Errorf("Blob target origin differs from selected publication domain")
		}
		switch os.Args[1] {
		case "blob-plan":
			p, err := staticbundle.PlanBlob(*bundle, t)
			if err != nil {
				return err
			}
			return printJSON(p)
		case "blob-site":
			if *out == "" {
				return fmt.Errorf("blob-site requires -out")
			}
			p, err := staticbundle.PrepareBlobSite(*repo, *bundle, *out, *previousCatalog, t)
			if err != nil {
				return err
			}
			return printJSON(p)
		case "blob-publish":
			return staticbundle.PublishBlob(ctx, *bundle, t, *confirmation, *projectDir, *vercelCLI, *probeReport)
		case "verify-public":
			if *out == "" {
				return fmt.Errorf("verify-public requires -out")
			}
			p, err := staticbundle.VerifyPublicProbe(ctx, *repo, *bundle, *out, t)
			if err != nil {
				return err
			}
			return printJSON(p)
		}
	case "package":
		if *archive == "" || *out == "" {
			return fmt.Errorf("package requires -archive and -out")
		}
		if publication != nil && *phase == "release" && *notices == "" {
			return fmt.Errorf("release packaging requires prepared -notices; use validation phase for development without them")
		}
		r, err := staticbundle.ReadRelease(*releasePath)
		if err != nil {
			return err
		}
		b, err := staticbundle.Package(ctx, staticbundle.Options{Repo: *repo, Archive: *archive, Output: *out, Platform: *platform, Prefix: *prefix, Bindings: *bindings, Notices: *notices, Release: r})
		if err != nil {
			return err
		}
		return printJSON(b)
	case "assemble":
		if *input == "" || *out == "" {
			return fmt.Errorf("assemble requires -input and -out")
		}
		r, err := staticbundle.ReadRelease(*releasePath)
		if err != nil {
			return err
		}
		b, err := staticbundle.Assemble(*repo, *out, strings.Split(*input, ","), r, *partial)
		if err != nil {
			return err
		}
		if publication != nil {
			if err = publication.CheckBundle(b, *phase); err != nil {
				return err
			}
		}
		return printJSON(b)
	case "verify":
		if *bundle == "" || *out == "" {
			return fmt.Errorf("verify requires -bundle and -out")
		}
		return staticbundle.Verify(ctx, *repo, *bundle, *out, arrowVersion)
	case "plan":
		if *endpoint != "" || *bucket != "" {
			p, err := staticbundle.PlanPublication(*bundle, *endpoint, *bucket)
			if err != nil {
				return err
			}
			return printJSON(p)
		}
		b, err := staticbundle.ReadBundle(*bundle)
		if err != nil {
			return err
		}
		if !b.Complete {
			return fmt.Errorf("partial development bundles cannot be published")
		}
		return printJSON(struct {
			Notice string              `json:"notice"`
			Bundle staticbundle.Bundle `json:"bundle"`
		}{"PLAN ONLY: no upload performed; review domain, bucket, root bindings release and license notices before confirming publication. Upload versioned objects with create-only conditional writes, native modules before static entry module, .info last; never replace published bytes.", b})
	case "publish":
		return staticbundle.Publish(ctx, *bundle, *endpoint, *bucket, *confirmation)
	default:
		return fmt.Errorf("unknown command %q", os.Args[1])
	}
	return nil
}
func printJSON(v any) error {
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	return e.Encode(v)
}
