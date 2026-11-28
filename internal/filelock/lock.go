package filelock

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const LockFilename = ".proxer.lock"

type Lock struct {
	path string
	file *os.File
}

// Acquire creates and locks <root>/.proxer.lock. Blocks until lock is available
// or timeout expires. Returns error if timeout or lock fails.
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

		time.Sleep(backoff)
		backoff = min(backoff*2, 500*time.Millisecond)
	}
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
