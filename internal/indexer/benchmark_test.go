package indexer_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/synt4xer/proxer/database"
	"github.com/synt4xer/proxer/internal/indexer"
)

func BenchmarkFirstBuild32k(b *testing.B) {
	root := b.TempDir()
	var body strings.Builder
	body.WriteString("package pkg\n\n")
	for i := 0; i < 52; i++ {
		fmt.Fprintf(&body, "func AuthMiddleware%d(sqlRepo string, httpServer int) string {\n", i)
		fmt.Fprintf(&body, "\treturn sqlRepo + httpServerName(httpServer)\n")
		body.WriteString("}\n")
	}
	fileBytes := []byte(body.String())

	for i := 0; i < 8000; i++ {
		dir := filepath.Join(root, fmt.Sprintf("pkg%02d", i%40))
		if i < 40 {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				b.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file_%04d.go", i)), fileBytes, 0o644); err != nil {
			b.Fatal(err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		dbPath := filepath.Join(b.TempDir(), "bench.db")
		db, err := database.Open(dbPath, indexer.Chunking())
		if err != nil {
			b.Fatal(err)
		}
		b.StartTimer()

		refreshed, err := indexer.IndexProject(db, root, nil)
		if err != nil {
			b.Fatal(err)
		}
		if refreshed.Added != 8000 {
			b.Fatalf("expected 8000 added, got %d", refreshed.Added)
		}

		b.StopTimer()
		db.Close()
	}
}
