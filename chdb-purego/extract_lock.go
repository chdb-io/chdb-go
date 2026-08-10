package chdbpurego

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// lockWait bounds how long a process waits for another one's extraction. An
// extraction writes a few hundred megabytes and fsyncs them, so seconds are
// normal and a slow disk can take longer; two minutes is far past that. The
// bound exists for the case the lock holder never finishes — a stopped or
// wedged process would otherwise hang every other program's startup, and
// waiting forever for a cooperative optimisation is worse than doing the work
// twice.
const lockWait = 2 * time.Minute

// lockPollInterval is short enough that the common case — a waiter picking up
// a copy another process just published — costs no perceptible delay.
const lockPollInterval = 20 * time.Millisecond

// lockExtraction takes an exclusive advisory lock covering the extraction of
// one payload into one cache root, and returns the function that releases it.
//
// It returns nil when the lock could not be taken, which callers must treat as
// permission to continue rather than as an error: the lock only saves work, it
// is not what makes concurrent extraction correct. Reasons it can fail include
// a filesystem without flock support and a holder that is not making progress.
//
// The lock file is named after the digest, so two binaries carrying different
// engine versions never wait on each other. It is deliberately never removed:
// unlinking it while another process holds the same file open would let a later
// process create a fresh file and lock that instead, which is precisely the
// mutual exclusion this is for.
func lockExtraction(root, digest string) func() {
	f, err := os.OpenFile(filepath.Join(root, "."+digest+".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil
	}

	deadline := time.Now().Add(lockWait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case err == nil:
			return func() {
				// Closing the descriptor releases the lock on its own; the
				// explicit unlock keeps that from depending on it.
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}
		case errors.Is(err, syscall.EINTR):
			// A signal, not a refusal. Try again without spending the deadline.
			continue
		case !errors.Is(err, syscall.EWOULDBLOCK):
			// Anything else means locking is unavailable here rather than
			// contended, so waiting cannot help.
			_ = f.Close()
			return nil
		case time.Now().After(deadline):
			_ = f.Close()
			return nil
		}
		time.Sleep(lockPollInterval)
	}
}
