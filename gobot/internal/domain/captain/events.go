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
	EventContainerLost      EventType = "container.lost"
	EventHeartbeatLost      EventType = "container.heartbeat_lost"
	EventShipIdle           EventType = "ship.idle"

	// EventPinnedHullContainerless (sp-v63s watchdog) fires when a hull with a
	// standing fleet dedication (dedicated_fleet != '') has had NO running
	// coordinator container for longer than a threshold. The continuous trade/tour
	// engines run one container per dedicated hull across manifests, so a dedicated
	// hull sitting containerless is an anomaly — the state signature of EVERY
	// silent-death class (a container dropped from recovery, a crash that left no
	// event, a claim that never re-formed), regardless of which code path caused it.
	// Unlike ship.idle (a fleet-wide, deferred "this hull has nothing to do" signal),
	// this is a targeted alarm for a hull the operator PINNED to earn — it is
	// interrupt class, mirroring container.lost: a stranded revenue hull must wake
	// the captain, not ride the next cadence.
	EventPinnedHullContainerless EventType = "hull.containerless"
	EventCreditsThreshold        EventType = "credits.threshold"
	EventContractCompleted       EventType = "contract.completed"
	EventContractFailed          EventType = "contract.failed"
	EventIncomeStalled           EventType = "income.stalled"
	EventStreamDown              EventType = "stream.down"

	// EventDeployCompleted marks a daemon boot running a different commit than
	// the last one recorded (sp-ess3): the one honest in-process "a deploy
	// happened" signal, since there is no distinct Go merge-deploy path — the
	// gate only gates+merges, and rebuild+restart happens out-of-process. It is
	// deferred class (rides the next wake, NOT in DefaultInterruptTypes) and is
	// what the crash-loop-resumes-on-deploy doctrine keys on: a job crash-
	// looping on a known defect with a fix in flight resumes on this event
	// instead of being re-rolled every heartbeat.
	EventDeployCompleted EventType = "deploy.completed"

	// EventMarketRegimeShift fires when a captain-declared price tripwire
	// (sp-zlfv) crosses: mechanizes the per-wake price sweep the captain used
	// to hand-roll ("any ore bid >=200 or gas bid >=150 (~3x baseline)
	// triggers an immediate extraction re-consult" — captain transcript). It
	// is deferred class (rides the next wake, NOT in DefaultInterruptTypes):
	// a price crossing is worth reconsidering next time the captain is up,
	// not worth forcing a wake on its own.
	EventMarketRegimeShift EventType = "market.regime_shift"

	// EventCoordinatorErrorLoop fires when a coordinator's internal retry
	// loop hits the identical error N times in a row at the same checkpoint
	// (sp-e2l1). Distinct from workflow.failed: the coordinator's container
	// is still RUNNING and retrying, not exited, so reusing workflow.failed
	// would misrepresent container state to consumers. It is interrupt
	// class: the whole point is that a stuck-but-silent loop (the 2026-07-05
	// negotiate-nil incident ran 18h and emitted nothing) must force a wake
	// instead of riding the next one.
	EventCoordinatorErrorLoop EventType = "coordinator.error_loop"

	// EventWakeWatch is the synthetic marker emitted by the watchkeeper when a
	// captain-armed one-shot wake watch fires (sp-oyer): a watched ship arrived,
	// a watched container reached a terminal state, or the watch's deadline
	// passed. It is ALWAYS interrupt class — a targeted wake the captain
	// explicitly asked for must never be downgraded to deferred — and that is
	// enforced in the watchkeeper's partitionEvents rather than here: it is
	// deliberately NOT in DefaultInterruptTypes because a captain-declared
	// --interrupt-types override REPLACES that set (see IsInterrupt), which would
	// silently drop this marker exactly when a watch fires. The watch payload
	// carries which watch fired and whether it was matched or deadline-fired.
	EventWakeWatch EventType = "wake.watch"
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
		// container.lost is emitted at boot recovery for a container that was
		// RUNNING/INTERRUPTED before shutdown but did NOT come back (recovery
		// error, or a candidate that fell out of the pass uncategorized). Unlike
		// a single container.crashed it is interrupt class: a crash auto-restarts
		// and resumes, but a recovery-lost container just stays dead until someone
		// acts (the sp-tit8 incident: a +200k/hr MEDICINE factory dead ~100 min,
		// caught only by eyeball). A single loss must wake the captain, not ride
		// the next cadence. By-design non-recoveries (coordinator-managed workers
		// that respawn, dead-era universe-reset containers) never emit this event.
		EventContainerLost,
		// A hull PINNED to a fleet with no running container for >N min is a
		// stranded revenue hull — like container.lost, it stays dead until someone
		// acts, so it forces a wake rather than riding the next cadence (sp-v63s).
		EventPinnedHullContainerless,
		EventHeartbeatLost,
		EventContractFailed,
		EventIncomeStalled,
		EventStreamDown,
		EventCoordinatorErrorLoop,
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
