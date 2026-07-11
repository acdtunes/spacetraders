package health

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// errorLoopRecordTimeout bounds the detached outbox write so a stalled
// recorder can never wedge a coordinator's retry loop.
const errorLoopRecordTimeout = 5 * time.Second

// RecordErrorLoop emits the coordinator error-loop captain event for a
// checkpoint whose identical-error streak just crossed a threshold multiple
// (sp-e2l1; shared across every coordinator by the sp-6wxq rollout). It is the
// single fire-and-forget emission helper each coordinator's streak hook calls,
// replacing the per-coordinator recordErrorLoopEvent boilerplate the contract
// fleet coordinator first hand-rolled (run_fleet_coordinator.go:recordErrorLoopEvent).
//
// The contract is identical to that original: a nil recorder — unwired in
// tests, or during a daemon boot before DI completes — silently disables
// emission rather than panicking; the write runs on a detached, short-timeout
// context so an outbox stall cannot break the caller's loop; and an outbox
// failure is logged at WARNING (via logger, when non-nil) and swallowed, never
// returned. Callers invoke this only on the exact iteration Monitor.Note
// reports crossed==true, so emission stays edge-triggered, not per-iteration.
func RecordErrorLoop(rec captain.EventRecorder, logger common.ContainerLogger, containerID string, playerID int, checkpoint string, cause error, streak int) {
	if rec == nil {
		return
	}
	event := NewErrorLoopEvent(containerID, playerID, checkpoint, cause, streak)
	ctx, cancel := context.WithTimeout(context.Background(), errorLoopRecordTimeout)
	defer cancel()
	if err := rec.Record(ctx, event); err != nil && logger != nil {
		logger.Log("WARNING", fmt.Sprintf("captain outbox: failed to record %s for checkpoint %s: %v", captain.EventCoordinatorErrorLoop, checkpoint, err), nil)
	}
}
