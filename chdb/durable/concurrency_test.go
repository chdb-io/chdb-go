package durable

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// The contract forbids leaning on "the runtime serializes this" as the
// serialization argument (§5.3), and in Go it plainly does not: heartbeat runs
// on its own goroutine and a caller may use one object from several. These
// tests are what the race detector runs over.

// Concurrent writers on one handle must not lose a statement or interleave one
// into the wrong segment.
func TestConcurrentExecuteAndFlushLoseNothing(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	obj, _, _ := f.mustOpen(OpenOptions{})

	const writers, perWriter = 8, 10
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				ticket, err := obj.Execute(ctx, fmt.Sprintf("INSERT INTO events VALUES (%d, %d)", w, i))
				if err != nil {
					t.Errorf("writer %d statement %d: %v", w, i, err)
					return
				}
				// Every other write asks for a durability barrier, so flushes
				// and executes contend rather than taking turns.
				if i%2 == 0 {
					if err := obj.FlushThrough(ctx, ticket); err != nil {
						t.Errorf("writer %d barrier %d: %v", w, i, err)
						return
					}
				}
			}
		}(w)
	}
	wg.Wait()

	if err := obj.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Recover and count: every statement that Execute accepted has to be
	// there, because close flushes what was still buffered.
	reopened, engine, _ := f.mustOpen(OpenOptions{})
	defer reopened.Close(ctx)
	if got := len(engine.statements()); got != writers*perWriter {
		t.Fatalf("recovery produced %d statements, want %d", got, writers*perWriter)
	}

	// And the manifest's sequence numbers are contiguous, so no two commits
	// claimed the same one.
	head := f.head()
	if head.Manifest.Seq != int64(len(head.Manifest.WAL)) {
		t.Fatalf("seq is %d with %d segments; a commit reused a sequence number",
			head.Manifest.Seq, len(head.Manifest.WAL))
	}
}

// A statement racing a close must come back as either done or refused, never
// as a write against an engine that is being shut down.
func TestExecuteRacingCloseIsEitherDoneOrRefused(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	obj, _, _ := f.mustOpen(OpenOptions{})

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		refused int
		done    int
	)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := obj.Execute(ctx, fmt.Sprintf("INSERT INTO events VALUES (%d)", i))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				done++
			case errors.Is(err, ErrClosed):
				refused++
			default:
				t.Errorf("statement %d reported %q, want success or closed: %v",
					i, CategoryOf(err), err)
			}
		}(i)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := obj.Close(ctx); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	wg.Wait()

	if done+refused != 16 {
		t.Fatalf("%d done and %d refused out of 16", done, refused)
	}
	// Whatever Execute accepted, the close flushed: a close that drained the
	// queue and then dropped the buffer would be the quiet version of this
	// bug.
	if got := obj.Stats().CommittedStatements; got != int64(done) {
		t.Fatalf("close committed %d of the %d accepted statements", got, done)
	}
}

// A writer idle for longer than its TTL is still the writer, because
// heartbeat has been renewing in the background — and a renewal moves neither
// the generation nor the sequence number, so it leaves no trace in the
// manifest.
func TestHeartbeatKeepsTheLeaseThroughAnIdleSpell(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	// Heartbeat often enough to land several times during the pause below,
	// with a TTL short enough that the pause would outlast it unaided.
	obj, _, _ := f.mustOpen(OpenOptions{Tuning: Tuning{
		LeaseTTL:           900 * time.Millisecond,
		HeartbeatInterval:  100 * time.Millisecond,
		ClockSkewAllowance: 50 * time.Millisecond,
		CommitDeadline:     2 * time.Second,
		MaxCommitAttempts:  3,
	}})
	defer obj.Close(ctx)

	mustExecute(t, obj, "INSERT INTO events VALUES (1)")

	before := f.head().Lease.ExpiresAt
	time.Sleep(1200 * time.Millisecond)

	if _, err := obj.Checkpoint(ctx); err != nil {
		t.Fatalf("a checkpoint after a pause longer than the TTL should still commit: %v", err)
	}
	after := f.head().Lease.ExpiresAt
	if before == nil || after == nil {
		t.Fatal("the lease should be held throughout")
	}
	if *after <= *before {
		t.Fatalf("the lease was not renewed: %v then %v", *before, *after)
	}
	// The generation never moved, so nothing took the object over.
	if got := f.head().Lease.Generation; got != 1 {
		t.Fatalf("generation is %d; a heartbeat must not increment it", got)
	}
	// And seq moved only for the checkpoint, not for the heartbeats.
	if got := f.head().Manifest.Seq; got != 1 {
		t.Fatalf("seq is %d, want 1 — heartbeat must not advance it", got)
	}
}

// A writer that cannot confirm its lease stops writing at the moment its
// locally believed window lapses, rather than on the next error.
func TestAWriterThatCannotRenewFencesItself(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	obj, _, _ := f.mustOpen(OpenOptions{Tuning: Tuning{
		LeaseTTL:           300 * time.Millisecond,
		HeartbeatInterval:  90 * time.Millisecond,
		ClockSkewAllowance: 50 * time.Millisecond,
		CommitDeadline:     200 * time.Millisecond,
		MaxCommitAttempts:  2,
	}})

	// Every renewal from here on fails, the way a partitioned writer's would.
	f.backend.mu.Lock()
	f.backend.onReplace = func(key string, _ int) (ReplaceOutcome, error, bool) {
		if key == HeadKey {
			return ReplaceOutcome{}, newError(CategoryBackend, "durable: pretend the network is gone"), true
		}
		return ReplaceOutcome{}, nil, false
	}
	f.backend.mu.Unlock()

	deadline := time.Now().Add(3 * time.Second)
	for {
		_, err := obj.Execute(ctx, "INSERT INTO events VALUES (1)")
		if errors.Is(err, ErrLeaseFenced) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the writer never fenced itself; last error was %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !obj.IsFenced() {
		t.Fatal("a self-fenced writer should say so")
	}
	// It stays unusable, and close still gives back the local resources.
	if _, err := obj.Flush(ctx); !errors.Is(err, ErrLeaseFenced) {
		t.Fatalf("a fenced writer reported %q", CategoryOf(err))
	}
	f.backend.mu.Lock()
	f.backend.onReplace = nil
	f.backend.mu.Unlock()
	if err := obj.Close(ctx); err != nil {
		t.Fatalf("close after self-fencing: %v", err)
	}
}

// Concurrent readers of the same snapshot must see a consistent one, which is
// why Stats is taken as a whole rather than field by field.
func TestStatsUnderConcurrentWriters(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	obj, _, _ := f.mustOpen(OpenOptions{})
	defer obj.Close(ctx)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				stats := obj.Stats()
				if stats.CommittedStatements > stats.ExecutedStatements {
					t.Errorf("stats describe a state that never existed: committed %d of %d executed",
						stats.CommittedStatements, stats.ExecutedStatements)
					return
				}
			}
		}
	}()

	for i := 0; i < 20; i++ {
		mustExecute(t, obj, fmt.Sprintf("INSERT INTO events VALUES (%d)", i))
		if i%5 == 0 {
			if _, err := obj.Flush(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	close(stop)
	wg.Wait()
}

// Two processes reaching a cold object at the same moment must not both come
// away believing they created it. The conditional create decides, and the
// loser sees an object with a live lease rather than one it may write to.
func TestConcurrentColdCreatesLeaveOneWriter(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	const racers = 6
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners []*Object
		refused int
	)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			obj, _, _, err := f.open(OpenOptions{Owner: fmt.Sprintf("worker-%d", i)})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				winners = append(winners, obj)
			case errors.Is(err, ErrLeaseHeld):
				refused++
			default:
				t.Errorf("racer %d reported %q, want success or lease_held: %v",
					i, CategoryOf(err), err)
			}
		}(i)
	}
	wg.Wait()

	if len(winners) != 1 {
		t.Fatalf("%d racers took the object, want exactly 1", len(winners))
	}
	if refused != racers-1 {
		t.Fatalf("%d racers were refused, want %d", refused, racers-1)
	}
	// And the object the winner holds is the one in the store.
	winner := winners[0]
	head := f.head()
	if head.Lease.Owner == nil || *head.Lease.Owner != winner.Stats().Owner {
		t.Fatalf("the head names %v, the winner is %q", head.Lease.Owner, winner.Stats().Owner)
	}
	if err := winner.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
}
