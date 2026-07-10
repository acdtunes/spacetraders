package contract

import (
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// The lane mutex is the dispatcher-level guard against the idle-arb LANE
// COLLISION (sp-lbbm): the money-loss class where two dedicated hulls were
// dispatched onto the SAME (good, sell-market) in one recovery window, each
// having quoted the pre-crush bid. On 2026-07-10 hulls 7+8 concurrently dumped
// SHIP_PARTS into H50, crushed the bid 19,950→4/unit, and TORWIND-7 kept selling
// (five tranches for 27 credits total, ~−80k net). The dispatcher launched two
// full dumps into one vol-6 sink because nothing tracked that a lane was already
// being worked.
//
// This tracker enforces ONE hull per (good, sink) per recovery window, covering
// BOTH the ways a collision arose:
//
//   - WITHIN a pass: the launch loop marks a lane in-flight the instant it
//     launches a leg, so a second candidate that would pick the SAME (good, sink)
//     this pass finds it held and is skipped:lane-held (the higher-margin hull is
//     processed first and wins the sink; the other falls back to its next-best
//     unheld lane, or waits a pass).
//   - ACROSS passes: legs are fire-and-forget arb_run containers that outlive the
//     pass that launched them. A lane stays held for as long as its leg's
//     container is still claimed to the hull, PLUS a recovery-window hold after
//     the leg terminates — so the next pass does not re-dump a sink the last leg
//     just depressed.
//
// The lane frees when its leg's container terminates — observed through the SAME
// hull re-eligibility seam the reserve accounting already uses: the dispatcher
// reads the fleet's live ship→container map each pass, and a launched leg whose
// hull no longer carries its container id (released back to idle, or re-claimed
// by a contract) has terminated. After that, a flat recovery hold keeps the lane
// closed while the sink recovers.
//
// WHY A FLAT HOLD (not the market model's own recovery curve): the routing
// service's market model knows the real recovery window — its recovery
// half-lives run 180min (GROWING) / 279min (WEAK) / 386min (STRONG) / 413min
// (RESTRICTED), ~1074min baseline (services/routing-service/model_artifacts/
// market_model.json). But that model lives behind the routing service; coupling
// the contract dispatcher to it would drag a heavy dependency into a decision
// loop that must stay pure. A parametrized flat hold (DefaultIdleArbRecoveryHold,
// config-tunable via the ts82 live path) is the honest v1: 20min is far shorter
// than any modelled half-life, so it does not claim the sink has fully recovered
// — it is the conservative "do not immediately re-dump" spacer that, together
// with the in-flight block above and the sp-lbbm per-tranche sell floor (which
// aborts a re-dump into a still-depressed sink), kills the concurrent-dump loss
// class. A captain who wants the fuller modelled hold raises the config knob; no
// code change (RULINGS #5).
//
// State is IN-MEMORY and per-dispatcher, mirroring spawnGovernor (which documents
// the same choice): a daemon restart builds a fresh tracker. That is safe because
// a leg in flight at restart is a CLAIMED hull, which FindIdleShipsByFleet
// excludes from candidates — so the mid-flight hull is never re-dispatched onto
// its own lane. The only state a restart drops is the post-termination hold, and
// a restart implies elapsed wall-time during which the sink recovered anyway; the
// sell floor remains the backstop for the narrow re-open window. The dispatcher's
// single goroutine calls these methods in sequence, so no locking is needed.

// laneKey identifies an arb lane by the good sold and the sink it is sold into.
// Two hulls sharing a laneKey in one recovery window is exactly the collision.
type laneKey struct {
	good string
	sink string
}

// laneLease is one launched leg's hold on its lane. While the leg's container is
// still claimed to hull it is in flight (flying); once the dispatcher observes
// the container gone, the lease flips to terminated and holds until heldUntil.
type laneLease struct {
	hull        string
	containerID string
	terminated  bool
	heldUntil   time.Time // meaningful only once terminated
}

// laneMutex tracks which (good, sink) lanes are currently held so the dispatcher
// never launches two hulls onto one sink in a recovery window. Not safe for
// concurrent use; the dispatcher's Run goroutine drives it serially.
type laneMutex struct {
	clock        shared.Clock
	recoveryHold time.Duration
	leases       map[laneKey]*laneLease
}

// newLaneMutex returns a tracker holding lanes for recoveryHold after their leg
// terminates. A non-positive hold is clamped to zero (in-flight legs still block;
// a terminated lane frees immediately) so a misconfig can never hold forever.
func newLaneMutex(clock shared.Clock, recoveryHold time.Duration) *laneMutex {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	if recoveryHold < 0 {
		recoveryHold = 0
	}
	return &laneMutex{
		clock:        clock,
		recoveryHold: recoveryHold,
		leases:       make(map[laneKey]*laneLease),
	}
}

// noteLaunch records that hull just launched a leg (container containerID) onto
// key, marking the lane in flight. Called on every successful dispatch, so the
// very next candidate this pass already sees the lane held (within-pass dedupe).
func (m *laneMutex) noteLaunch(key laneKey, hull, containerID string) {
	m.leases[key] = &laneLease{hull: hull, containerID: containerID}
}

// reconcile observes leg terminations from the fleet's live ship→container map
// (symbol → current container id, "" when idle/unassigned) and advances lease
// state: an in-flight lease whose hull no longer carries its container id has
// terminated — it starts the recovery hold. Terminated leases whose hold has
// elapsed are dropped so the map cannot grow without bound. Called once at the
// top of every dispatch pass, before any lane-held check.
func (m *laneMutex) reconcile(shipContainerIDs map[string]string) {
	now := m.clock.Now()
	for key, lease := range m.leases {
		if !lease.terminated {
			if shipContainerIDs[lease.hull] != lease.containerID {
				// The leg's container is gone (hull released to idle, re-claimed by a
				// contract, or the hull vanished): the leg terminated. Start the hold.
				lease.terminated = true
				lease.heldUntil = now.Add(m.recoveryHold)
			}
			continue
		}
		if !now.Before(lease.heldUntil) {
			delete(m.leases, key)
		}
	}
}

// held reports whether key may not be dispatched right now: a leg is in flight on
// it, or its post-termination recovery hold has not yet elapsed.
func (m *laneMutex) held(key laneKey) bool {
	lease, ok := m.leases[key]
	if !ok {
		return false
	}
	if !lease.terminated {
		return true
	}
	return m.clock.Now().Before(lease.heldUntil)
}

// describe returns the holder and, for a terminated-but-held lane, when it frees
// (flying==true means the leg is still in the air, so the free time is not yet
// known). Used only to enrich the per-candidate verdict log for a held lane; it
// reads no state the held check does not.
func (m *laneMutex) describe(key laneKey) (hull string, freesAt time.Time, flying bool) {
	lease, ok := m.leases[key]
	if !ok {
		return "", time.Time{}, false
	}
	return lease.hull, lease.heldUntil, !lease.terminated
}
