package database

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/jmoiron/sqlx"
)

const (
	defaultBusyTimeoutMS   = 5000
	preflightBusyTimeoutMS = 250
)

// CheckWritable verifies that db accepts writes before a caller mutates files
// on disk. It pins a single *sql.Conn, lowers busy_timeout to 250 ms with a
// cancel-safe restore to 5000 ms, and executes a real no-op UPDATE inside a
// rolled-back BEGIN IMMEDIATE transaction. A bare BEGIN IMMEDIATE + ROLLBACK
// returns nil on ?mode=ro and 0444 files in modernc.org/sqlite, so the no-op
// UPDATE is required to detect read-only databases accurately.
func CheckWritable(ctx context.Context, db *sqlx.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire database connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", preflightBusyTimeoutMS)); err != nil {
		return fmt.Errorf("failed to set preflight busy_timeout: %w", err)
	}
	defer func() {
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		if _, rerr := conn.ExecContext(restoreCtx, fmt.Sprintf("PRAGMA busy_timeout = %d", defaultBusyTimeoutMS)); rerr != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}

	_, updateErr := conn.ExecContext(ctx, "UPDATE meta SET value = value WHERE key = ?", metaSchemaVersion)

	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if _, rerr := conn.ExecContext(rollbackCtx, "ROLLBACK"); rerr != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	}

	return updateErr
}

// IsReadOnlyError reports whether err indicates a read-only database or
// filesystem permission failure.
func IsReadOnlyError(err error) bool {
	if err == nil {
		return false
	}
	if os.IsPermission(err) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EROFS) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "readonly") ||
		strings.Contains(msg, "read-only") ||
		strings.Contains(msg, "permission denied")
}

// IsBusyError reports whether err indicates SQLite lock contention.
func IsBusyError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "sqlite_busy") ||
		strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database is busy")
}
