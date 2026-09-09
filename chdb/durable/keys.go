package durable

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// Object key construction and validation (contract §4.1).
//
// Two separate jobs live here, and they are separate on purpose:
//
//   - *Minting* a key for something this writer is about to publish. Every
//     attempt gets a fresh key, including a retry of an attempt that may
//     already have landed. That is what makes an ambiguous upload resolvable
//     rather than destructive: a retry can never overwrite the bytes the
//     first try published (contract §5.8).
//   - *Validating* a key read out of someone else's head. A reference is a
//     relative key inside the object prefix and nothing else. Rejecting "..",
//     absolute paths and empty segments here is what stops a hostile or
//     broken head from steering a download outside the object — the local
//     backend resolves keys against a directory, so a traversal would be a
//     real escape.

// HeadKey is the one mutable key in an object.
const HeadKey = "head.json"

// uuid8 returns the first 8 hex digits of a random UUID4, per the frozen key
// shape. It reads from crypto/rand because two writers minting the same
// suffix in the same generation and sequence would collide on a key that must
// be unique per attempt.
func uuid8() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on any platform this package builds for;
		// if it ever did, a predictable key is worse than a stopped process.
		panic("durable: crypto/rand is unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// checkpointKey mints checkpoints/<generation>-<seq>-<uuid8>.tar.gz.
func checkpointKey(generation, seq int64) string {
	return fmt.Sprintf("checkpoints/%d-%d-%s.tar.gz", generation, seq, uuid8())
}

// walKey mints wal/<generation>-<seq>-<uuid8>.jsonl.
func walKey(generation, seq int64) string {
	return fmt.Sprintf("wal/%d-%d-%s.jsonl", generation, seq, uuid8())
}

// IsValidObjectKey accepts only a relative, "/"-separated key with no empty,
// "." or ".." segments.
//
// Backslashes are rejected too: on a POSIX filesystem a backslash is an
// ordinary character, so a key holding one would name a different file here
// than it does on a provider that normalises it — and an object is supposed to
// mean the same thing wherever it is opened.
func IsValidObjectKey(key string) bool {
	if key == "" || strings.HasPrefix(key, "/") {
		return false
	}
	if strings.ContainsAny(key, "\\\x00") {
		return false
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

// validateObjectID accepts an object id that is a single path segment, so an
// id cannot climb out of its namespace or contain another id's prefix.
func validateObjectID(id string) error {
	if id == "" || strings.ContainsAny(id, "/\\\x00") || id == "." || id == ".." {
		return newError(CategoryBackend,
			"durable: object id must be a single non-empty path segment, got %q", id)
	}
	return nil
}
