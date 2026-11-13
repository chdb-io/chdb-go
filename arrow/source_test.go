//go:build cgo && (darwin || linux)

package chdbarrow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chdb-io/chdb-go/v2/internal/nativearrow"
)

func TestQueryBuilderCanCloseItsSource(t *testing.T) {
	s := &Source{name: "test"}
	done := make(chan error, 1)
	go func() {
		done <- s.run(func(expression string) string {
			if err := s.Close(); err != nil {
				return ""
			}
			return "SELECT count() FROM " + expression
		}, func(nativearrow.Connection, string) error {
			return errors.New("native query must not run after Close")
		})
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrSourceClosed) {
			t.Fatalf("query after close during SQL construction = %v, want ErrSourceClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("query builder deadlocked while closing its Source")
	}
}

func TestSourceContextCancellationBeforeAndAfterNativeCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &Source{name: "test"}
	if err := s.runContext(ctx, func(string) string {
		t.Fatal("builder ran after cancellation")
		return ""
	}, nil); !errors.Is(err, context.Canceled) || s.used {
		t.Fatalf("pre-cancelled source: err=%v used=%v", err, s.used)
	}

	ctx, cancel = context.WithCancel(context.Background())
	s = &Source{name: "test", lease: nativearrow.NewLease(nil, nil), copying: &copyReader{}}
	called := false
	err := s.runContext(ctx, func(source string) string {
		return "SELECT * FROM " + source
	}, func(nativearrow.Connection, string) error {
		called = true
		cancel() // cancellation races with the native call returning
		return nil
	})
	if !called || !errors.Is(err, context.Canceled) {
		t.Fatalf("post-native cancellation: called=%v err=%v", called, err)
	}
}
