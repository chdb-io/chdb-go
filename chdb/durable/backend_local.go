package durable

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Local-filesystem backend.
//
// This is the backend every test uses, and the one a developer gets from a
// file:// namespace URL. That makes its conditional operations load-bearing
// rather than a convenience: if they were approximations, every scenario that
// passes here would prove nothing about the ones that run against a real
// object store.
//
// # Conditional create
//
// link(2) is the primitive. It fails with EEXIST atomically, so writing to a
// unique scratch file and then linking it into place is a true
// create-if-absent — with the full contents already in the file at the moment
// the name appears. Rename would have been simpler and wrong: it clobbers.
//
// # Conditional replace
//
// POSIX has no compare-and-swap on file contents, and the usual workarounds
// are worse than the problem. A lock file turns a crash into a stuck object
// that needs a staleness heuristic to recover; read-compare-rename has the
// race it is meant to prevent.
//
// So the one mutable key is stored as a chain of immutable versions with a
// symlink naming the current one:
//
//	head.json                -> symlink to .head-versions/7.json
//	.head-versions/7.json    immutable
//	.head-versions/6.json    immutable
//
// The ETag is the version number. Replacing against ETag v7 means creating
// .head-versions/8.json, and link(2) lets exactly one racer do that — so the
// EEXIST *is* the compare-and-swap failure. The symlink is then swapped in
// with an atomic rename. A writer that wins the create and dies before the
// rename has still legitimately won: a racer reading version 7 will fail to
// create 8 and correctly report not-replaced.
//
// Version numbers only ever move forward, because creating version N+1
// requires having read version N, which requires the symlink to already point
// at it.
//
// # Reading what another binding wrote
//
// An object produced elsewhere has a plain head.json file and no version
// chain. That reads fine — the ETag is then a content digest — and the first
// ReplaceIfMatch adopts it into the chain: verify the plain file still hashes
// to the ETag, create .head-versions/1.json exclusively, swap the symlink.
// Adoption is atomic against other adopters, since only one can win the
// create.
//
// # What this backend is for, and what that rules out
//
// Development and conformance. Recovery on another machine needs storage
// neither machine owns, which is the S3 backend; a directory on one host
// cannot be a remote authority. That scope decides which hardening belongs
// here and which is noise.
//
// Two things it does defend, because they are real regardless of scope:
//
//   - **Keys out of head.json.** A head is fetched from object storage and is
//     therefore untrusted input. A key containing "..", an absolute path or an
//     empty component would steer a read or a publish outside the object, so
//     pathFor refuses it.
//   - **Losing data it said it had written.** Publishing without flushing is a
//     correctness bug, and a test baseline that can silently roll back is not
//     a baseline. The promise has to hold all the way down: flushing a file
//     and its immediate parent is worthless if the parent is itself a fresh,
//     unflushed entry in a directory above, so a publish that creates
//     directories flushes the chain it created.
//
// What it does not defend against is a hostile local filesystem — a symlink
// planted in the object prefix, say, to redirect a write. Whoever can do that
// can already rewrite the objects directly, so guarding it buys no privilege
// boundary.

const (
	// versionsDir is where the mutable key's version chain lives, relative to
	// the object prefix. One directory, because V1 has exactly one mutable
	// key. It is dot-prefixed so it cannot collide with a protocol key, and a
	// reader that only understands plain files still sees a correct head.json
	// through the symlink.
	versionsDir = ".head-versions"
	// tmpDir is scratch for partially written files.
	tmpDir = ".tmp"
)

var versionTarget = regexp.MustCompile(`^` + regexp.QuoteMeta(versionsDir) + `/(\d+)\.json$`)

func init() {
	// file:///abs/path — the URL form. An opaque "local:/path" is accepted too
	// for symmetry with the other bindings' namespace URLs.
	local := func(_ context.Context, u *url.URL, objectID string) (Backend, error) {
		root := u.Path
		if root == "" {
			root = u.Opaque
		}
		if !filepath.IsAbs(root) {
			return nil, newError(CategoryBackend,
				"durable: a %s namespace needs an absolute path, got %q", u.Scheme, u.String())
		}
		return NewLocalBackend(filepath.Join(root, objectID))
	}
	RegisterBackendScheme("file", local)
	RegisterBackendScheme("local", local)
}

// LocalBackend stores one object as a directory.
type LocalBackend struct {
	root string
}

// NewLocalBackend binds a backend to one object's directory, creating it if
// needed.
func NewLocalBackend(root string) (*LocalBackend, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, wrapError(CategoryBackend, err, "durable: cannot resolve %q", root)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, wrapError(CategoryBackend, err, "durable: cannot create %q", abs)
	}
	return &LocalBackend{root: abs}, nil
}

// Describe implements Backend.
func (b *LocalBackend) Describe() string { return "file://" + b.root }

// pathFor resolves a protocol key under the object root, refusing anything
// that is not a plain relative key.
func (b *LocalBackend) pathFor(key string) (string, error) {
	if !IsValidObjectKey(key) {
		return "", newError(CategoryBackend, "durable: refusing to resolve invalid key %q", key)
	}
	full := filepath.Join(b.root, key)
	// Join already cleans the path, and IsValidObjectKey has refused every
	// component that could climb; this is the belt to that braces.
	if full != b.root && !strings.HasPrefix(full, b.root+string(os.PathSeparator)) {
		return "", newError(CategoryBackend, "durable: key %q escapes the object root", key)
	}
	return full, nil
}

// GetBytes implements Backend.
func (b *LocalBackend) GetBytes(_ context.Context, key string) ([]byte, bool, error) {
	path, err := b.pathFor(key)
	if err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, wrapError(CategoryBackend, err, "durable: cannot read %s", key)
	}
	return data, true, nil
}

// GetBytesWithETag implements Backend.
//
// For the mutable key the token is the version the symlink points at, so it
// describes exactly the bytes returned. For anything else — and for a plain
// head.json written by another binding — it is a content digest, which
// describes the same version for the same reason.
func (b *LocalBackend) GetBytesWithETag(ctx context.Context, key string) ([]byte, string, bool, error) {
	path, err := b.pathFor(key)
	if err != nil {
		return nil, "", false, err
	}
	if key == HeadKey {
		if version, ok, err := b.currentVersion(); err != nil {
			return nil, "", false, err
		} else if ok {
			data, err := os.ReadFile(filepath.Join(b.root, versionsDir, strconv.FormatInt(version, 10)+".json"))
			if err != nil {
				// The symlink names a version that is not there. Reading it as
				// absent would hand a fresh writer a cold object on top of a
				// live one, so it is a failure instead.
				return nil, "", false, wrapError(CategoryCorrupt, err,
					"durable: %s points at version %d, which cannot be read", key, version)
			}
			return data, versionETag(version), true, nil
		}
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, wrapError(CategoryBackend, err, "durable: cannot read %s", key)
	}
	return data, digestETag(digestOf(data)), true, nil
}

// OpenReader implements Backend.
func (b *LocalBackend) OpenReader(_ context.Context, key string) (io.ReadCloser, bool, error) {
	path, err := b.pathFor(key)
	if err != nil {
		return nil, false, err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, wrapError(CategoryBackend, err, "durable: cannot open %s", key)
	}
	return f, true, nil
}

// PutBytesIfAbsent implements Backend.
func (b *LocalBackend) PutBytesIfAbsent(_ context.Context, key string, data []byte) (PutOutcome, error) {
	path, err := b.pathFor(key)
	if err != nil {
		return PutAmbiguous, err
	}
	staged, err := b.stage(func(f *os.File) error {
		_, err := f.Write(data)
		return err
	})
	if err != nil {
		return PutAmbiguous, err
	}
	defer os.Remove(staged)
	return b.publish(staged, path, key)
}

// PutFileIfAbsent implements Backend.
//
// The digest is not used here: a local publish is a copy inside one
// filesystem, so there is nothing to sign and nothing a second read of the
// same bytes would establish. It is part of the interface because a remote
// backend does need it.
func (b *LocalBackend) PutFileIfAbsent(_ context.Context, key, localPath string, _ Digest) (PutOutcome, error) {
	path, err := b.pathFor(key)
	if err != nil {
		return PutAmbiguous, err
	}
	source, err := os.Open(localPath)
	if err != nil {
		return PutAmbiguous, wrapError(CategoryBackend, err, "durable: cannot read %s", localPath)
	}
	defer source.Close()
	staged, err := b.stage(func(f *os.File) error {
		_, err := io.Copy(f, source)
		return err
	})
	if err != nil {
		return PutAmbiguous, err
	}
	defer os.Remove(staged)
	return b.publish(staged, path, key)
}

// ReplaceIfMatch implements Backend.
func (b *LocalBackend) ReplaceIfMatch(_ context.Context, key string, data []byte, etag string) (ReplaceOutcome, error) {
	if key != HeadKey {
		// Every other key in the protocol is immutable, so a conditional
		// replace against one is a bug rather than a race. Reporting it as a
		// failed compare-and-swap would send the caller into a reconcile that
		// can never settle.
		return ReplaceOutcome{}, newError(CategoryBackend,
			"durable: %s is immutable; only %s can be replaced", key, HeadKey)
	}
	if _, err := b.pathFor(key); err != nil {
		return ReplaceOutcome{}, err
	}
	if err := os.MkdirAll(filepath.Join(b.root, versionsDir), 0o700); err != nil {
		return ReplaceOutcome{}, wrapError(CategoryBackend, err, "durable: cannot create the version chain")
	}

	next, err := b.nextVersionFor(etag)
	if err != nil {
		return ReplaceOutcome{}, err
	}
	if next == 0 {
		return ReplaceOutcome{Status: ReplaceNotMatched}, nil
	}

	staged, err := b.stage(func(f *os.File) error {
		_, err := f.Write(data)
		return err
	})
	if err != nil {
		return ReplaceOutcome{}, err
	}
	defer os.Remove(staged)

	target := filepath.Join(b.root, versionsDir, strconv.FormatInt(next, 10)+".json")
	outcome, err := b.publish(staged, target, key)
	if err != nil {
		return ReplaceOutcome{}, err
	}
	if outcome != PutCreated {
		// Somebody else created this version. That is the compare-and-swap
		// losing, told by the filesystem rather than guessed at.
		return ReplaceOutcome{Status: ReplaceNotMatched}, nil
	}

	if err := b.pointHeadAt(next); err != nil {
		// The version exists and is durable; only the pointer is behind. A
		// reader still sees the previous head, and another writer holding the
		// previous ETag will fail to create this same version — so the
		// compare-and-swap has been won but not published, which is exactly
		// what ambiguous means.
		return ReplaceOutcome{Status: ReplaceAmbiguous}, nil
	}
	return ReplaceOutcome{Status: ReplaceDone, ETag: versionETag(next)}, nil
}

func versionETag(version int64) string {
	return "v" + strconv.FormatInt(version, 10)
}

func digestETag(digest Digest) string {
	return "sha256:" + digest.SHA256
}

// currentVersion reports which version the head symlink names, if it is a
// chain at all.
func (b *LocalBackend) currentVersion() (int64, bool, error) {
	target, err := os.Readlink(filepath.Join(b.root, HeadKey))
	if err != nil {
		// Not a symlink, or not there: either way this is not a chain, and the
		// caller falls back to reading a plain file.
		return 0, false, nil
	}
	m := versionTarget.FindStringSubmatch(filepath.ToSlash(target))
	if m == nil {
		return 0, false, nil
	}
	version, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, false, nil
	}
	return version, true, nil
}

// nextVersionFor decides which version a replace against etag would create,
// or 0 when the token no longer describes the stored head.
func (b *LocalBackend) nextVersionFor(etag string) (int64, error) {
	current, isChain, err := b.currentVersion()
	if err != nil {
		return 0, err
	}
	if isChain {
		if etag != versionETag(current) {
			return 0, nil
		}
		return current + 1, nil
	}

	// No chain yet: this is either a cold object created through
	// PutBytesIfAbsent or one another binding wrote. Adopting it into the
	// chain is only sound if the plain file is still exactly what the caller
	// read.
	data, err := os.ReadFile(filepath.Join(b.root, HeadKey))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, wrapError(CategoryBackend, err, "durable: cannot read %s", HeadKey)
	}
	if etag != digestETag(digestOf(data)) {
		return 0, nil
	}
	return 1, nil
}

// pointHeadAt swaps the head symlink to name one version, atomically.
func (b *LocalBackend) pointHeadAt(version int64) error {
	link := filepath.Join(b.root, tmpDir, "head-"+uuid8()+".link")
	if err := os.MkdirAll(filepath.Join(b.root, tmpDir), 0o700); err != nil {
		return err
	}
	relative := versionsDir + "/" + strconv.FormatInt(version, 10) + ".json"
	if err := os.Symlink(relative, link); err != nil {
		return err
	}
	if err := os.Rename(link, filepath.Join(b.root, HeadKey)); err != nil {
		os.Remove(link)
		return err
	}
	return syncDir(b.root)
}

// stage writes content to a unique scratch file, flushed, and returns its
// path. The caller publishes or removes it.
func (b *LocalBackend) stage(write func(*os.File) error) (string, error) {
	dir := filepath.Join(b.root, tmpDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", wrapError(CategoryBackend, err, "durable: cannot create a staging directory")
	}
	path := filepath.Join(dir, uuid8()+".part")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", wrapError(CategoryBackend, err, "durable: cannot stage a write")
	}
	writeErr := write(f)
	if writeErr == nil {
		// Flush before the name appears. Publishing bytes that are only in the
		// page cache would let a crash roll back a write this backend has
		// already reported as done.
		writeErr = f.Sync()
	}
	if closeErr := f.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		os.Remove(path)
		return "", wrapError(CategoryBackend, writeErr, "durable: cannot stage a write")
	}
	return path, nil
}

// publish links a staged file into place, reporting whether the name was free.
func (b *LocalBackend) publish(staged, target, key string) (PutOutcome, error) {
	created, err := ensureDirChain(b.root, filepath.Dir(target))
	if err != nil {
		return PutAmbiguous, wrapError(CategoryBackend, err, "durable: cannot create a directory for %s", key)
	}
	if err := os.Link(staged, target); err != nil {
		if errors.Is(err, os.ErrExist) {
			return PutAlreadyExists, nil
		}
		return PutAmbiguous, wrapError(CategoryBackend, err, "durable: cannot publish %s", key)
	}
	// Flush the whole chain of directories this publish brought into
	// existence, innermost first. Flushing only the immediate parent is
	// worthless when the parent is itself a fresh, unflushed entry above.
	for i := len(created) - 1; i >= 0; i-- {
		if err := syncDir(created[i]); err != nil {
			return PutAmbiguous, wrapError(CategoryBackend, err, "durable: cannot flush %s", created[i])
		}
	}
	if err := syncDir(filepath.Dir(target)); err != nil {
		return PutAmbiguous, wrapError(CategoryBackend, err, "durable: cannot flush the directory of %s", key)
	}
	return PutCreated, nil
}

// ensureDirChain creates dir under root and returns the directories it had to
// create, outermost first.
func ensureDirChain(root, dir string) ([]string, error) {
	var missing []string
	for current := dir; strings.HasPrefix(current, root) && current != root; current = filepath.Dir(current) {
		if _, err := os.Stat(current); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		missing = append([]string{current}, missing...)
	}
	if len(missing) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return missing, nil
}

// syncDir flushes a directory entry, so a name this backend created survives
// power loss.
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		// Some filesystems refuse fsync on a directory. That is not a reason
		// to fail a publish whose data is already flushed; the name may just
		// be less durable than the bytes.
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			return nil
		}
		return fmt.Errorf("fsync %s: %w", dir, err)
	}
	return nil
}
