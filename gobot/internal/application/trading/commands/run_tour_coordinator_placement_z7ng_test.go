package commands

// run_tour_coordinator_placement_z7ng_test.go — sp-z7ng (epic sp-fguo Layer-B): white-box tests of
// the armed placement/relocation scoring loop through the driving Handle port. RED 8-17 of the
// brief. (The two BLACK-BOX acceptance scenarios live in run_tour_coordinator_placement_test.go,
// owned by sp-f1yk — this file must not collide with them.)
//
// Every armed test seeds telemetry so β (fleet-median tour $/hr) is readable, drives a continuous
// margins-death, and asserts the observable outcome (jump target / stay / hold / fallback) plus the
// greppable decision log. A pre-flight call is identified by ship.Cargo == nil (planAtCandidate
// clears the hold), which cleanly separates the reposition evaluation from the loop's own re-plans.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// tlegRow builds one telemetry leg for β seeding (100 units at the given realized price).
func tlegRow(tourID string, isBuy bool, price int, planned, realized time.Time) trading.TourLegTelemetry {
	return trading.TourLegTelemetry{
		TourID: tourID, ShipSymbol: "SEED", IsBuy: isBuy, RealizedUnits: 100, RealizedUnitPrice: price,
		PlannedAt: planned, RealizedAt: realized, PlayerID: 1,
	}
}

// betaSeedRows builds telemetry MedianTourRate reads as the given per-hour rates — one 1h-span tour
// each (buy 100@1000 then sell 100@sell over 1h ⇒ net = 100·sell − 100·1000 = rate). Used with the
// default tourFakeTelemetry, which returns all rows regardless of the since bound.
func betaSeedRows(ratesPerHour ...int) []trading.TourLegTelemetry {
	base := time.Now().Add(-30 * time.Minute)
	rows := make([]trading.TourLegTelemetry, 0, 2*len(ratesPerHour))
	for i, rate := range ratesPerHour {
		id := fmt.Sprintf("bseed-%d", i)
		sell := (rate + 100000) / 100 // net = 100·sell − 100000 = rate, realized over a 1h span
		rows = append(rows, tlegRow(id, true, 1000, base, base), tlegRow(id, false, sell, base, base.Add(time.Hour)))
	}
	return rows
}

// feasiblePlan is a pre-flight-only plan carrying an explicit projected $/hr (E_x). It has no legs
// because the pre-flight discards everything but Feasible + ProjectedCreditsPerHour.
func feasiblePlan(cph float64, profit int64) *routing.TourPlan {
	return &routing.TourPlan{Feasible: true, ProjectedProfit: profit, ProjectedCreditsPerHour: cph}
}

// placementPlan additionally carries executable H buy@100/sell@300 legs so a post-jump re-plan at
// the winner runs against the fixture's H lane.
func placementPlan(systemSymbol string, cph float64, profit int64) *routing.TourPlan {
	return &routing.TourPlan{Feasible: true, ProjectedProfit: profit, ProjectedCreditsPerHour: cph, Legs: []routing.TourLeg{
		leg(systemSymbol+"-A", systemSymbol, buy("H", 40, 100)),
		leg(systemSymbol+"-B", systemSymbol, sell("H", 40, 300)),
	}}
}

// seededTelemetry is a READ-ONLY β stub: ListByPlayer returns the pre-seeded rows and RecordLeg is a
// no-op. The no-op matters — the coordinator records the RUNNING tour's legs during the productive
// tour, and the fake executes tours instantly (near-zero wall-clock span ⇒ an astronomical rate),
// which would pollute β. Isolating the β READ from that write artifact lets each test control β
// exactly (recording is exercised by the telemetry tests elsewhere, not here).
type seededTelemetry struct {
	rows []trading.TourLegTelemetry
}

func (s *seededTelemetry) RecordLeg(_ context.Context, _ trading.TourLegTelemetry) error { return nil }
func (s *seededTelemetry) ListByPlayer(_ context.Context, _ int, _ time.Time) ([]trading.TourLegTelemetry, error) {
	return s.rows, nil
}

// windowedTelemetry honours the ListByPlayer since bound (realized_at >= since), like the real
// repository, and records every since it was called with — so a test can prove production passes a
// bounded trailing window rather than the zero time. RecordLeg is a no-op (same β-isolation reason
// as seededTelemetry).
type windowedTelemetry struct {
	rows      []trading.TourLegTelemetry
	sinceSeen []time.Time
}

func (w *windowedTelemetry) RecordLeg(_ context.Context, _ trading.TourLegTelemetry) error {
	return nil
}

func (w *windowedTelemetry) ListByPlayer(_ context.Context, _ int, since time.Time) ([]trading.TourLegTelemetry, error) {
	w.sinceSeen = append(w.sinceSeen, since)
	var out []trading.TourLegTelemetry
	for _, r := range w.rows {
		if !r.RealizedAt.Before(since) {
			out = append(out, r)
		}
	}
	return out, nil
}

// RED#8 — the default-safety proof, byte-identical to legacy. A zero-value command (exactly what
// buildTourCoordinatorCommand produces for every existing container: PlacementScoreEnabled=false)
// driving the margins-death fixture must reproduce the LEGACY reposition outcome and emit the LEGACY
// ranking log — with NO placement decision line and NO placement fallback line. The dispatch is
// never taken when unarmed.
func TestTour_PlacementDefaultOff_LegacyRepositionUnchanged(t *testing.T) {
	fx := repositionFixture()
	homeCalls, s2Calls := 0, 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
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
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	logger := &tradeCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-DEFOFF", PlayerID: 1, ContainerID: "ctr-defoff", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t), // PlacementScoreEnabled defaults FALSE
	})
	if err != nil {
		t.Fatalf("default-off run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 1 || r.ToursCompleted != 2 || r.ExitReason != tourExitStarvation {
		t.Fatalf("default-off must reproduce the legacy reposition outcome (1 reposition, 2 tours, starvation), got %+v", r)
	}
	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-S2" {
		t.Fatalf("legacy reposition must jump to X1-S2, got %v", fx.jumps)
	}
	if shared.ExtractSystemSymbol(fx.location) != "X1-S2" {
		t.Fatalf("hull must end at X1-S2, got %q", fx.location)
	}
	if !logger.loggedContaining("Reposition ranking from") {
		t.Fatalf("default-off must emit the LEGACY ranking log:\n%s", strings.Join(logger.messages, "\n"))
	}
	if logger.loggedContaining("Placement decision") || logger.loggedContaining("Placement:") {
		t.Fatalf("default-off must NOT run the placement engine (no placement log line):\n%s", strings.Join(logger.messages, "\n"))
	}
}

// RED#9 — the kill-switch wins over placement arming (resolves the major finding). PlacementScoreEnabled
// AND RepositionDisabled both true ⇒ (false,nil): no jump, no ranking, NO placement pre-flight, no
// placement log. The reposition_disabled guard sits ABOVE the placement dispatch, so an armed daemon
// keeps the muscle-memory stop.
func TestTour_PlacementArmed_KillSwitchStillWins(t *testing.T) {
	fx := repositionFixture()
	homeCalls := 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		if ship.CurrentSystem == "X1-S1" {
			homeCalls++
			if homeCalls == 1 {
				return roundTripS1()
			}
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &seededTelemetry{rows: betaSeedRows(150000)})
	logger := &tradeCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-KILL", PlayerID: 1, ContainerID: "ctr-kill", Iterations: -1,
		PlacementScoreEnabled: true, RepositionDisabled: true, // armed but killed
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("kill-switch run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 0 || len(fx.jumps) != 0 {
		t.Fatalf("reposition_disabled must win over placement arming — no jump, got %d repositions / jumps %v", r.Repositions, fx.jumps)
	}
	if plannerVisitedSystem(planner.positions, "X1-S2") {
		t.Fatalf("the kill-switch must return BEFORE any placement pre-flight (no planner call at a candidate), positions=%v", planner.positions)
	}
	if logger.loggedContaining("Placement decision") || logger.loggedContaining("Placement:") {
		t.Fatalf("an armed-but-killed daemon must emit NO placement activity:\n%s", strings.Join(logger.messages, "\n"))
	}
	if r.ToursCompleted != 1 || r.ExitReason != tourExitStarvation {
		t.Fatalf("expected the pre-sp-zhii honest exit, got tours=%d reason=%q", r.ToursCompleted, r.ExitReason)
	}
}

// RED#10 — the armed engine JUMPS to the score argmax, with the units-finding observability and the
// persist-before-jump ordering. Two 1-hop candidates: X1-S2 (E_x=200k) outscores X1-S3 (E_x=100k) at
// equal deadhead, so the hull jumps to X1-S2; the decision log carries β, φ·β, and per-candidate raw
// E_x/hops/D_x/score separately; the in-flight destination is persisted before the jump and cleared
// after.
func TestTour_PlacementArmed_JumpsToScoreArgmax(t *testing.T) {
	fx := repositionFixture()
	fx.markets["X1-S3"] = []string{"X1-S3-A", "X1-S3-B"}
	fx.ask["X1-S3-A"] = map[string]int{"H": 100}
	fx.ask["X1-S3-B"] = map[string]int{"H": 300}
	fx.bid["X1-S3-B"] = map[string]int{"H": 300}
	fx.tv["X1-S3-A"] = map[string]int{"H": 1000}
	fx.tv["X1-S3-B"] = map[string]int{"H": 1000}
	fx.neighbors["X1-S1"] = []string{"X1-S2", "X1-S3"}

	homeCalls, s2Calls := 0, 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-S1":
			if ship.Cargo == nil {
				return infeasibleTour() // E_s: home is dead even clean-hold
			}
			homeCalls++
			if homeCalls == 1 {
				return roundTripS1()
			}
			return infeasibleTour()
		case "X1-S2":
			s2Calls++
			if s2Calls <= 2 { // pre-flight (E_x=200k) + the post-jump productive re-plan
				return placementPlan("X1-S2", 200000, 200000)
			}
			return infeasibleTour()
		case "X1-S3":
			return feasiblePlan(100000, 100000) // lower E_x → loses the argmax
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &seededTelemetry{rows: betaSeedRows(150000)})
	persister := &fakeRepositionPersister{}
	h.SetRepositionPersister(persister)
	logger := &tradeCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-ARGMAX", PlayerID: 1, ContainerID: "ctr-argmax", Iterations: -1,
		PlacementScoreEnabled: true, ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("argmax run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 1 {
		t.Fatalf("the armed engine must jump exactly once to the score argmax, got %d (%+v)", r.Repositions, r)
	}
	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-S2" {
		t.Fatalf("the jump must target the score argmax X1-S2, got %v", fx.jumps)
	}
	if !logger.loggedContaining("Placement decision", "jump X1-S2") {
		t.Fatalf("the decision log must name the jump verdict:\n%s", strings.Join(logger.messages, "\n"))
	}
	// Units-finding observability: β, φ·β floor, and per-candidate raw E_x / hops / D_x / score
	// each appear SEPARATELY on the one decision line.
	if !logger.loggedContaining("Placement decision", "beta=", "park-floor", "hops=", "ex=", "d=", "score=") {
		t.Fatalf("the decision log must carry raw β, φ·β, and per-candidate E_x/hops/D_x/score:\n%s", strings.Join(logger.messages, "\n"))
	}
	// persist-before-jump ordering (RULINGS #2): the in-flight destination is committed, then cleared.
	var committed, clearedAfter bool
	for _, ep := range persister.recorded() {
		if ep.InProgress && ep.TargetSystem == "X1-S2" {
			committed = true
		}
		if committed && !ep.InProgress {
			clearedAfter = true
		}
	}
	if !committed || !clearedAfter {
		t.Fatalf("the placement jump must persist the in-flight destination before jumping and clear it after, got %+v", persister.recorded())
	}
}

// RED#11 — the deadhead charge is PER-HOP (drives the hops threading through the sp-jeou broadening).
// A 3-hop candidate with a slightly RICHER raw E_x loses to a 2-hop candidate once each is charged
// hops·crossSystemHopSeconds: the winner is the nearer ground. If the BFS depth were discarded (all
// candidates charged 1 hop) the richer far ground would wrongly win.
func TestTour_PlacementArmed_MultiHopCandidateChargedPerHop(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{
			"X1-S1": {"X1-S1-A", "X1-S1-B"},
			// X1-MID: barren (no market) — forces the sp-jeou broadening past it
			"X1-NR": {"X1-NR-A", "X1-NR-B"}, // depth 2
			"X1-FR": {"X1-FR-A", "X1-FR-B"}, // depth 3
		},
		bid: map[string]map[string]int{"X1-S1-B": {"G": 200}, "X1-NR-B": {"H": 300}, "X1-FR-B": {"H": 300}},
		ask: map[string]map[string]int{
			"X1-S1-A": {"G": 100}, "X1-S1-B": {"G": 200},
			"X1-NR-A": {"H": 100}, "X1-NR-B": {"H": 300},
			"X1-FR-A": {"H": 100}, "X1-FR-B": {"H": 300},
		},
		tv: map[string]map[string]int{
			"X1-S1-A": {"G": 1000}, "X1-S1-B": {"G": 1000},
			"X1-NR-A": {"H": 1000}, "X1-NR-B": {"H": 1000},
			"X1-FR-A": {"H": 1000}, "X1-FR-B": {"H": 1000},
		},
		neighbors: map[string][]string{},
	}
	homeCalls, nrCalls := 0, 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-S1":
			if ship.Cargo == nil {
				return infeasibleTour() // E_s: home dead
			}
			homeCalls++
			if homeCalls == 1 {
				return roundTripS1()
			}
			return infeasibleTour()
		case "X1-NR":
			// Feasible ONLY for the pre-flight (E_x=120k) so NR wins the argmax; it then dies on
			// arrival (no productive tour), so episode.repositioned stays set and the run makes
			// exactly ONE reposition — the first jump, whose target is under assertion.
			nrCalls++
			if nrCalls == 1 {
				return feasiblePlan(120000, 200000) // 2 hops, lower raw E_x
			}
			return infeasibleTour()
		case "X1-FR":
			return feasiblePlan(125000, 200000) // 3 hops, HIGHER raw E_x — but charged 3× deadhead
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &seededTelemetry{rows: betaSeedRows(150000)})
	h.SetGateGraph(&fakeGateGraph{
		edges: map[string][]system.GateEdge{
			"X1-S1":  {{ConnectedSystem: "X1-MID", GateWaypoint: "X1-MID-GATE"}},
			"X1-MID": {{ConnectedSystem: "X1-NR", GateWaypoint: "X1-NR-GATE"}},
			"X1-NR":  {{ConnectedSystem: "X1-FR", GateWaypoint: "X1-FR-GATE"}},
		},
		repositionPath: []string{"X1-S1", "X1-MID", "X1-NR"},
	})
	logger := &tradeCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-HOPS", PlayerID: 1, ContainerID: "ctr-hops", Iterations: -1,
		PlacementScoreEnabled: true, ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("multi-hop run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 1 {
		t.Fatalf("expected exactly one reposition to the per-hop-charged winner, got %d (%+v)", r.Repositions, r)
	}
	if !containsSystem(fx.jumps, "X1-NR") || containsSystem(fx.jumps, "X1-FR") {
		t.Fatalf("the 2-hop nearer ground X1-NR must win over the richer 3-hop X1-FR, jumps=%v", fx.jumps)
	}
	if !logger.loggedContaining("Placement decision", "jump X1-NR") {
		t.Fatalf("the winner must be the nearer ground X1-NR:\n%s", strings.Join(logger.messages, "\n"))
	}
	// The far candidate was evaluated at hops=3 — proof the BFS depth threaded through to the charge
	// (had it been charged 1 hop, its richer E_x would have won).
	if !logger.loggedContaining("X1-FR(hops=3") {
		t.Fatalf("the far candidate must be charged its BFS depth (hops=3):\n%s", strings.Join(logger.messages, "\n"))
	}
}

// RED#12 — STAY exits honestly. The current-system E_s (a rich clean-hold, feasible where the laden
// 3-strike was not) beats every charged foreign candidate, so the hull holds its ground: no jump,
// repositioned=false, honest starvation exit, verdict stay.
func TestTour_PlacementArmed_StayWins_ExitsHonestlyNamingStay(t *testing.T) {
	fx := repositionFixture()
	homeCalls := 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-S1":
			if ship.Cargo == nil {
				return feasiblePlan(300000, 300000) // E_s: the ground is still richly tourable clean-hold
			}
			homeCalls++
			if homeCalls == 1 {
				return roundTripS1()
			}
			return infeasibleTour() // laden 3-strike
		case "X1-S2":
			return feasiblePlan(100000, 100000) // foreign, lower score
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &seededTelemetry{rows: betaSeedRows(150000)})
	logger := &tradeCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-STAY", PlayerID: 1, ContainerID: "ctr-stay", Iterations: -1,
		PlacementScoreEnabled: true, ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("stay run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 0 || len(fx.jumps) != 0 {
		t.Fatalf("a stay-wins decision must not jump, got %d repositions / jumps %v", r.Repositions, fx.jumps)
	}
	if shared.ExtractSystemSymbol(fx.location) != "X1-S1" {
		t.Fatalf("the hull must hold its current ground X1-S1, got %q", fx.location)
	}
	if r.ExitReason != tourExitStarvation {
		t.Fatalf("a stay flows to the honest starvation exit, got %q", r.ExitReason)
	}
	if !logger.loggedContaining("Placement decision", "stay X1-S1") {
		t.Fatalf("the decision log must name the stay verdict:\n%s", strings.Join(logger.messages, "\n"))
	}
}

// RED#13 — the park floor HOLDS. With a hot fleet β, φ·β sits above every candidate's charged score
// (and above the current-system E_s), so the hull parks rather than chase a marginal jump: no jump,
// verdict hold_park_floor.
func TestTour_PlacementArmed_ParkFloorHolds(t *testing.T) {
	fx := repositionFixture()
	homeCalls := 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-S1":
			if ship.Cargo == nil {
				return feasiblePlan(50000, 50000) // E_s: below the hot-fleet floor
			}
			homeCalls++
			if homeCalls == 1 {
				return roundTripS1()
			}
			return infeasibleTour()
		case "X1-S2":
			return feasiblePlan(100000, 100000) // charged score goes negative under a 1M/hr β
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &seededTelemetry{rows: betaSeedRows(1000000)})
	logger := &tradeCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-HOLD", PlayerID: 1, ContainerID: "ctr-hold", Iterations: -1,
		PlacementScoreEnabled: true, ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("park-floor run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 0 || len(fx.jumps) != 0 {
		t.Fatalf("a below-park-floor fleet must HOLD, got %d repositions / jumps %v", r.Repositions, fx.jumps)
	}
	if !logger.loggedContaining("Placement decision", "hold_park_floor") {
		t.Fatalf("the decision log must name the park-floor hold:\n%s", strings.Join(logger.messages, "\n"))
	}
}

// RED#14 — an unreadable β FALLS BACK to the legacy engine (fresh-boot rescue preserved). Empty
// telemetry AND a nil telemetry repo both leave β unreadable, so the untouched legacy static-floor
// reposition runs for the episode and still rescues the hull to the fresh ground.
func TestTour_PlacementArmed_BetaUnreadable_FallsBackToLegacy(t *testing.T) {
	cases := []struct {
		name string
		tel  trading.TourTelemetryRepository
	}{
		{"empty telemetry", &seededTelemetry{}},
		{"nil telemetry repo", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := repositionFixture()
			homeCalls, s2Calls := 0, 0
			planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
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
			h := newTourHandler(t, fx, planner, tc.tel)
			logger := &tradeCaptureLogger{}
			ctx := common.WithLogger(context.Background(), logger)

			resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
				ShipSymbol: "TOUR-FB", PlayerID: 1, ContainerID: "ctr-fb", Iterations: -1,
				PlacementScoreEnabled: true, ModelArtifactPath: writeTourArtifact(t),
			})
			if err != nil {
				t.Fatalf("fallback run returned error: %v", err)
			}
			r := tourResponse(t, resp)

			if r.Repositions != 1 {
				t.Fatalf("β unreadable must fall back to the legacy reposition (rescue preserved), got %d repositions", r.Repositions)
			}
			if len(fx.jumps) != 1 || fx.jumps[0] != "X1-S2" {
				t.Fatalf("the legacy fallback must jump to X1-S2, got %v", fx.jumps)
			}
			if !logger.loggedContaining("Reposition ranking from") {
				t.Fatalf("the LEGACY ranking log must appear on fallback:\n%s", strings.Join(logger.messages, "\n"))
			}
			if !logger.loggedContaining("falling back to the legacy") {
				t.Fatalf("a fallback_legacy line must name the fall-back:\n%s", strings.Join(logger.messages, "\n"))
			}
		})
	}
}

// RED#15 — the β window EXCLUDES old tours. A 60-min trailing window drops a stale 1M/hr tour so β is
// the recent 50k/hr; the candidate then clears φ·β and the hull jumps. Had the old tour leaked in, β
// would spike and the candidate would fall below φ·β (a hold). A jump therefore proves the since
// bound excluded the old rows — and the bound is ~now−60min, never the zero time.
func TestTour_PlacementArmed_BetaWindowExcludesOldTours(t *testing.T) {
	now := time.Now()
	tel := &windowedTelemetry{}
	recentBuy, recentSell := now.Add(-40*time.Minute), now.Add(-10*time.Minute)
	tel.rows = append(tel.rows,
		tlegRow("recent", true, 1000, recentBuy, recentBuy),
		tlegRow("recent", false, 1250, recentBuy, recentSell), // net 25000 over 0.5h = 50000/hr
	)
	oldBuy, oldSell := now.Add(-140*time.Minute), now.Add(-110*time.Minute)
	tel.rows = append(tel.rows,
		tlegRow("old", true, 1000, oldBuy, oldBuy),
		tlegRow("old", false, 6000, oldBuy, oldSell), // net 500000 over 0.5h = 1,000,000/hr (if it leaked in)
	)

	fx := repositionFixture()
	homeCalls, s2Calls := 0, 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-S1":
			if ship.Cargo == nil {
				return infeasibleTour() // E_s: home dead
			}
			homeCalls++
			if homeCalls == 1 {
				return roundTripS1()
			}
			return infeasibleTour()
		case "X1-S2":
			s2Calls++
			if s2Calls <= 2 {
				return placementPlan("X1-S2", 100000, 100000)
			}
			return infeasibleTour()
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, tel)

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-WIN", PlayerID: 1, ContainerID: "ctr-win", Iterations: -1,
		PlacementScoreEnabled: true, ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("window run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 1 {
		t.Fatalf("the window must exclude the old 1M/hr tour → low β → jump; got %d repositions", r.Repositions)
	}
	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-S2" {
		t.Fatalf("expected a jump to X1-S2, got %v", fx.jumps)
	}
	if len(tel.sinceSeen) == 0 {
		t.Fatalf("ListByPlayer must be called with a trailing-window since bound")
	}
	since := tel.sinceSeen[0]
	if since.Before(now.Add(-61*time.Minute)) || since.After(now.Add(-59*time.Minute)) {
		t.Fatalf("since bound = %v, want ~now−60min (%v)", since, now.Add(-60*time.Minute))
	}
}

// RED#16 — the 75-min staleness gate excludes stale candidates from the shortlist (the armed path
// shares freshListings verbatim). A candidate whose only listings are >75min old is dropped in
// discovery, so it never reaches a pre-flight — the planner is never asked to price it.
func TestTour_PlacementArmed_StalenessGateExcludesStaleListings(t *testing.T) {
	fx := repositionFixture()
	fx.staleMarkets = map[string]bool{"X1-S2-A": true, "X1-S2-B": true} // S2's only listings are >75min stale
	homeCalls := 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		if ship.CurrentSystem == "X1-S1" {
			if ship.Cargo == nil {
				return infeasibleTour() // E_s: home dead
			}
			homeCalls++
			if homeCalls == 1 {
				return roundTripS1()
			}
			return infeasibleTour()
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &seededTelemetry{rows: betaSeedRows(150000)})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-STALE", PlayerID: 1, ContainerID: "ctr-stale", Iterations: -1,
		PlacementScoreEnabled: true, ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("staleness run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if plannerVisitedSystem(planner.positions, "X1-S2") {
		t.Fatalf("a candidate whose only listings are >75min stale must never reach the pre-flight shortlist, positions=%v", planner.positions)
	}
	if r.Repositions != 0 || len(fx.jumps) != 0 {
		t.Fatalf("no fresh candidate → no jump, got %d repositions / jumps %v", r.Repositions, fx.jumps)
	}
}

// RED#17 — the armed solver budget EQUALS legacy's K (resolves the perf finding / no thundering herd).
// Over four foreign candidates, the reposition-episode planner-call count is the same armed as legacy
// (top-(N−1) foreign + E_s = N = K = 3), and the placement_shortlist_top_n override widens it to 5.
// Counted against a kill-switched control baseline so the assertion never hard-codes the loop's own
// productive+strike calls.
func TestTour_PlacementArmed_SolverBudgetMatchesLegacy(t *testing.T) {
	budgetFixture := func() *tourFixture {
		fx := repositionFixture()
		for _, s := range []string{"X1-S3", "X1-S4", "X1-S5"} {
			fx.markets[s] = []string{s + "-A", s + "-B"}
			fx.ask[s+"-A"] = map[string]int{"H": 100}
			fx.ask[s+"-B"] = map[string]int{"H": 300}
			fx.bid[s+"-B"] = map[string]int{"H": 300}
			fx.tv[s+"-A"] = map[string]int{"H": 1000}
			fx.tv[s+"-B"] = map[string]int{"H": 1000}
		}
		fx.neighbors["X1-S1"] = []string{"X1-S2", "X1-S3", "X1-S4", "X1-S5"}
		return fx
	}
	// Candidates feasible but below the legacy 25k floor AND (under a 1M/hr β) below φ·β, so NOTHING
	// jumps in any mode — the reposition episode runs its evaluations then exits, isolating the count.
	newPlanner := func() *tourFakeRoutingClient {
		homeCalls := 0
		return &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
			if ship.CurrentSystem == "X1-S1" {
				if ship.Cargo == nil {
					return infeasibleTour() // E_s
				}
				homeCalls++
				if homeCalls == 1 {
					return roundTripS1()
				}
				return infeasibleTour()
			}
			return feasiblePlan(1000, 1000) // feasible, below both floors
		}}
	}
	runCalls := func(cmd *RunTourCoordinatorCommand) int {
		planner := newPlanner()
		h := newTourHandler(t, budgetFixture(), planner, &seededTelemetry{rows: betaSeedRows(1000000)})
		if _, err := h.Handle(context.Background(), cmd); err != nil {
			t.Fatalf("budget run %q returned error: %v", cmd.ShipSymbol, err)
		}
		return planner.calls
	}

	base := func(ship string) *RunTourCoordinatorCommand {
		return &RunTourCoordinatorCommand{ShipSymbol: ship, PlayerID: 1, ContainerID: "ctr-" + ship, Iterations: -1, ModelArtifactPath: writeTourArtifact(t)}
	}
	control := base("CTRL")
	control.RepositionDisabled = true // kill-switch: baseline = the loop's own productive+strike calls only
	legacy := base("LEG")
	armed := base("ARM")
	armed.PlacementScoreEnabled = true
	armedWide := base("ARM5")
	armedWide.PlacementScoreEnabled = true
	armedWide.PlacementShortlistTopN = 5

	controlCalls := runCalls(control)
	legacyCalls := runCalls(legacy)
	armedCalls := runCalls(armed)
	armedWideCalls := runCalls(armedWide)

	if legacyCalls-controlCalls != 3 {
		t.Fatalf("legacy must price K=3 candidates per episode, got %d", legacyCalls-controlCalls)
	}
	if armedCalls-controlCalls != 3 {
		t.Fatalf("armed must price N=3 (top-2 foreign + E_s) per episode — same budget as legacy, got %d", armedCalls-controlCalls)
	}
	if armedCalls != legacyCalls {
		t.Fatalf("the same-budget rule: armed (%d) must equal legacy (%d) — arming cannot grow the solver herd", armedCalls, legacyCalls)
	}
	if armedWideCalls-controlCalls != 5 {
		t.Fatalf("placement_shortlist_top_n=5 must price 5 (top-4 foreign + E_s), got %d", armedWideCalls-controlCalls)
	}
}
