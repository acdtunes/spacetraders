package health

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// captureRecorder records the last event it was handed and can be told to fail,
// so the emission helper's shape and error-swallowing contract are observable
// without a real outbox.
type captureRecorder struct {
	last    *captain.Event
	calls   int
	failErr error
}

func (c *captureRecorder) Record(_ context.Context, e *captain.Event) error {
	c.calls++
	c.last = e
	return c.failErr
}

// captureLogger records WARNING lines so the swallow-and-log contract is testable.
type captureLogger struct{ warnings int }

func (l *captureLogger) Log(level, _ string, _ map[string]interface{}) {
	if level == "WARNING" {
		l.warnings++
	}
}

// TestRecordErrorLoop_NilRecorder_NoOp pins that an unwired recorder (tests, or
// a daemon boot before DI completes) silently disables emission rather than
// panicking.
func TestRecordErrorLoop_NilRecorder_NoOp(t *testing.T) {
	// A nil recorder and a nil logger must both be tolerated.
	RecordErrorLoop(nil, nil, "container-1", 7, "negotiate_contract", errors.New("boom"), 5)
}

// TestRecordErrorLoop_EmitsShapedEvent pins that a crossing hands the recorder
// the interrupt-class, container-scoped event carrying checkpoint/cause/streak.
func TestRecordErrorLoop_EmitsShapedEvent(t *testing.T) {
	rec := &captureRecorder{}
	cause := errors.New("failed to find idle haulers: context deadline exceeded")

	RecordErrorLoop(rec, &captureLogger{}, "stocker-coordinator-player-1-abc", 1, "find_candidates", cause, 5)

	if rec.calls != 1 {
		t.Fatalf("expected the recorder to be called exactly once, got %d", rec.calls)
	}
	if rec.last.Type != captain.EventCoordinatorErrorLoop {
		t.Fatalf("expected type %q, got %q", captain.EventCoordinatorErrorLoop, rec.last.Type)
	}
	if rec.last.Ship != "stocker-coordinator-player-1-abc" {
		t.Fatalf("expected Ship to carry the container id, got %q", rec.last.Ship)
	}
	if rec.last.PlayerID != 1 {
		t.Fatalf("expected PlayerID 1, got %d", rec.last.PlayerID)
	}
}

// TestRecordErrorLoop_OutboxFailure_LoggedAndSwallowed pins that an outbox error
// is logged at WARNING and never propagated — a coordinator's retry loop must
// not break because the event bus hiccuped.
func TestRecordErrorLoop_OutboxFailure_LoggedAndSwallowed(t *testing.T) {
	rec := &captureRecorder{failErr: errors.New("outbox down")}
	logger := &captureLogger{}

	// Must not panic and must not surface the error (signature returns nothing).
	RecordErrorLoop(rec, logger, "container-1", 7, "reconcile", errors.New("boom"), 10)

	if logger.warnings != 1 {
		t.Fatalf("expected exactly one WARNING logged on outbox failure, got %d", logger.warnings)
	}
}
