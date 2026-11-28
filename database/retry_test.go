package database

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fastConfig keeps tests well under a second while still exercising backoff.
func fastConfig(attempts int) RetryConfig {
	return RetryConfig{
		MaxAttempts: attempts,
		InitialWait: time.Millisecond,
		MaxWait:     4 * time.Millisecond,
	}
}

func TestWithRetrySucceedsFirstTry(t *testing.T) {
	calls := 0
	err := WithRetry(fastConfig(3), func() error {
		calls++
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, 1, calls, "a successful first attempt must not retry")
}

func TestWithRetryRetriesBusyThenSucceeds(t *testing.T) {
	calls := 0
	err := WithRetry(fastConfig(3), func() error {
		calls++
		if calls < 3 {
			return errors.New("SQLITE_BUSY: database is locked")
		}
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, 3, calls, "must retry busy errors until success")
}

func TestWithRetryExhaustsAttempts(t *testing.T) {
	busy := errors.New("database is locked")
	calls := 0
	err := WithRetry(fastConfig(3), func() error {
		calls++
		return busy
	})

	require.Error(t, err)
	assert.Equal(t, 3, calls, "must stop at MaxAttempts")
	assert.Contains(t, err.Error(), "failed after 3 attempts")
	assert.ErrorIs(t, err, busy, "the underlying error must stay in the chain for errors.Is")
}

func TestWithRetryDoesNotRetryNonBusyError(t *testing.T) {
	sentinel := errors.New("syntax error near FROM")
	calls := 0

	err := WithRetry(fastConfig(5), func() error {
		calls++
		return sentinel
	})

	assert.Equal(t, 1, calls, "non-busy errors must fail fast, not burn the retry budget")
	assert.ErrorIs(t, err, sentinel, "the original error must propagate unwrapped")
}

func TestWithRetryZeroAttempts(t *testing.T) {
	calls := 0
	err := WithRetry(fastConfig(0), func() error {
		calls++
		return nil
	})

	assert.Equal(t, 0, calls)
	assert.Error(t, err, "MaxAttempts <= 0 must not report success")
}

func TestIsBusyError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"sqlite_busy", errors.New("SQLITE_BUSY (5)"), true},
		{"database is locked", errors.New("database is locked"), true},
		{"database is busy", errors.New("database is busy"), true},
		{"unrelated", errors.New("no such table: chunk_search"), false},
		{"empty", errors.New(""), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsBusyError(tc.err))
		})
	}
}

func TestMinDuration(t *testing.T) {
	assert.Equal(t, time.Second, minDuration(time.Second, 2*time.Second))
	assert.Equal(t, time.Second, minDuration(2*time.Second, time.Second))
	assert.Equal(t, time.Second, minDuration(time.Second, time.Second))
}

func TestJitterWaitWindow(t *testing.T) {
	const d = 100 * time.Millisecond
	seen := make(map[time.Duration]struct{}, 64)

	for i := 0; i < 64; i++ {
		got := jitterWait(d)
		assert.GreaterOrEqual(t, got, d/2, "jitter must not fall below d/2")
		assert.Less(t, got, d, "jitter must stay below d")
		seen[got] = struct{}{}
	}

	assert.Greater(t, len(seen), 1, "jitter must vary; a constant delay recreates the lockstep herd")
}

// TestJitterWaitDoesNotLockstep is the regression guard for the retry herd:
// many goroutines hitting the same busy error must not all wake in lockstep.
func TestJitterWaitDoesNotLockstep(t *testing.T) {
	const d = 40 * time.Millisecond

	delays := make([]time.Duration, 32)
	for i := range delays {
		delays[i] = jitterWait(d)
	}

	identical := 0
	for _, v := range delays {
		if v == delays[0] {
			identical++
		}
	}
	assert.Less(t, identical, len(delays), "delays must not all be identical")
}

func TestJitterWaitSmallDuration(t *testing.T) {
	// A sub-2ns wait halves to zero; jitter must fall back to the raw duration
	// instead of returning 0 and spinning.
	assert.Equal(t, time.Duration(0), jitterWait(0))
	assert.Equal(t, time.Nanosecond, jitterWait(time.Nanosecond))
}

func TestWithRetryBackoffCappedAtMaxWait(t *testing.T) {
	cfg := RetryConfig{
		MaxAttempts: 3,
		InitialWait: time.Nanosecond,
		MaxWait:     time.Nanosecond,
	}

	start := time.Now()
	err := WithRetry(cfg, func() error { return errors.New("database is locked") })
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Less(t, elapsed, 500*time.Millisecond, "MaxWait must cap the backoff")
}
