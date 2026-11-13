package chdb

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	chdbpurego "github.com/chdb-io/chdb-go/v2/chdb-purego"
	"github.com/chdb-io/chdb-go/v2/internal/nativearrow"
)

type fakeArrowConn struct {
	closed bool
	closes atomic.Int32
}

func (c *fakeArrowConn) Query(string, string) (chdbpurego.ChdbResult, error) { return nil, nil }
func (c *fakeArrowConn) QueryStreaming(string, string) (chdbpurego.ChdbStreamResult, error) {
	return nil, nil
}
func (c *fakeArrowConn) Capabilities() nativearrow.Capabilities      { return nativearrow.Capabilities{} }
func (c *fakeArrowConn) Ready() bool                                 { return !c.closed }
func (c *fakeArrowConn) Close()                                      { c.closed = true; c.closes.Add(1) }
func (c *fakeArrowConn) RegisterStream(string, unsafe.Pointer) error { return nil }
func (c *fakeArrowConn) UnregisterStream(string) error               { return nil }
func (c *fakeArrowConn) QueryBytes(string, string) ([]byte, error)   { return nil, nil }
func (c *fakeArrowConn) QueryArrow(string, unsafe.Pointer, *nativearrow.OutputOptions) error {
	return nil
}
func (c *fakeArrowConn) QueryArrowStream(string, *nativearrow.OutputOptions) (nativearrow.Stream, error) {
	return nil, nil
}
func (c *fakeArrowConn) QueryArrowStreamParams(string, *nativearrow.OutputOptions, []nativearrow.Parameter) (nativearrow.Stream, error) {
	return nil, nil
}

func TestWithNativeArrowKeepsConnectionUntilScopeEnds(t *testing.T) {
	conn := &fakeArrowConn{}
	s := &Session{conn: conn}
	entered := make(chan struct{})
	leave := make(chan struct{})
	completed := make(chan error, 1)
	go func() {
		completed <- s.WithNativeArrow(func(c nativearrow.Connection) error {
			if c != conn {
				return errors.New("wrong connection")
			}
			close(entered)
			<-leave
			if conn.closed {
				return errors.New("connection closed inside Arrow scope")
			}
			return nil
		})
	}()
	<-entered
	var wg sync.WaitGroup
	wg.Add(1)
	closed := make(chan struct{})
	go func() { defer wg.Done(); s.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("Session.Close returned while native Arrow scope was active")
	case <-time.After(20 * time.Millisecond):
	}
	close(leave)
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if !conn.closed {
		t.Fatal("Session.Close did not close native connection")
	}
	if err := s.WithNativeArrow(func(nativearrow.Connection) error { return nil }); err == nil {
		t.Fatal("closed session accepted Arrow operation")
	}
}

func TestLazyArrowLeaseClosesBeforeConnection(t *testing.T) {
	conn := &fakeArrowConn{}
	s := &Session{conn: conn}
	entered := make(chan struct{})
	resume := make(chan struct{})
	var lease *nativearrow.Lease
	var err error
	lease, err = s.AcquireNativeArrow(func() {
		close(entered)
		<-resume // simulate an in-flight Next that Close must wait for
		if conn.closed {
			t.Error("native connection closed before resource")
		}
		lease.Release()
	})
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	<-entered
	if _, err := s.AcquireNativeArrow(func() {}); err == nil {
		t.Fatal("closing session accepted a new lease")
	}
	select {
	case <-closed:
		t.Fatal("Session.Close returned before lazy resource finished")
	default:
	}
	close(resume)
	<-closed
	if !conn.closed {
		t.Fatal("Session.Close did not retire the connection")
	}
	lease.Release() // idempotent after the session-driven closer
}

func TestConcurrentCloseAndCleanupRetireConnectionOnce(t *testing.T) {
	conn := &fakeArrowConn{}
	s := &Session{conn: conn}
	var lease *nativearrow.Lease
	var err error
	lease, err = s.AcquireNativeArrow(func() { lease.Release() })
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.Close() }()
	go func() { defer wg.Done(); s.Cleanup() }()
	wg.Wait()
	if got := conn.closes.Load(); got != 1 {
		t.Fatalf("native connection closed %d times, want 1", got)
	}
}
