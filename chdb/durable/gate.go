package durable

import (
	"context"
	"sync"
)

// Operation serialization (contract §5.3).
//
// The contract requires an explicit queue and forbids leaning on "the runtime
// happens to serialize this" as the argument. In Go it plainly does not:
// heartbeat runs on its own goroutine, callers are free to use one object from
// several goroutines, and every commit path has a window between a storage
// call and the state update that follows it.
//
// A durable object uses two of these rather than one, and the split is the
// interesting part.
//
//   - The **operation** gate covers a whole logical operation: execute,
//     query, flush, checkpoint, close. Holding it for the duration of a
//     checkpoint is what stops new writes landing in a database that is being
//     archived.
//   - The **head** gate covers a single compare-and-swap against head.json.
//
// If one gate covered both, a checkpoint of a large database would block
// heartbeat for the whole backup and upload, and the writer would fence itself
// out of an object it was in the middle of legitimately checkpointing. With
// the split, heartbeat only ever contends for the head gate, which is held for
// a single conditional write — while the checkpoint's own final commit takes
// that same gate and therefore sees the ETag heartbeat just produced.
//
// A channel rather than a sync.Mutex, because waiting has to be cancellable: a
// caller that passes a context with a deadline should not be stuck behind a
// checkpoint until the deadline is meaningless.

type gate struct {
	slot chan struct{}

	mu sync.Mutex
	// sealed, once set, turns away new entrants with the error it returns.
	// Close uses it: the drain has to happen before the durability barrier,
	// and new callers have to be refused rather than silently queued behind a
	// close they will never come back from.
	sealed func() error
}

func newGate() *gate {
	return &gate{slot: make(chan struct{}, 1)}
}

// acquire waits for exclusive access, honouring ctx and the seal.
//
// The seal is checked twice, and the second check is the one that matters. A
// caller that arrives while the gate is still open waits behind whatever is in
// flight — and close may be what seals it in the meantime. Checking only on
// arrival would let that caller run one more operation against an object whose
// close has already drained the queue and is about to shut the engine down.
func (g *gate) acquire(ctx context.Context) error {
	if err := g.sealedErr(); err != nil {
		return err
	}
	if err := g.acquireSealed(ctx); err != nil {
		return err
	}
	if err := g.sealedErr(); err != nil {
		g.release()
		return err
	}
	return nil
}

func (g *gate) sealedErr() error {
	g.mu.Lock()
	sealed := g.sealed
	g.mu.Unlock()
	if sealed == nil {
		return nil
	}
	return sealed()
}

// acquireSealed waits for exclusive access without consulting the seal. Close
// itself needs this: it seals the gate against everyone else and then still
// has to do its own work in the same serialized position.
func (g *gate) acquireSealed(ctx context.Context) error {
	select {
	case g.slot <- struct{}{}:
		return nil
	case <-ctx.Done():
		return wrapError(CategoryTimeout, ctx.Err(),
			"durable: gave up waiting for the operation queue")
	}
}

func (g *gate) release() {
	select {
	case <-g.slot:
	default:
		// Releasing a gate nobody holds is a bug in this package, not
		// something a caller can cause; not blocking here keeps that bug from
		// turning into a deadlock that hides where it came from.
	}
}

// seal stops new work and waits for what is in flight to finish.
func (g *gate) seal(ctx context.Context, reason func() error) error {
	g.mu.Lock()
	g.sealed = reason
	g.mu.Unlock()
	// Taking the slot is the drain: whoever holds it now finishes first.
	if err := g.acquireSealed(ctx); err != nil {
		return err
	}
	g.release()
	return nil
}
