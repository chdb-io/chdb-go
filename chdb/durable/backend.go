package durable

import (
	"context"
	"io"
)

// The object-storage contract every durable backend must satisfy
// (contract §5.1).
//
// The interface is small on purpose. Only two properties are load-bearing,
// and both are properties a provider either has or does not:
//
//  1. **Real conditional operations.** PutBytesIfAbsent must be an atomic
//     create and ReplaceIfMatch an atomic compare-and-swap. Simulating either
//     with a read followed by a write is not a weaker implementation, it is a
//     broken one: the window between the two is exactly where two writers
//     both conclude they are the only writer.
//  2. **Streaming.** A checkpoint is a full database archive. Requiring it to
//     pass through a []byte puts a ceiling on database size that has nothing
//     to do with the database. So checkpoints move as files and readers; only
//     the head and WAL segments — both bounded by the protocol — move as
//     bytes.
//
// A third property is expressed in the return values rather than the methods:
// every mutating call can answer "ambiguous". A request whose response was
// lost is not a failure, and reporting it as one would make a caller retry a
// commit that already happened. The state machine resolves ambiguity by
// re-reading (contract §5.8), which is only possible if the backend admits it.
//
// Deleting is absent by design: V1 has no destroy and no garbage collection,
// so nothing in the protocol has the authority to remove an object.
//
// Every method takes a context. What it can cancel is honest about the layer
// it sits in: storage transfers and the boundaries between operation phases
// respect it, and a native query already in flight does not — chDB's query API
// has no cancellation to forward it to.

// PutOutcome is the result of a conditional create.
type PutOutcome int

const (
	// PutCreated means the object was created by this call.
	PutCreated PutOutcome = iota
	// PutAlreadyExists means the key was already there. For a key minted per
	// attempt this means *we* created it earlier, on a try whose response
	// never arrived.
	PutAlreadyExists
	// PutAmbiguous means the request may or may not have landed, and the
	// caller has to re-read to find out.
	PutAmbiguous
)

func (o PutOutcome) String() string {
	switch o {
	case PutCreated:
		return "created"
	case PutAlreadyExists:
		return "already-exists"
	case PutAmbiguous:
		return "ambiguous"
	}
	return "unknown"
}

// ReplaceStatus is the result of a conditional replace.
type ReplaceStatus int

const (
	// ReplaceDone means the compare-and-swap succeeded, and ReplaceOutcome
	// carries the new token.
	ReplaceDone ReplaceStatus = iota
	// ReplaceNotMatched means the stored token no longer matched: someone else
	// wrote first.
	ReplaceNotMatched
	// ReplaceAmbiguous means the outcome is unknown.
	ReplaceAmbiguous
)

func (s ReplaceStatus) String() string {
	switch s {
	case ReplaceDone:
		return "replaced"
	case ReplaceNotMatched:
		return "not-replaced"
	case ReplaceAmbiguous:
		return "ambiguous"
	}
	return "unknown"
}

// ReplaceOutcome is what a conditional replace reports. ETag is set only when
// Status is ReplaceDone.
type ReplaceOutcome struct {
	Status ReplaceStatus
	ETag   string
}

// Backend is a key/value store scoped to one object's prefix.
//
// Keys are relative, "/"-separated and validated by IsValidObjectKey: they
// arrive from a head.json that came out of object storage, so they are
// untrusted input, and an implementation that resolves them against a
// directory must refuse a traversal rather than trust the caller.
//
// The "found" return distinguishes an absent key from a failure. It is a
// separate value rather than a sentinel error because absence is a normal,
// expected answer at nearly every call site — a cold object, a probe for a
// head — and a caller that has to unwrap an error to learn something ordinary
// eventually forgets to.
type Backend interface {
	// Describe is a human-readable location of the object prefix, for logs and
	// errors. It must never contain credentials.
	Describe() string

	// GetBytes reads a whole object.
	GetBytes(ctx context.Context, key string) (data []byte, found bool, err error)

	// GetBytesWithETag reads a whole object together with its CAS token. The
	// two must describe the same version: pairing an old body with a new token
	// would let a compare-and-swap succeed against state nobody read.
	GetBytesWithETag(ctx context.Context, key string) (data []byte, etag string, found bool, err error)

	// OpenReader opens a byte stream for a potentially large object, so a
	// checkpoint download never has to be resident. The caller closes it.
	OpenReader(ctx context.Context, key string) (r io.ReadCloser, found bool, err error)

	// PutBytesIfAbsent atomically creates an object from bytes. It never
	// overwrites.
	PutBytesIfAbsent(ctx context.Context, key string, data []byte) (PutOutcome, error)

	// PutFileIfAbsent atomically creates an object by uploading a local file.
	// It never overwrites.
	//
	// digest is the file's already-computed length and SHA-256 — the protocol
	// requires both to be known before publishing, so they are passed rather
	// than recomputed. A backend may use them to sign or verify the upload
	// without a second pass over the file.
	PutFileIfAbsent(ctx context.Context, key, localPath string, digest Digest) (PutOutcome, error)

	// ReplaceIfMatch atomically replaces an object only if its stored token
	// still equals etag.
	ReplaceIfMatch(ctx context.Context, key string, data []byte, etag string) (ReplaceOutcome, error)
}

// BackendFactory builds a backend bound to one object's prefix.
//
// A namespace owns the factory and hands each object its own scoped backend,
// so no code below the namespace can address a key outside its object.
type BackendFactory func(ctx context.Context, objectID string) (Backend, error)
