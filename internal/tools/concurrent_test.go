package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/database"
	"github.com/synt4xer/proxer/internal/domain"
	"github.com/synt4xer/proxer/internal/indexer"
)

func TestConcurrentEdits(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, ".proxer.db")

	db, err := database.Open(dbPath, database.OpenOptions{
		Chunking:       indexer.Chunking(),
		IndexSensitive: false,
	})
	require.NoError(t, err)
	defer db.Close()

	file1 := filepath.Join(root, "file1.go")
	file2 := filepath.Join(root, "file2.go")

	require.NoError(t, os.WriteFile(file1, []byte("package main\n\nfunc foo() {}\n"), 0600))
	require.NoError(t, os.WriteFile(file2, []byte("package main\n\nfunc bar() {}\n"), 0600))

	_, err = indexer.IndexProjectWithOptions(db, root, indexer.IndexOptions{})
	require.NoError(t, err)

	const workers = 5
	var wg sync.WaitGroup
	results := make(chan error, workers*2)

	for i := 0; i < workers; i++ {
		wg.Add(2)

		go func(id int) {
			defer wg.Done()
			time.Sleep(time.Duration(id*10) * time.Millisecond)
			res, err := PerformEdit(db, root, "file1.go", 3, 3, "func foo1() {}", "", nil)
			if err != nil {
				results <- err
			} else if res.Status == "success" {
				results <- nil
			}
		}(i)

		go func(id int) {
			defer wg.Done()
			time.Sleep(time.Duration(id*10) * time.Millisecond)
			res, err := PerformEdit(db, root, "file2.go", 3, 3, "func bar1() {}", "", nil)
			if err != nil {
				results <- err
			} else if res.Status == "success" {
				results <- nil
			}
		}(i)
	}

	wg.Wait()
	close(results)

	successCount := 0
	for err := range results {
		if err == nil {
			successCount++
		}
	}

	assert.Greater(t, successCount, 0, "at least one edit should succeed")

	content1, err := os.ReadFile(file1)
	require.NoError(t, err)
	assert.Contains(t, string(content1), "foo1", "file1 should be edited")

	content2, err := os.ReadFile(file2)
	require.NoError(t, err)
	assert.Contains(t, string(content2), "bar1", "file2 should be edited")
}

func TestEditRetryOnBusy(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, ".proxer.db")

	db, err := database.Open(dbPath, database.OpenOptions{
		Chunking:       indexer.Chunking(),
		IndexSensitive: false,
	})
	require.NoError(t, err)
	defer db.Close()

	testFile := filepath.Join(root, "test.go")
	require.NoError(t, os.WriteFile(testFile, []byte("package main\n\nfunc test() {}\n"), 0600))

	_, err = indexer.IndexProjectWithOptions(db, root, indexer.IndexOptions{})
	require.NoError(t, err)

	done := make(chan domain.EditResultJSON, 2)
	errors := make(chan error, 2)

	go func() {
		res, err := PerformEdit(db, root, "test.go", 3, 3, "func test1() {}", "", nil)
		if err != nil {
			errors <- err
		} else {
			done <- res
		}
	}()

	time.Sleep(10 * time.Millisecond)

	go func() {
		res, err := PerformEdit(db, root, "test.go", 3, 3, "func test2() {}", "", nil)
		if err != nil {
			errors <- err
		} else {
			done <- res
		}
	}()

	successCount := 0
	timeout := time.After(5 * time.Second)

	for successCount < 2 {
		select {
		case res := <-done:
			assert.Equal(t, "success", res.Status)
			successCount++
		case err := <-errors:
			t.Logf("Edit failed: %v", err)
		case <-timeout:
			t.Fatal("timeout waiting for edits")
		}
	}

	content, err := os.ReadFile(testFile)
	require.NoError(t, err)
	assert.True(t,
		string(content) == "package main\n\nfunc test1() {}\n" ||
			string(content) == "package main\n\nfunc test2() {}\n",
		"file should contain one of the edits")
}

// TestConcurrentEditsSameHashGuard proves the lock makes the read-hash-write
// cycle atomic: two writers that both saw the same hash and race on the same
// file must produce exactly one success, not two silent overwrites.
func TestConcurrentEditsSameHashGuard(t *testing.T) {
	root := t.TempDir()
	db, err := database.Open(filepath.Join(root, ".proxer.db"), database.OpenOptions{
		Chunking:       indexer.Chunking(),
		IndexSensitive: false,
	})
	require.NoError(t, err)
	defer db.Close()

	target := filepath.Join(root, "target.go")
	require.NoError(t, os.WriteFile(target, []byte("package main\n\nfunc target() {}\n"), 0600))
	_, err = indexer.IndexProjectWithOptions(db, root, indexer.IndexOptions{})
	require.NoError(t, err)

	read, err := PerformRead(root, "target.go", 1, 3)
	require.NoError(t, err)
	staleHash := read.Hash

	const writers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes, rejections := 0, 0

	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			_, err := PerformEdit(db, root, "target.go", 3, 3, fmt.Sprintf("func target%d() {}", id), staleHash, nil)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				rejections++
			} else {
				successes++
			}
		}(i)
	}
	wg.Wait()

	assert.Equal(t, 1, successes, "exactly one writer may win the hash guard")
	assert.Equal(t, writers-1, rejections, "every other writer must be rejected")
}
