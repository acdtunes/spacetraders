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

	// EventPinnedHullContainerless fires when a hull with a standing fleet
	// dedication (dedicated_fleet != '') has had no running coordinator container
	// for longer than a threshold — a stranded revenue hull. Interrupt class,
	// mirroring container.lost: it must wake the captain, not ride the next cadence.
	EventPinnedHullContainerless EventType = "hull.containerless"
	EventCreditsThreshold        EventType = "credits.threshold"
	EventContractCompleted       EventType = "contract.completed"
	EventContractFailed          EventType = "contract.failed"
	EventIncomeStalled           EventType = "income.stalled"
	EventStreamDown              EventType = "stream.down"

	// EventDeployCompleted marks a daemon boot running a different commit than the
	// last recorded. Deferred class. The crash-loop-resumes-on-deploy doctrine keys
	// on it: a job crash-looping on a known defect with a fix in flight resumes on
	// this event instead of being re-rolled every heartbeat.
	EventDeployCompleted EventType = "deploy.completed"

	// EventMarketRegimeShift fires when a captain-declared price tripwire crosses.
	// Deferred class: a price crossing is worth reconsidering next wake, not worth
	// forcing a wake on its own.
	EventMarketRegimeShift EventType = "market.regime_shift"

	// EventCoordinatorErrorLoop fires when a coordinator's internal retry loop hits
	// the identical error N times in a row at the same checkpoint. Distinct from
	// workflow.failed: the container is still RUNNING and retrying, not exited, so
	// reusing workflow.failed would misrepresent container state. Interrupt class —
	// a stuck-but-silent loop must force a wake.
	EventCoordinatorErrorLoop EventType = "coordinator.error_loop"

	// EventDaemonComponentCrashLoop fires when a supervised daemon background
	// component (ship-state sweeper, container recovery, samplers — NOT containers,
	// which have container.crashloop) has crashed and been restarted
	// crashLoopThreshold times within crashLoopWindow (see
	// internal/infrastructure/supervise). Interrupt class: a safety-net component
	// dying in a loop silently degrades the whole fleet. Edge-triggered once per
	// window, never per-crash.
	EventDaemonComponentCrashLoop EventType = "daemon.component_crashloop"

	// EventWakeWatch is the synthetic marker the watchkeeper emits when a
	// captain-armed one-shot wake watch fires (watched ship arrived, watched
	// container reached a terminal state, or the deadline passed). ALWAYS interrupt
	// class, enforced in the watchkeeper's partitionEvents rather than here: it is
	// deliberately NOT in DefaultInterruptTypes because a captain-declared
	// --interrupt-types override REPLACES that set (see IsInterrupt), which would
	// silently drop this marker exactly when a watch fires. The payload carries
	// which watch fired and whether it was matched or deadline-fired.
	EventWakeWatch EventType = "wake.watch"

	// The scout staleness family is all DEFERRED class (NOT in DefaultInterruptTypes):
	// each is a "reconsider next wake" coverage signal, hours-scale, never worth
	// forcing a wake on its own.

	// EventScoutPostUndersized fires when a standing scout post's deterministic
	// circuit math (markets / hulls × avg hop) cannot keep its markets within the
	// post's own freshness target — the post is structurally too small for its
	// system. The payload names the required hull count so the fix is spelled out.
	EventScoutPostUndersized EventType = "scout.post_undersized"

	// EventStalenessHidingRevenue fires when a market-rich system (>= N priced
	// markets) has enough of its markets aged past the tour-planner staleness cap
	// that the planner is dropping their lanes — staleness is actively hiding
	// tradeable revenue.
	EventStalenessHidingRevenue EventType = "scout.staleness_hiding_revenue"

	// EventScoutPostProposal fires when discovery has priced a system past the
	// market-rich threshold yet NO scout post stands over it — a coverage gap the
	// captain should close. PROPOSAL only (the captain decides and declares); the
	// payload carries the hull count from the circuit math.
	EventScoutPostProposal EventType = "scout.post_proposal"

	// EventScoutPostManningStalled fires when a post reads IsFullyManned() yet has
	// produced NO new scan telemetry: its worst-case market age has breached the
	// post's OWN freshness target without improving for N consecutive reconcile
	// cycles, so its tour is wedged (the container may read RUNNING while the hull no
	// longer scans — invisible to the reconciler, whose only health signal is
	// container liveness). The watchdog re-mans it (tears the wedged tour down so the
	// reconciler re-mans it fresh); after re-manning to its correction cap without
	// telemetry recovering it BACKS OFF — keeps emitting so the stuck post stays
	// visible, but stops churning a tour a genuinely unreachable market will only
	// wedge again. DEFERRED class. The payload carries the market count, worst-case
	// age, freshness target, stall-cycle count, cycle-sample count, corrections taken,
	// and whether the watchdog has backed off.
	EventScoutPostManningStalled EventType = "scout.post_manning_stalled"

	// EventFleetAutosizerPurchase fires when the fleet capacity autosizer buys a hull —
	// a treasury-moving action the captain must be able to see and audit. The payload
	// carries the class, ship type, price, and the demand arithmetic that justified it.
	EventFleetAutosizerPurchase EventType = "fleet.autosizer_purchase"

	// EventAutoOutfitInstalled fires when the guarded auto-outfit coordinator installs a
	// capacity module on an existing hull — a treasury-moving action the captain must be
	// able to see and audit. DEFERRED class. The payload carries the ship, the module,
	// the price, the new capacity, and the cost-per-unit that beat the new-hull
	// alternative.
	EventAutoOutfitInstalled EventType = "fleet.auto_outfit_installed"

	// EventAutoOutfitModuleInReach fires ONCE when a watchlisted capacity module
	// (FUEL_TANK, CARGO_HOLD_II/III) first appears in a reachable market — the
	// coordinator can now close a capability gap without buying a new hull. DEFERRED
	// class. The payload carries the module, the market waypoint, and the price.
	EventAutoOutfitModuleInReach EventType = "fleet.auto_outfit_module_in_reach"

	// EventHeavyYardDiscovered fires ONCE per era when the scout tour's piggybacked
	// shipyard scan first discovers a yard selling a heavy-freight hull
	// ({SHIP_HEAVY_FREIGHTER, SHIP_BULK_FREIGHTER} by default, [scouting]
	// heavy_ship_types to override). The fleet autosizer's heavy branch fails closed
	// without this signal, so its first appearance is news the captain acts on (arm
	// heavies, size the trade fleet). DEFERRED class. The payload carries the system,
	// waypoint, and the heavy types + prices found.
	EventHeavyYardDiscovered EventType = "shipyard.heavy_yard_discovered"

	// EventConfigTuned is the audit record of every EFFECTIVE `spacetraders tune`
	// write — a live change to a running container's spend/cooldown/cap knob. These
	// knobs move real credits, so a tune must never be a silent DB write: the payload
	// carries container, key, old→new effective values, and the requested value.
	// Deferred class. No-op re-tunes and rejected tunes emit nothing.
	EventConfigTuned EventType = "config.tuned"

	// EventPrometheusAlertFiring fires once per Prometheus alertname found in the
	// "firing" state on Prometheus's /api/v1/alerts endpoint — EarnerDark,
	// BurstSaturation, ApproachCeiling, StarvationWave (see
	// gobot/configs/prometheus/rules/fleet-health.yml). Interrupt class — a
	// revenue-critical stall must wake the captain, not ride the next cadence. The
	// payload carries the alert's labels/annotations (alertname, summary, severity)
	// so the wake mail explains WHY without a Grafana round-trip (see
	// describePrometheusAlert in wake.go).
	EventPrometheusAlertFiring EventType = "prometheus.alert_firing"

	// EventSupervisorGagged is the edge-triggered audit record of the running
	// supervisor entering or exiting its dynamic GAG stand-down — the soft,
	// runtime-togglable pause (distinct from the captain/DISABLED hard halt) that
	// stands the supervisor down from ALL wake-eval actions (spawns no captain
	// session, takes no corrective action) while keeping its process, heartbeat, and
	// the universe-reset safety rail live. Recorded ONCE per transition, never per
	// tick. DEFERRED class: while gagged no wake fires at all, and the resume rides
	// the next natural wake. The payload carries the new state ("gagged" or
	// "ungagged") and the operator's reason.
	EventSupervisorGagged EventType = "captain.supervisor_gagged"

	// EventCapacityCapexProposal fires when the capacity reconciler's tiered-autonomy
	// gate files a CAPITAL proposal for human approval — a tier-4 capacity add
	// (autobuy a hull / stand up a cluster) that moves treasury and therefore NEVER
	// auto-executes under v1 tiered autonomy (approval threshold 0). PROPOSAL only
	// (the captain decides and declares), so filing it spends NOTHING; capital
	// executes only later, via the approval-execution path, past the invariant-4
	// capital gate. The payload carries the full ROI evidence (estimated cost,
	// projected gain/hr, payback horizon + projected hours, before/after fleet
	// per-hull cr/hr, and a narrative) so the approver judges from evidence. DEFERRED
	// class. Deduped per proposal (the Proposal.ID is stable per gap) over a cooldown
	// so a gap re-proposed every reconcile tick nudges ONCE, not per tick.
	EventCapacityCapexProposal EventType = "capacity.capex_proposal"
)

// DefaultInterruptTypes returns the built-in set of event types that force
// an immediate captain wake regardless of cadence. Every other known event type
// is deferred: it does not wake the supervisor on its own, it simply rides
// whichever wake fires next (cadence, credits, or another interrupt) since
// bridgeWake always delivers the full unprocessed batch.
func DefaultInterruptTypes() []EventType {
	return []EventType{
		EventWorkflowFailed,
		// A single container.crashed is self-healing (auto-restart+resume), so it
		// is deferred; the interrupt-class crash signal is the crash LOOP below
		// (N true deaths of one container in a window — see detectCrashLoops).
		EventContainerCrashLoop,
		EventDaemonComponentCrashLoop,
		// container.lost is emitted at boot recovery for a container that was
		// RUNNING/INTERRUPTED before shutdown but did NOT come back (recovery
		// error, or a candidate that fell out of the pass uncategorized). Unlike a
		// single container.crashed it is interrupt class: a crash auto-restarts and
		// resumes, but a recovery-lost container just stays dead until someone acts.
		// By-design non-recoveries (coordinator-managed workers that respawn,
		// dead-era universe-reset containers) never emit this event.
		EventContainerLost,
		// A hull PINNED to a fleet with no running container for >N min is a
		// stranded revenue hull — like container.lost, it stays dead until someone
		// acts, so it forces a wake rather than riding the next cadence.
		EventPinnedHullContainerless,
		EventHeartbeatLost,
		EventContractFailed,
		EventIncomeStalled,
		EventStreamDown,
		EventCoordinatorErrorLoop,
		// A firing Prometheus alert (EarnerDark/BurstSaturation/ApproachCeiling/
		// StarvationWave) is by definition revenue-critical or capacity-critical.
		// Interrupt class.
		EventPrometheusAlertFiring,
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
