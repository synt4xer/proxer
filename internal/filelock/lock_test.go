package filelock

import (
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
