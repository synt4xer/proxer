package database

import (
	"fmt"
	"time"
)

// RetryConfig controls retry behavior for transient database errors.
type RetryConfig struct {
	MaxAttempts int
	InitialWait time.Duration
	MaxWait     time.Duration
}

// DefaultRetryConfig provides exponential backoff: 100ms, 200ms, 400ms (3 attempts).
var DefaultRetryConfig = RetryConfig{
	MaxAttempts: 3,
	InitialWait: 100 * time.Millisecond,
	MaxWait:     500 * time.Millisecond,
}

// WithRetry executes fn with exponential backoff on SQLITE_BUSY errors.
// Non-busy errors return immediately without retry.
func WithRetry(cfg RetryConfig, fn func() error) error {
	var lastErr error
	wait := cfg.InitialWait

	for attempt := 0; attempt < cfg.MaxAttempts; attempt++ {
		lastErr = fn()
		if lastErr == nil {
			return nil
		}

		if !IsBusyError(lastErr) {
			return lastErr
		}

		if attempt < cfg.MaxAttempts-1 {
			time.Sleep(wait)
			wait = minDuration(wait*2, cfg.MaxWait)
		}
	}

	return fmt.Errorf("failed after %d attempts: %w", cfg.MaxAttempts, lastErr)
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// IsBusyError returns true if err is a SQLite busy/locked error.
func IsBusyError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return contains(msg, "SQLITE_BUSY") ||
		contains(msg, "database is locked") ||
		contains(msg, "database is busy")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && indexOf(s, substr) >= 0
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
