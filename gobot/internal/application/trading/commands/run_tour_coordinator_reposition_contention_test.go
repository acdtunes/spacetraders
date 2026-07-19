package commands

// run_tour_coordinator_reposition_contention_test.go — sp-lq64 (epic sp-g9td): contention-aware
// reposition on the DEFAULT margins-death path. The pre-flight ALREADY nets the fleet-wide
// absorption ledger read-only (planAtCandidate → planForState → assembleAbsorption), so a sink
// that is ALREADY saturated at pre-flight time is planned around. The residual bug the sp-fmxp
// trace saw is a TOCTOU thundering herd: N simultaneously margins-dead hulls each release their
// old reservation, deadhead 10+ min toward the same not-yet-reserved sink, and all but one breach
// the fleet-wide sink cap on arrival — because a hull mid-deadhead has NO ledger presence yet.
//
// R2 fix (reuse the shipped sp-uf64 anti-herd machinery, NOT new per-sink TTL state):
//   (1) both reposition COMMITs now register their in-flight target in pendingRelocationsBySystem
//       (incrementPendingRelocation, released by a deferred decrement on the synchronous jump),
//   (2) the default margins-death path now runs excludeHerdedSystems (until now only the reach +
//       rate-floor paths did), so a concurrent margins-dead hull sees the in-flight mover and does
//       not pile onto the same over-subscribed system.
// Strictly stricter (RULINGS #4): only REMOVES over-subscribed candidates; the 25000 floor and
// every money guard are untouched, and it fails open when the fleet snapshot is unreadable.

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/placement"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// repositionHerdPlanner returns the standard margins-death planFn: home X1-S1 tours once then dies
// (3-strike), and X1-S2 clears the reposition floor (roundTripS2, fresh 100k >> 25k) whenever the
// ranking or the post-jump re-plan prices it — so the ONLY thing that can stop a jump to X1-S2 is
// the herd exclusion, not an infeasible destination.
func repositionHerdPlanner() *tourFakeRoutingClient {
	homeCalls, s2Calls := 0, 0
	return &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-S1":
			homeCalls++
			if homeCalls == 1 {
				return roundTripS1()
			}
			return infeasibleTour()
		case "X1-S2":
			s2Calls++
			if s2Calls <= 2 {
				return roundTripS2()
			}
			return infeasibleTour()
		}
		return infeasibleTour()
	}}
}

// THE default-path anti-herd gate (the core sp-lq64 fix). With RepositionReachEnabled OFF (the
// default margins-death path), a candidate system already saturated with active trade hulls must be
// EXCLUDED from the ranking so the hull does NOT deadhead into a contended sink — whereas an
// un-saturated candidate of the same projected margin IS chosen. The cap is pinned to 1, so a single
// landed trade hull saturates X1-S2. The un-saturated control proves the exclusion is specifically
// the herd, not an unrelated stall.
func TestReposition_MarginsDeath_DefaultPathExcludesHerdedSystem(t *testing.T) {
	cases := []struct {
		name        string
		activeHulls []activeHull
		wantJump    bool
	}{
		{"un-saturated candidate is chosen (control)", nil, true},
		{"herd-saturated candidate is excluded — no deadhead into the contended sink", []activeHull{{"X1-S2", tradeFleet}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := repositionFixture()
			fx.activeHulls = tc.activeHulls
			h := newTourHandler(t, fx, repositionHerdPlanner(), &tourFakeTelemetry{})

			resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
				ShipSymbol: "TOUR-HERD", PlayerID: 1, ContainerID: "ctr-herd", Iterations: -1,
				RepositionReachMaxHullsPerSystem: 1, // cap: one landed trade hull saturates X1-S2
				ModelArtifactPath:                writeTourArtifact(t),
			})
			if err != nil {
				t.Fatalf("reposition run returned error: %v", err)
			}
			r := tourResponse(t, resp)

			if tc.wantJump {
				if len(fx.jumps) != 1 || fx.jumps[0] != "X1-S2" {
					t.Fatalf("un-saturated: expected exactly one jump to X1-S2, got jumps=%v repositions=%d", fx.jumps, r.Repositions)
				}
			} else if len(fx.jumps) != 0 || r.Repositions != 0 {
				t.Fatalf("herd-saturated: X1-S2 must be excluded on the DEFAULT path — expected NO jump, got jumps=%v repositions=%d", fx.jumps, r.Repositions)
			}
		})
	}
}

// THE convergence scenario (two hulls, same tick). Hull A is already mid-deadhead toward X1-S2 —
// its commit registered a pending relocation — with NO landed hull there yet: the exact TOCTOU
// window where the landed count is 0 and only the in-flight registry can stop hull B piling onto
// the same sink. On the DEFAULT path, hull B must now SEE that in-flight mover and decline X1-S2
// (instead of both breaching the fleet-wide sink cap on arrival). This closes the loop Test 1
// (landed saturation) + the registration tests only prove separately: pending intent → exclusion.
func TestReposition_MarginsDeath_ConcurrentMoverBlocksSameSystem(t *testing.T) {
	fx := repositionFixture()
	h := newTourHandler(t, fx, repositionHerdPlanner(), &tourFakeTelemetry{})

	// Simulate hull A permanently in-flight to X1-S2 (no paired decrement — it never lands here),
	// with no landed hull in the system, so ONLY the pending registry can gate hull B.
	h.incrementPendingRelocation("X1-S2")

	if _, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-B", PlayerID: 1, ContainerID: "ctr-b", Iterations: -1,
		RepositionReachMaxHullsPerSystem: 1, // one in-flight mover saturates X1-S2 for a concurrent evaluator
		ModelArtifactPath:                writeTourArtifact(t),
	}); err != nil {
		t.Fatalf("reposition run returned error: %v", err)
	}

	if len(fx.jumps) != 0 {
		t.Fatalf("hull B must NOT deadhead to X1-S2 while hull A is in-flight toward it (pending herd), got jumps=%v", fx.jumps)
	}
}

// THE margins-death commit registration. The default reposition commit must publish its target
// system into pendingRelocationsBySystem BEFORE the jump (so a concurrent evaluator's herd check
// sees the mover mid-flight) and RELEASE it once the jump returns (deferred decrement). The jumpHook
// snapshots the registry DURING the flight — the only window in which the claim is observable.
func TestReposition_MarginsDeath_RegistersInFlightTarget(t *testing.T) {
	fx := repositionFixture()
	h := newTourHandler(t, fx, repositionHerdPlanner(), &tourFakeTelemetry{})

	var pendingDuringJump map[string]int
	fx.jumpHook = func() { pendingDuringJump = h.snapshotPendingRelocations() }

	if _, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-REG", PlayerID: 1, ContainerID: "ctr-reg", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	}); err != nil {
		t.Fatalf("reposition run returned error: %v", err)
	}

	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-S2" {
		t.Fatalf("precondition: expected the margins-death reposition to jump to X1-S2, got %v", fx.jumps)
	}
	if pendingDuringJump["X1-S2"] != 1 {
		t.Fatalf("the margins-death commit must register target X1-S2 in-flight (pendingRelocationsBySystem), got %v during the jump", pendingDuringJump)
	}
	if remaining := h.snapshotPendingRelocations(); len(remaining) != 0 {
		t.Fatalf("the in-flight claim must be released once the jump returns (deferred decrement), still have %v", remaining)
	}
}

// THE placement commit registration (sp-z7ng convergePlacementJump). The armed placement engine's
// jump commit must register + release its target identically, so a placement-driven mover is visible
// to the same fleet-wide herd check. Driven directly (no β telemetry needed) with a constructed
// winner — the commit path is what is under test, not the score decision.
func TestReposition_PlacementCommit_RegistersInFlightTarget(t *testing.T) {
	fx := repositionFixture()
	h := newTourHandler(t, fx, &tourFakeRoutingClient{}, &tourFakeTelemetry{})

	var pendingDuringJump map[string]int
	fx.jumpHook = func() { pendingDuringJump = h.snapshotPendingRelocations() }

	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	cmd := &RunTourCoordinatorCommand{ShipSymbol: "TOUR-PLC", PlayerID: 1, ContainerID: "ctr-plc"}
	episode := &repositionEpisode{}
	winner := &placement.Evaluation{System: "X1-S2", Waypoint: "X1-S2-A", Feasible: true}

	handled, repositioned, err := h.convergePlacementJump(
		ctx, cmd, &RunTourCoordinatorResponse{}, episode, map[string]int{}, "X1-S1", winner, int64(1_000_000), int64(0),
	)
	if err != nil {
		t.Fatalf("convergePlacementJump errored: %v", err)
	}
	if !handled || !repositioned {
		t.Fatalf("expected a committed placement jump, got handled=%v repositioned=%v", handled, repositioned)
	}
	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-S2" {
		t.Fatalf("precondition: expected a jump to the winner X1-S2, got %v", fx.jumps)
	}
	if pendingDuringJump["X1-S2"] != 1 {
		t.Fatalf("the placement commit must register target X1-S2 in-flight, got %v during the jump", pendingDuringJump)
	}
	if remaining := h.snapshotPendingRelocations(); len(remaining) != 0 {
		t.Fatalf("the placement in-flight claim must be released once the jump returns, still have %v", remaining)
	}
}
