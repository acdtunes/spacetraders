package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// captainOutboxRecordTimeout bounds the outbox write, which runs on a DETACHED
// context so a cancelled coordinator still records — and cannot outlive the process.
const captainOutboxRecordTimeout = 5 * time.Second

// recordErrorLoopEvent emits the captain outbox event for a checkpoint's
// error streak crossing. Fire-and-forget with its own short timeout,
// mirroring internal/adapters/grpc/captain_recorder.go's idiom: an
// outbox failure must never break the coordinator's retry loop, so errors
// are logged at WARNING and swallowed. A nil captainEvents (not wired —
// tests, or a daemon boot before main finishes DI) silently disables
// recording rather than panicking.
func (h *RunFleetCoordinatorHandler) recordErrorLoopEvent(ctx context.Context, cmd *RunFleetCoordinatorCommand, checkpoint string, cause error, streak int) {
	if h.captainEvents == nil {
		return
	}
	logger := common.LoggerFromContext(ctx)
	event := health.NewErrorLoopEvent(cmd.ContainerID, cmd.PlayerID.Value(), checkpoint, cause, streak)
	recordCtx, cancel := context.WithTimeout(context.Background(), captainOutboxRecordTimeout)
	defer cancel()
	if err := h.captainEvents.Record(recordCtx, event); err != nil {
		logger.Log("WARNING", fmt.Sprintf("captain outbox: failed to record %s for checkpoint %s: %v", captain.EventCoordinatorErrorLoop, checkpoint, err), nil)
	}
}

// recordHullQuarantineEvent emits the captain outbox event for a hull crossing
// into spawn quarantine. Fire-and-forget with its own short timeout,
// mirroring recordErrorLoopEvent exactly: an outbox failure must never
// break the coordinator's loop, so it is logged at WARNING and swallowed, and a
// nil captainEvents (not wired — tests, or a daemon boot before DI completes)
// silently disables recording rather than panicking.
func (h *RunFleetCoordinatorHandler) recordHullQuarantineEvent(ctx context.Context, cmd *RunFleetCoordinatorCommand, hull string, outcome spawnOutcome) {
	if h.captainEvents == nil {
		return
	}
	logger := common.LoggerFromContext(ctx)
	event := buildHullQuarantineEvent(cmd.ContainerID, cmd.PlayerID.Value(), hull, outcome)
	recordCtx, cancel := context.WithTimeout(context.Background(), captainOutboxRecordTimeout)
	defer cancel()
	if err := h.captainEvents.Record(recordCtx, event); err != nil {
		logger.Log("WARNING", fmt.Sprintf("captain outbox: failed to record hull quarantine for %s: %v", hull, err), nil)
	}
}
