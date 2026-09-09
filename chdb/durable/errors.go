package durable

import (
	"errors"
	"fmt"
)

// The V1 error model.
//
// The contract freezes a set of error *categories* (§6) and requires them to
// be programmatically distinguishable across bindings. It deliberately does
// not freeze type names, so the category is a string on one error type rather
// than a hierarchy of types — which in Go also means a caller can branch with
// errors.Is against a sentinel:
//
//	if errors.Is(err, durable.ErrLeaseHeld) { ... }
//	switch durable.CategoryOf(err) { case durable.CategoryCorrupt: ... }
//
// Two rules the contract states outright and this file encodes:
//
//  1. A provider precondition failure (HTTP 412 and friends) is a
//     compare-and-set race first. It is resolved against lease and manifest
//     state into lease_held, lease_fenced or a retry — never reported as a
//     plain backend error. CategoryBackend is for failures that really are the
//     provider's: network, auth, quota.
//  2. Messages carry no secret-bearing SQL, no credentials and no unredacted
//     connection parameters. secret_refused in particular says what was
//     refused without echoing the statement that caused it.

// Category is a frozen V1 error category. Cross-binding conformance asserts on
// these exact strings, so they are wire names rather than prose.
type Category string

const (
	// CategoryNotFound is a read-only open of an object that does not exist,
	// or an open that required an existing object.
	CategoryNotFound Category = "not_found"
	// CategoryLeaseHeld is another writer holding an unexpired lease.
	CategoryLeaseHeld Category = "lease_held"
	// CategoryLeaseFenced is this instance no longer owning the generation it
	// was writing under, whether through a takeover or through self-fencing
	// after it could not confirm its lease in time.
	CategoryLeaseFenced Category = "lease_fenced"
	// CategoryEngineIncompatible is an object whose archive format or minimum
	// reader this engine cannot satisfy, or one written by another engine.
	CategoryEngineIncompatible Category = "engine_incompatible"
	// CategoryProtocolUnsupported is a protocol version above this baseline,
	// or a feature name this build does not know.
	CategoryProtocolUnsupported Category = "protocol_unsupported"
	// CategoryCorrupt is a head that fails schema validation, a referenced
	// immutable object that is missing, or one whose length or SHA-256 does
	// not match. Never downgraded into opening an older state.
	CategoryCorrupt Category = "corrupt"
	// CategoryClassificationRefused is core analysis refusing a statement at a
	// public entry point: wrong statement count, wrong class, or a write
	// outside the object's database.
	CategoryClassificationRefused Category = "classification_refused"
	// CategorySecretRefused is a mutation embedding a credential. V1 has
	// nowhere to put it other than the WAL, which outlives the statement, so
	// it is refused outright.
	CategorySecretRefused Category = "secret_refused"
	// CategoryEngine is a core query, backup or restore failure.
	CategoryEngine Category = "engine"
	// CategoryBackend is a provider network, auth or non-conditional failure.
	CategoryBackend Category = "backend"
	// CategoryTimeout is a deadline passing while the operation was provably
	// still uncommitted.
	CategoryTimeout Category = "timeout"
	// CategoryCommitAmbiguous is reconcile being unable to prove whether the
	// remote committed. The one thing it must never do is report success.
	CategoryCommitAmbiguous Category = "commit_ambiguous"
	// CategoryLimitExceeded is SQL, a WAL segment, a head or a provider object
	// over a declared V1 limit.
	CategoryLimitExceeded Category = "limit_exceeded"
	// CategoryClosed is an operation on an object whose close has completed.
	CategoryClosed Category = "closed"
)

// Error is every failure this package reports. Category is what callers branch
// on; the remaining fields are populated only where they mean something, so a
// caller can act on the specifics without parsing the message.
type Error struct {
	// Category is the frozen V1 category.
	Category Category

	// Message describes the failure. Never holds SQL or credentials.
	Message string

	// Err is the underlying cause, if any. Reachable with errors.Unwrap.
	Err error

	// Owner is the visible name recorded on a lease that blocked this
	// operation. Observability only — never an authority check.
	Owner string

	// Key names the object whose commit state could not be settled, for
	// operator triage of a commit_ambiguous.
	Key string

	// Features lists the unrecognised protocol feature names that blocked the
	// open, so the operator learns which build they need.
	Features []string

	// Expected and Actual carry the two sides of a compatibility refusal: what
	// the object demands and what this process offers.
	Expected string
	Actual   string

	// Limit and Observed carry the two sides of a limit refusal, in bytes.
	Limit    int64
	Observed int64
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Err)
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.Err }

// Is matches by category, which is what makes the sentinels below work with
// errors.Is: a target carrying only a category matches any error of that
// category, while a fully populated target still compares as itself.
func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	if !ok {
		return false
	}
	return other.Message == "" && other.Category == e.Category
}

// Sentinels for errors.Is. Each one carries a category and nothing else, so it
// matches any error of that category.
var (
	ErrNotFound              = &Error{Category: CategoryNotFound}
	ErrLeaseHeld             = &Error{Category: CategoryLeaseHeld}
	ErrLeaseFenced           = &Error{Category: CategoryLeaseFenced}
	ErrEngineIncompatible    = &Error{Category: CategoryEngineIncompatible}
	ErrProtocolUnsupported   = &Error{Category: CategoryProtocolUnsupported}
	ErrCorrupt               = &Error{Category: CategoryCorrupt}
	ErrClassificationRefused = &Error{Category: CategoryClassificationRefused}
	ErrSecretRefused         = &Error{Category: CategorySecretRefused}
	ErrEngine                = &Error{Category: CategoryEngine}
	ErrBackend               = &Error{Category: CategoryBackend}
	ErrTimeout               = &Error{Category: CategoryTimeout}
	ErrCommitAmbiguous       = &Error{Category: CategoryCommitAmbiguous}
	ErrLimitExceeded         = &Error{Category: CategoryLimitExceeded}
	ErrClosed                = &Error{Category: CategoryClosed}
)

// CategoryOf returns the V1 category of err, or "" if err is not one of this
// package's errors. It unwraps, so a category survives being wrapped with
// fmt.Errorf("%w").
func CategoryOf(err error) Category {
	var e *Error
	if errors.As(err, &e) {
		return e.Category
	}
	return ""
}

// newError builds an error of one category with a formatted message.
func newError(category Category, format string, args ...any) *Error {
	return &Error{Category: category, Message: fmt.Sprintf(format, args...)}
}

// wrapError is newError with a cause attached.
func wrapError(category Category, cause error, format string, args ...any) *Error {
	return &Error{Category: category, Message: fmt.Sprintf(format, args...), Err: cause}
}
