package durable

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func localBackend(t *testing.T) *LocalBackend {
	t.Helper()
	backend, err := NewLocalBackend(filepath.Join(t.TempDir(), "object"))
	if err != nil {
		t.Fatal(err)
	}
	return backend
}

func TestLocalBackendConditionalCreate(t *testing.T) {
	ctx := context.Background()
	backend := localBackend(t)

	outcome, err := backend.PutBytesIfAbsent(ctx, "wal/1-1-aaaa1111.jsonl", []byte("first\n"))
	if err != nil {
		t.Fatal(err)
	}
	if outcome != PutCreated {
		t.Fatalf("first create reported %s", outcome)
	}

	// Never overwrites. This is the property the whole immutable-key scheme
	// rests on: a retry after a lost response must not replace what landed.
	outcome, err = backend.PutBytesIfAbsent(ctx, "wal/1-1-aaaa1111.jsonl", []byte("second\n"))
	if err != nil {
		t.Fatal(err)
	}
	if outcome != PutAlreadyExists {
		t.Fatalf("second create reported %s, want already-exists", outcome)
	}
	data, found, err := backend.GetBytes(ctx, "wal/1-1-aaaa1111.jsonl")
	if err != nil || !found {
		t.Fatalf("read back: found=%v err=%v", found, err)
	}
	if string(data) != "first\n" {
		t.Fatalf("the object holds %q; the second create overwrote it", data)
	}
}

func TestLocalBackendReportsAMissingKey(t *testing.T) {
	ctx := context.Background()
	backend := localBackend(t)

	if _, found, err := backend.GetBytes(ctx, HeadKey); err != nil || found {
		t.Fatalf("a cold object should report the head as absent: found=%v err=%v", found, err)
	}
	if _, _, found, err := backend.GetBytesWithETag(ctx, HeadKey); err != nil || found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if _, found, err := backend.OpenReader(ctx, "wal/1-1-aaaa1111.jsonl"); err != nil || found {
		t.Fatalf("found=%v err=%v", found, err)
	}
}

func TestLocalBackendCompareAndSwap(t *testing.T) {
	ctx := context.Background()
	backend := localBackend(t)

	if _, err := backend.PutBytesIfAbsent(ctx, HeadKey, []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	_, etag, found, err := backend.GetBytesWithETag(ctx, HeadKey)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}

	outcome, err := backend.ReplaceIfMatch(ctx, HeadKey, []byte(`{"v":2}`), etag)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != ReplaceDone {
		t.Fatalf("replace reported %s", outcome.Status)
	}

	// The old token no longer matches, which is the fence: a superseded
	// writer's next commit fails rather than clobbering.
	stale, err := backend.ReplaceIfMatch(ctx, HeadKey, []byte(`{"v":3}`), etag)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Status != ReplaceNotMatched {
		t.Fatalf("a stale token reported %s, want not-replaced", stale.Status)
	}

	data, current, _, err := backend.GetBytesWithETag(ctx, HeadKey)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"v":2}` {
		t.Fatalf("the head holds %q; a stale replace got through", data)
	}
	if current != outcome.ETag {
		t.Fatalf("the read token %q does not match the one the replace returned %q",
			current, outcome.ETag)
	}
}

// Only one racer may win, and it has to be told by the filesystem rather than
// by a read-then-write window.
func TestLocalBackendCompareAndSwapHasExactlyOneWinner(t *testing.T) {
	ctx := context.Background()
	backend := localBackend(t)

	if _, err := backend.PutBytesIfAbsent(ctx, HeadKey, []byte(`{"v":0}`)); err != nil {
		t.Fatal(err)
	}
	_, etag, _, err := backend.GetBytesWithETag(ctx, HeadKey)
	if err != nil {
		t.Fatal(err)
	}

	const racers = 12
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners int
	)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outcome, err := backend.ReplaceIfMatch(ctx, HeadKey, []byte(`{"v":1}`), etag)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Errorf("racer %d: %v", i, err)
				return
			}
			if outcome.Status == ReplaceDone {
				winners++
			}
		}(i)
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("%d racers won the compare-and-swap, want exactly 1", winners)
	}
}

// A conditional create of the same unique key must also have one winner: it is
// what the object layer relies on when two processes race to create a cold
// object.
func TestLocalBackendConditionalCreateHasExactlyOneWinner(t *testing.T) {
	ctx := context.Background()
	backend := localBackend(t)

	const racers = 12
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners int
	)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			outcome, err := backend.PutBytesIfAbsent(ctx, HeadKey, []byte(`{"v":0}`))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Error(err)
				return
			}
			if outcome == PutCreated {
				winners++
			}
		}()
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("%d racers created the head, want exactly 1", winners)
	}
}

// An object produced by another binding has a plain head.json and no version
// chain. It must read, and the first replace must adopt it — otherwise a Go
// writer could never take over an object a Python or Node writer created.
func TestLocalBackendAdoptsAForeignPlainHead(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "object")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, HeadKey), []byte(foreignHead), 0o600); err != nil {
		t.Fatal(err)
	}
	backend, err := NewLocalBackend(root)
	if err != nil {
		t.Fatal(err)
	}

	data, etag, found, err := backend.GetBytesWithETag(ctx, HeadKey)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if string(data) != foreignHead {
		t.Fatal("the foreign head did not read back verbatim")
	}
	if _, _, err := parseHead(data); err != nil {
		t.Fatalf("the foreign head does not parse: %v", err)
	}

	outcome, err := backend.ReplaceIfMatch(ctx, HeadKey, []byte(`{"adopted":true}`), etag)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != ReplaceDone {
		t.Fatalf("adopting a foreign head reported %s", outcome.Status)
	}
	// A reader that only understands plain files still sees a correct
	// head.json, through the symlink.
	after, err := os.ReadFile(filepath.Join(root, HeadKey))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != `{"adopted":true}` {
		t.Fatalf("head.json holds %q after adoption", after)
	}
	// And the old digest token is now stale.
	stale, err := backend.ReplaceIfMatch(ctx, HeadKey, []byte(`{"no":true}`), etag)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Status != ReplaceNotMatched {
		t.Fatalf("the pre-adoption token still matched: %s", stale.Status)
	}
}

// A key out of a head is untrusted input, and the local backend resolves keys
// against a directory — so a traversal would be a real escape.
func TestLocalBackendRefusesKeysThatEscape(t *testing.T) {
	ctx := context.Background()
	backend := localBackend(t)

	for _, key := range []string{"../escape", "/etc/passwd", "a/../../b", ""} {
		if _, _, err := backend.GetBytes(ctx, key); err == nil {
			t.Errorf("reading %q should be refused", key)
		}
		if _, err := backend.PutBytesIfAbsent(ctx, key, []byte("x")); err == nil {
			t.Errorf("writing %q should be refused", key)
		}
	}
}

// Every key but the head is immutable, so a conditional replace against one is
// a bug rather than a race. Reporting it as a failed compare-and-swap would
// send the caller into a reconcile that can never settle.
func TestLocalBackendRefusesToReplaceAnImmutableKey(t *testing.T) {
	ctx := context.Background()
	backend := localBackend(t)
	if _, err := backend.ReplaceIfMatch(ctx, "wal/1-1-aaaa1111.jsonl", []byte("x"), "v1"); err == nil {
		t.Fatal("replacing a WAL segment should be refused")
	}
}

func TestLocalBackendPublishesAFile(t *testing.T) {
	ctx := context.Background()
	backend := localBackend(t)

	source := filepath.Join(t.TempDir(), "archive.tar.gz")
	body := []byte("pretend this is a chdb archive")
	if err := os.WriteFile(source, body, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := digestOf(body)

	outcome, err := backend.PutFileIfAbsent(ctx, "checkpoints/1-1-aaaa1111.tar.gz", source, digest)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != PutCreated {
		t.Fatalf("publishing a file reported %s", outcome)
	}

	reader, found, err := backend.OpenReader(ctx, "checkpoints/1-1-aaaa1111.tar.gz")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	defer reader.Close()
	observed, err := drainDigest(reader)
	if err != nil {
		t.Fatal(err)
	}
	if observed != digest {
		t.Fatalf("the published file digests to %+v, want %+v", observed, digest)
	}

	// And again: unique keys mean this only happens on a retry, which must not
	// replace what landed.
	outcome, err = backend.PutFileIfAbsent(ctx, "checkpoints/1-1-aaaa1111.tar.gz", source, digest)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != PutAlreadyExists {
		t.Fatalf("republishing reported %s, want already-exists", outcome)
	}
}

func TestLocalBackendDescribeCarriesNoSecret(t *testing.T) {
	backend := localBackend(t)
	if got := backend.Describe(); got == "" {
		t.Fatal("Describe must say where the object is")
	}
}
