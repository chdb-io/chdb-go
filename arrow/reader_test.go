//go:build cgo && (darwin || linux)

package chdbarrow

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/chdb-io/chdb-go/v2/internal/nativearrow"
)

type blockingStream struct {
	entered   chan struct{}
	resume    chan struct{}
	cancelled atomic.Bool
	closed    atomic.Bool
}

func (s *blockingStream) Fetch(unsafe.Pointer) error {
	close(s.entered)
	<-s.resume
	return errors.New("synthetic fetch failure")
}
func (s *blockingStream) Cancel() { s.cancelled.Store(true) }
func (s *blockingStream) Close()  { s.closed.Store(true) }

func TestCloseWaitsForFetchBeforeCancelAndLeaseRelease(t *testing.T) {
	s := &blockingStream{entered: make(chan struct{}), resume: make(chan struct{})}
	released := make(chan struct{})
	r := &ResultReader{
		ctx: context.Background(), stream: s,
		lease: nativearrow.NewLease(nil, func() { close(released) }),
	}
	nextDone := make(chan bool, 1)
	go func() { nextDone <- r.Next() }()
	<-s.entered
	closeDone := make(chan struct{})
	go func() { _ = r.Close(); close(closeDone) }()
	select {
	case <-closeDone:
		t.Fatal("Close returned while fetch remained in flight")
	case <-time.After(20 * time.Millisecond):
	}
	if s.cancelled.Load() || s.closed.Load() {
		t.Fatal("native stream was cancelled or destroyed during fetch")
	}
	close(s.resume)
	if <-nextDone {
		t.Fatal("failed fetch produced a batch")
	}
	<-closeDone
	<-released
	if !s.cancelled.Load() || !s.closed.Load() || r.Err() == nil {
		t.Fatal("fetch failure did not cancel, close and release the reader")
	}
}
