package durable

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// Lease acquisition, cold creation and release (contract §5.2, §5.7).
//
// These run before an Object exists, which is why they are functions rather
// than methods: an open that fails while taking the lease has nothing to hang
// state on, and the unwind has to work anyway.

// newInstanceID identifies one live instance of a writer.
//
// The owner name is for humans and may repeat — two replicas of the same
// deployment, a restarted process with the same name. The instance is what the
// fence compares, so it has to be unique per live handle, and it comes from
// crypto/rand for the same reason a key suffix does.
func newInstanceID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("durable: crypto/rand is unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// nowSeconds is the current time as epoch seconds, the form a lease records.
func nowSeconds() float64 {
	return float64(time.Now().UnixMicro()) / 1e6
}

type leaseParams struct {
	instance string
	owner    string
	tuning   Tuning
	force    bool
}

type coldParams struct {
	id       string
	database string
	running  runningEngine
	instance string
	owner    string
	tuning   Tuning
}

// createCold atomically creates a cold object and takes generation 1 in the
// same write.
func createCold(ctx context.Context, backend Backend, params coldParams) (*headSnapshot, error) {
	head := coldHead(params.database, params.running.version, params.running.backupFormat)
	head.Lease = Lease{
		Generation: 1,
		Owner:      stringPtr(params.owner),
		Instance:   stringPtr(params.instance),
		ExpiresAt:  floatPtr(nowSeconds() + params.tuning.LeaseTTL.Seconds()),
	}
	data, err := serializeHead(head, nil)
	if err != nil {
		return nil, err
	}
	outcome, err := backend.PutBytesIfAbsent(ctx, HeadKey, data)
	if err != nil {
		return nil, err
	}

	// Read back on every path, including success. The ETag of what was created
	// is what the next commit has to present, and a conditional create does
	// not always report one.
	fresh, found, readErr := readHead(ctx, backend)

	if outcome == PutCreated {
		if readErr != nil {
			return nil, readErr
		}
		if !found {
			return nil, newError(CategoryCorrupt,
				"durable: created %s at %s but it cannot be read back", HeadKey, backend.Describe())
		}
		return &fresh, nil
	}

	// Lost the race, or the response was lost. Either way the answer is in the
	// object: if the head that is there names this instance, the create
	// landed.
	if readErr != nil {
		return nil, readErr
	}
	if !found {
		return nil, &Error{
			Category: CategoryCommitAmbiguous,
			Message: fmt.Sprintf("durable: could not determine whether object %s was created at %s",
				params.id, backend.Describe()),
			Key: HeadKey,
		}
	}
	if fresh.head.Lease.instanceIs(params.instance) {
		return &fresh, nil
	}

	// Someone else created it. It is now an existing object, so it goes
	// through the same gates any existing object would.
	if err := assertReadable(fresh.head); err != nil {
		return nil, err
	}
	if err := assertEngineCompatible(fresh.head, params.running); err != nil {
		return nil, err
	}
	if err := assertWritable(fresh.head); err != nil {
		return nil, err
	}
	return acquireLease(ctx, backend, fresh, leaseParams{
		instance: params.instance, owner: params.owner, tuning: params.tuning, force: false,
	})
}

// acquireLease takes the writer lease by compare-and-swap.
//
// An unheld lease is free. A held one is only takeable once its recorded expiry
// is behind us by more than the clock-skew allowance — the allowance is there
// because the two writers' clocks are not the same clock, and a lease that
// looks expired by a second might not be. Taking a lease that has not expired
// is possible, but only as an explicit force, never as a retry.
func acquireLease(ctx context.Context, backend Backend, start headSnapshot, params leaseParams) (*headSnapshot, error) {
	current := start
	deadline := time.Now().Add(params.tuning.CommitDeadline)

	for attempt := 1; ; attempt++ {
		lease := current.head.Lease
		if lease.held() && !params.force {
			var expiresAt float64
			if lease.ExpiresAt != nil {
				expiresAt = *lease.ExpiresAt
			}
			takeableAt := expiresAt + params.tuning.ClockSkewAllowance.Seconds()
			if nowSeconds() < takeableAt {
				return nil, &Error{
					Category: CategoryLeaseHeld,
					Message: fmt.Sprintf("durable: %q holds the writer lease (generation %d)",
						*lease.Owner, lease.Generation),
					Owner: *lease.Owner,
				}
			}
		}

		candidate := current.head.clone()
		candidate.Lease = Lease{
			Generation: lease.Generation + 1,
			Owner:      stringPtr(params.owner),
			Instance:   stringPtr(params.instance),
			ExpiresAt:  floatPtr(nowSeconds() + params.tuning.LeaseTTL.Seconds()),
		}
		data, err := serializeHead(candidate, current.raw)
		if err != nil {
			return nil, err
		}
		outcome, err := backend.ReplaceIfMatch(ctx, HeadKey, data, current.etag)
		if err != nil {
			return nil, err
		}
		if outcome.Status == ReplaceDone {
			return &headSnapshot{head: candidate, etag: outcome.ETag, raw: current.raw}, nil
		}

		fresh, found, err := readHead(ctx, backend)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, newError(CategoryCorrupt,
				"durable: %s disappeared from %s while taking the lease", HeadKey, backend.Describe())
		}
		if fresh.head.Lease.instanceIs(params.instance) {
			// The write landed and its response was lost.
			return &fresh, nil
		}

		if attempt >= params.tuning.MaxCommitAttempts || !time.Now().Before(deadline) {
			held := &Error{
				Category: CategoryLeaseHeld,
				Message: fmt.Sprintf("durable: could not take the writer lease after %d attempts; "+
					"generation is now %d", attempt, fresh.head.Lease.Generation),
			}
			if fresh.head.Lease.Owner != nil {
				held.Owner = *fresh.head.Lease.Owner
			}
			return nil, held
		}
		current = fresh
	}
}

// releaseLease gives the lease back, best effort, when an open failed
// part-way through.
//
// A failure after the lease was taken must not strand it until the TTL
// expires, but the release has to be conditional on still owning it: a head
// that has moved on belongs to someone else, and writing over it would undo
// their acquisition.
func releaseLease(ctx context.Context, backend Backend, instance string) error {
	fresh, found, err := readHead(ctx, backend)
	if err != nil {
		return err
	}
	if !found || !fresh.head.Lease.instanceIs(instance) {
		return nil
	}
	candidate := fresh.head.clone()
	candidate.Lease = Lease{Generation: fresh.head.Lease.Generation}
	data, err := serializeHead(candidate, fresh.raw)
	if err != nil {
		return err
	}
	_, err = backend.ReplaceIfMatch(ctx, HeadKey, data, fresh.etag)
	return err
}
