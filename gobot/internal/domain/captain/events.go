// Package captain defines the strategic-event outbox consumed by the
// autonomous captain supervisor (see docs/superpowers/specs/2026-07-02-autonomous-captain-design.md).
package captain

import (
	"context"
	"time"
)

type EventType string

const (
	EventWorkflowFinished  EventType = "workflow.finished"
	EventWorkflowFailed    EventType = "workflow.failed"
	EventContainerCrashed  EventType = "container.crashed"
	EventHeartbeatLost     EventType = "container.heartbeat_lost"
	EventShipIdle          EventType = "ship.idle"
	EventCreditsThreshold  EventType = "credits.threshold"
	EventContractCompleted EventType = "contract.completed"
	EventContractFailed    EventType = "contract.failed"
)

type Event struct {
	ID          int64
	Type        EventType
	Ship        string // ship symbol, empty when not ship-scoped
	PlayerID    int
	Payload     string // JSON object with event-specific detail
	CreatedAt   time.Time
	ProcessedAt *time.Time
}

// EventRecorder is the write-only port the daemon uses.
type EventRecorder interface {
	Record(ctx context.Context, e *Event) error
}

// EventStore is the full port the captain supervisor uses.
type EventStore interface {
	EventRecorder
	// FindUnprocessed returns events with ProcessedAt IS NULL, oldest first.
	FindUnprocessed(ctx context.Context, playerID int, limit int) ([]*Event, error)
	MarkProcessed(ctx context.Context, ids []int64, at time.Time) error
	// HasUnprocessed reports whether an unprocessed event of the given type
	// exists for the ship (used to avoid duplicate synthetic events).
	HasUnprocessed(ctx context.Context, playerID int, t EventType, ship string) (bool, error)
}
