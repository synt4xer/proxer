package filelock

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAcquire(t *testing.T) {
	root := t.TempDir()

	lock, err := Acquire(root, 5*time.Second)
	require.NoError(t, err)
	require.NotNil(t, lock)
	defer lock.Release()

	lockPath := filepath.Join(root, LockFilename)
	assert.FileExists(t, lockPath)
}

func TestAcquireBlocks(t *testing.T) {
	root := t.TempDir()

	lock1, err := Acquire(root, 5*time.Second)
	require.NoError(t, err)
	defer lock1.Release()

	done := make(chan error, 1)
	go func() {
		_, err := Acquire(root, 200*time.Millisecond)
		done <- err
	}()

	select {
	case err := <-done:
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to acquire lock")
	case <-time.After(1 * time.Second):
		t.Fatal("second acquire should timeout")
	}
}

func TestAcquireAfterRelease(t *testing.T) {
	root := t.TempDir()

	lock1, err := Acquire(root, 5*time.Second)
	require.NoError(t, err)
	require.NoError(t, lock1.Release())

	lock2, err := Acquire(root, 5*time.Second)
	require.NoError(t, err)
	defer lock2.Release()
}

func TestConcurrentAcquire(t *testing.T) {
	root := t.TempDir()

	const workers = 5
	var wg sync.WaitGroup
	acquired := make(chan int, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			lock, err := Acquire(root, 2*time.Second)
			if err != nil {
				return
			}
			acquired <- id
			time.Sleep(50 * time.Millisecond)
			lock.Release()
		}(i)
	}

	wg.Wait()
	close(acquired)

	count := 0
	for range acquired {
		count++
	}

	assert.Greater(t, count, 0, "at least one goroutine should acquire lock")
}

// TestAcquireContentionNoStarvation is the regression test for the lockstep
// bug: fixed (unjittered) backoff made every waiter sleep for identical
// durations, so under 32+ writers most of them never got a turn and hit the
// timeout. Jitter must keep every waiter inside the deadline.
func TestAcquireContentionNoStarvation(t *testing.T) {
	root := t.TempDir()

	const workers = 32
	var wg sync.WaitGroup
	var mu sync.Mutex
	acquired := 0

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lock, err := Acquire(root, 10*time.Second)
			if err != nil {
				return
			}
			mu.Lock()
			acquired++
			mu.Unlock()
			time.Sleep(2 * time.Millisecond)
			lock.Release()
		}()
	}
	wg.Wait()

	assert.Equal(t, workers, acquired, "jitter must let every waiter acquire the lock before the deadline")
}

func TestJitterSpreadsDelays(t *testing.T) {
	const d = 100 * time.Millisecond
	seen := make(map[time.Duration]struct{}, 64)
	for i := 0; i < 64; i++ {
		j := jitter(d)
		assert.GreaterOrEqual(t, j, d/2, "jitter must stay at or above d/2")
		assert.Less(t, j, d, "jitter must stay below d")
		seen[j] = struct{}{}
	}
	assert.Greater(t, len(seen), 1, "jitter must produce varying delays, not a constant")
}

func TestAcquireFailsWhenRootIsUnwritable(t *testing.T) {
	// A regular file where the root directory should be makes OpenFile fail,
	// exercising the branch that reports a broken lock path.
	missingRoot := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(missingRoot, []byte("x"), 0o600))

	lock, err := Acquire(missingRoot, time.Second)

	require.Error(t, err)
	assert.Nil(t, lock)
	assert.Contains(t, err.Error(), "failed to create lock file")
}

func TestReleaseIsIdempotent(t *testing.T) {
	root := t.TempDir()
	lock, err := Acquire(root, time.Second)
	require.NoError(t, err)

	require.NoError(t, lock.Release())
	assert.NoError(t, lock.Release(), "a second Release must be a no-op")
}
