//go:build cgo && (darwin || linux)

package chdbarrow

import (
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
