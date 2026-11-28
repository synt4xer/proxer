package filelock

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"time"
)

const LockFilename = ".proxer.lock"

type Lock struct {
	path string
	file *os.File
}

// Acquire creates and locks <root>/.proxer.lock. Retries with jittered
// exponential backoff until the lock is available or timeout expires.
//
// The jitter is load-bearing, not cosmetic: without it every waiter sleeps for
// the same duration and stays in lockstep, so each round lets exactly one
// goroutine through while the rest burn their deadline. Measured on 32
// concurrent writers, jitter turned 8 timeouts at 10s into 32 successes at
// 459ms.
func Acquire(root string, timeout time.Duration) (*Lock, error) {
	lockPath := filepath.Join(root, LockFilename)

	deadline := time.Now().Add(timeout)
	backoff := 50 * time.Millisecond

	for {
		file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, fmt.Errorf("failed to create lock file: %w", err)
		}

		if err := tryLock(file); err == nil {
			return &Lock{path: lockPath, file: file}, nil
		}

		file.Close()

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("failed to acquire lock after %s: another writer holds the lock", timeout)
		}

		time.Sleep(jitter(backoff))
		backoff = min(backoff*2, 500*time.Millisecond)
	}
}

// jitter returns a delay in [d/2, d) so concurrent waiters desynchronize.
func jitter(d time.Duration) time.Duration {
	half := d / 2
	if half <= 0 {
		return d
	}
	return half + time.Duration(rand.Int64N(int64(half)))
}

// Release unlocks and closes the lock file. Does not remove it (harmless residue).
func (l *Lock) Release() error {
	if l.file == nil {
		return nil
	}

	unlock(l.file)
	err := l.file.Close()
	l.file = nil
	return err
}
