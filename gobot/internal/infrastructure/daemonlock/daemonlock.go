// Package daemonlock enforces a single live daemon per player using a Postgres
// session advisory lock. The lock is held on a dedicated connection for the life
// of the process and released automatically when that connection closes (clean
// shutdown) or drops (crash), so two daemons can never write the same player's
// game state — regardless of PID-file/socket path or a manual --force (sp-wrh84).
package daemonlock

import (
	"context"
	"database/sql"
	"fmt"
)

// daemonAdvisoryNamespace namespaces the per-player daemon lock in Postgres'
// shared advisory-lock keyspace, distinct from the transaction locks the
// absorption ("ABSB") and spend ("SPND") ledgers take, so keys never collide.
const daemonAdvisoryNamespace = 0x444D4F4E // "DMON"

// PlayerLocker acquires the exclusive per-player daemon lock.
type PlayerLocker interface {
	// TryLock attempts to acquire the lock. It returns acquired=false (with a nil
	// error) when another live session already holds it.
	TryLock(ctx context.Context, playerID int) (acquired bool, err error)
}

// AcquireExclusive acquires the per-player daemon lock or returns a fatal error.
// A held lock means another daemon is already running for this player: the caller
// must abort rather than start a second writer.
func AcquireExclusive(ctx context.Context, locker PlayerLocker, playerID int) error {
	acquired, err := locker.TryLock(ctx, playerID)
	if err != nil {
		return fmt.Errorf("failed to acquire daemon advisory lock for player %d: %w", playerID, err)
	}
	if !acquired {
		return fmt.Errorf("another daemon is already running for player %d (advisory lock held); refusing to start a second writer", playerID)
	}
	return nil
}

// PostgresPlayerLock holds the per-player session advisory lock on a dedicated
// Postgres connection kept open for the daemon's lifetime.
type PostgresPlayerLock struct {
	conn *sql.Conn
}

// NewPostgresPlayerLock pins a dedicated connection out of the pool. The caller
// MUST keep the returned lock alive for the whole process and Close it at
// shutdown; closing the connection is what releases the session advisory lock.
func NewPostgresPlayerLock(ctx context.Context, db *sql.DB) (*PostgresPlayerLock, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to pin advisory-lock connection: %w", err)
	}
	return &PostgresPlayerLock{conn: conn}, nil
}

// TryLock runs pg_try_advisory_lock on the pinned connection, so the lock is held
// at SESSION scope (auto-released when the connection ends).
func (l *PostgresPlayerLock) TryLock(ctx context.Context, playerID int) (bool, error) {
	var acquired bool
	row := l.conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1, $2)", daemonAdvisoryNamespace, playerID)
	if err := row.Scan(&acquired); err != nil {
		return false, err
	}
	return acquired, nil
}

// Close releases the session advisory lock by closing the pinned connection.
func (l *PostgresPlayerLock) Close() error {
	if l == nil || l.conn == nil {
		return nil
	}
	return l.conn.Close()
}
