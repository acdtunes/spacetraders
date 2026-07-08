// Package captain defines the strategic-event outbox consumed by the
// autonomous watchkeeper (see docs/superpowers/specs/2026-07-02-autonomous-captain-design.md).
package captain

import (
	"context"
	"time"
)

type EventType string

const (
	EventWorkflowFinished   EventType = "workflow.finished"
	EventWorkflowFailed     EventType = "workflow.failed"
	EventContainerCrashed   EventType = "container.crashed"
	EventContainerCrashLoop EventType = "container.crashloop"
	EventHeartbeatLost      EventType = "container.heartbeat_lost"
	EventShipIdle           EventType = "ship.idle"
	EventCreditsThreshold   EventType = "credits.threshold"
	EventContractCompleted  EventType = "contract.completed"
	EventContractFailed     EventType = "contract.failed"
	EventIncomeStalled      EventType = "income.stalled"
	EventStreamDown         EventType = "stream.down"

	// EventDeployCompleted marks a daemon boot running a different commit than
	// the last one recorded (sp-ess3): the one honest in-process "a deploy
	// happened" signal, since there is no distinct Go merge-deploy path — the
	// gate only gates+merges, and rebuild+restart happens out-of-process. It is
	// deferred class (rides the next wake, NOT in DefaultInterruptTypes) and is
	// what the crash-loop-resumes-on-deploy doctrine keys on: a job crash-
	// looping on a known defect with a fix in flight resumes on this event
	// instead of being re-rolled every heartbeat.
	EventDeployCompleted EventType = "deploy.completed"
)

// DefaultInterruptTypes returns the built-in set of event types that force
// an immediate captain wake regardless of cadence (spec: sp-sk68 wake
// model). Every other known event type is deferred: it does not wake the
// supervisor on its own, it simply rides whichever wake fires next (cadence,
// credits, or another interrupt) since bridgeWake always delivers the full
// unprocessed batch.
func DefaultInterruptTypes() []EventType {
	return []EventType{
		EventWorkflowFailed,
		// A single container.crashed is self-healing (auto-restart+resume), so it
		// is deferred; the interrupt-class crash signal is the crash LOOP below
		// (N true deaths of one container in a window — see detectCrashLoops).
		EventContainerCrashLoop,
		EventHeartbeatLost,
		EventContractFailed,
		EventIncomeStalled,
		EventStreamDown,
	}
}

// IsInterrupt reports whether t should force an immediate wake. A nil or
// empty override falls back to DefaultInterruptTypes(); a non-empty override
// (a captain-declared wake policy) REPLACES the default set entirely rather
// than extending it.
func IsInterrupt(t EventType, override []EventType) bool {
	set := override
	if len(set) == 0 {
		set = DefaultInterruptTypes()
	}
	for _, candidate := range set {
		if candidate == t {
			return true
		}
	}
	return false
}

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

// EventStore is the full port the watchkeeper uses.
type EventStore interface {
	EventRecorder
	// FindUnprocessed returns events with ProcessedAt IS NULL, oldest first.
	FindUnprocessed(ctx context.Context, playerID int, limit int) ([]*Event, error)
	MarkProcessed(ctx context.Context, ids []int64, at time.Time) error
	// HasUnprocessed reports whether an unprocessed event of the given type
	// exists for the ship (used to avoid duplicate synthetic events).
	HasUnprocessed(ctx context.Context, playerID int, t EventType, ship string) (bool, error)
	// HasSince reports whether any event of the given type exists for the
	// ship created after `since`, processed or not. Detectors use this as a
	// cooldown so persistent states (an idle ship) do not re-trigger a
	// session on every poll after each event is processed.
	HasSince(ctx context.Context, playerID int, t EventType, ship string, since time.Time) (bool, error)
	// LatestByType returns the most recently created event of type t for the
	// player (by CreatedAt, ties broken by ID), or nil if none exists. Used as
	// a zero-migration baseline: e.g. RecordDeployIfChanged reads the latest
	// deploy.completed event instead of a dedicated "last deploy" column.
	LatestByType(ctx context.Context, playerID int, t EventType) (*Event, error)
}
