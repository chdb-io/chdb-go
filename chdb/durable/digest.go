package durable

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"os"
)

// Length and SHA-256 verification (contract §4.5).
//
// The contract requires both to be checked before a base is restored or a WAL
// segment is parsed, and it requires checkpoint transfer not to hold the whole
// archive in memory. So the streaming helper here does both jobs in one pass:
// it hashes and counts while it writes, and publishes the file to its final
// name only once both match. A caller therefore never sees a scratch path
// holding unverified bytes.

// Digest is the length and content hash of one object.
type Digest struct {
	Size int64
	// SHA256 is the lowercase full hex digest.
	SHA256 string
}

// digestOf hashes a buffer.
func digestOf(data []byte) Digest {
	sum := sha256.Sum256(data)
	return Digest{Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
}

// digestFile streams a local file through SHA-256 without holding it in
// memory.
func digestFile(path string) (Digest, error) {
	f, err := os.Open(path)
	if err != nil {
		return Digest{}, err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return Digest{}, err
	}
	return Digest{Size: size, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

// assertDigest refuses an object that is not the one the head describes.
func assertDigest(ref ObjectRef, observed Digest, what string) error {
	if ref.Size != observed.Size {
		return newError(CategoryCorrupt, "durable: %s (%s) has size %d, head says %d",
			what, ref.Key, observed.Size, ref.Size)
	}
	if ref.SHA256 != observed.SHA256 {
		return newError(CategoryCorrupt, "durable: %s (%s) sha256 %s does not match head %s",
			what, ref.Key, observed.SHA256, ref.SHA256)
	}
	return nil
}

// countingHasher accumulates length and SHA-256 as bytes flow past.
type countingHasher struct {
	h    hash.Hash
	size int64
}

func newCountingHasher() *countingHasher {
	return &countingHasher{h: sha256.New()}
}

func (c *countingHasher) Write(p []byte) (int, error) {
	c.size += int64(len(p))
	return c.h.Write(p)
}

func (c *countingHasher) digest() Digest {
	return Digest{Size: c.size, SHA256: hex.EncodeToString(c.h.Sum(nil))}
}

// drainDigest reads r to the end and returns what went past, without keeping
// it. Used to settle an upload whose response was lost: the question is
// whether the bytes at that key are ours, and the answer is a digest.
func drainDigest(r io.Reader) (Digest, error) {
	c := newCountingHasher()
	if _, err := io.Copy(c, r); err != nil {
		return Digest{}, err
	}
	return c.digest(), nil
}

// streamToVerifiedFile writes src into tmpPath, verifies it against ref, then
// renames it to finalPath.
//
// On any mismatch the scratch file is removed and finalPath is never created,
// so a failed verify cannot leave behind something a later step mistakes for a
// good archive.
func streamToVerifiedFile(src io.Reader, ref ObjectRef, tmpPath, finalPath, what string) error {
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return wrapError(CategoryBackend, err, "durable: cannot stage %s", ref.Key)
	}
	counter := newCountingHasher()
	_, copyErr := io.Copy(io.MultiWriter(f, counter), src)
	syncErr := f.Sync()
	closeErr := f.Close()
	if copyErr == nil {
		copyErr = syncErr
	}
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		os.Remove(tmpPath)
		return wrapError(CategoryBackend, copyErr, "durable: downloading %s failed", ref.Key)
	}
	if err := assertDigest(ref, counter.digest(), what); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return wrapError(CategoryBackend, err, "durable: publishing verified %s failed", ref.Key)
	}
	return nil
}
