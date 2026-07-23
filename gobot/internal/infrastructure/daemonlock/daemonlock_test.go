package daemonlock

import (
	"context"
	"errors"
	"testing"
)

// fakeLocker stands in for the Postgres advisory lock at the port boundary, so
// the fatal/proceed decision is tested without a database (the suite has no
// Postgres harness; the real pg_try_advisory_lock path is covered by wiring).
type fakeLocker struct {
	acquired  bool
	err       error
	gotPlayer int
	calls     int
}

func (f *fakeLocker) TryLock(_ context.Context, playerID int) (bool, error) {
	f.calls++
	f.gotPlayer = playerID
	return f.acquired, f.err
}

// Lock granted → the daemon may proceed, and the correct player was locked.
func TestAcquireExclusiveSucceedsWhenLockGranted(t *testing.T) {
	f := &fakeLocker{acquired: true}
	if err := AcquireExclusive(context.Background(), f, 7); err != nil {
		t.Fatalf("expected nil when the lock is granted, got %v", err)
	}
	if f.gotPlayer != 7 {
		t.Fatalf("locker asked to lock player %d, want 7", f.gotPlayer)
	}
}

// Lock held by another live daemon → fail closed (no second writer).
func TestAcquireExclusiveFailsWhenLockHeldByAnotherDaemon(t *testing.T) {
	f := &fakeLocker{acquired: false}
	if err := AcquireExclusive(context.Background(), f, 7); err == nil {
		t.Fatal("expected a fatal error when the advisory lock is already held, got nil")
	}
}

// A locker error is fatal too — the daemon must not start unsure of exclusivity.
func TestAcquireExclusiveFailsOnLockerError(t *testing.T) {
	f := &fakeLocker{err: errors.New("connection refused")}
	if err := AcquireExclusive(context.Background(), f, 7); err == nil {
		t.Fatal("expected an error when the locker fails, got nil")
	}
}
