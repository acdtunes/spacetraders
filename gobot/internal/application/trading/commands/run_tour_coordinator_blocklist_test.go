package commands

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// sp-o4wa — noise-goods blocklist. The economy-analyst's 12h matched-P&L found FUEL
// (11.2 cr/u), ALUMINUM (63.7 cr/u), and PLASTICS (65.2 cr/u) are sub-70-cr/u trades
// whose per-leg dock+dwell tempo cost exceeds the cargo value — pure tempo drag on a
// tour objective already significantly negative on $/hr. The tour coordinator therefore
// filters a config-driven good blocklist out of the market snapshot BEFORE it reaches
// the solver, so a blocklisted good is never chosen as tour cargo (neither a buy source
// nor a sell sink). Mirrors the contract pre_positioning.blocklist mechanism: a global
// config list injected into the handler via a setter, defaulting EMPTY (byte-identical)
// so arming is an explicit config edit.
//
// EXEMPTION: this is FUEL-as-tradeable-CARGO only. Ship refueling is a wholly separate
// command path (RefuelShipHandler -> API RefuelShip / ship.Refuel), which never consults
// the tour snapshot — see TestTourCargoBlocklist_SurgicalScope_RefuelUnaffected.

func snapshotGoods(rows []routing.TourGoodSnapshot) map[string]bool {
	got := map[string]bool{}
	for _, r := range rows {
		got[r.Good] = true
	}
	return got
}

// blocklistFixture: one system with a single market hosting all four goods. The three
// noise goods plus a genuine arb good (IRON_ORE) so a test can prove the blocklist drops
// exactly the named goods and keeps everything else.
func blocklistFixture() *tourFixture {
	wp := "X1-BL1-A"
	return &tourFixture{
		cargo: map[string]int{}, location: wp, cargoCap: 40,
		markets: map[string][]string{"X1-BL1": {wp}},
		ask: map[string]map[string]int{wp: {
			"FUEL": 100, "ALUMINUM": 70, "PLASTICS": 72, "IRON_ORE": 120,
		}},
		bid: map[string]map[string]int{wp: {
			"FUEL": 90, "ALUMINUM": 60, "PLASTICS": 62, "IRON_ORE": 50,
		}},
		tv: map[string]map[string]int{wp: {
			"FUEL": 40, "ALUMINUM": 20, "PLASTICS": 20, "IRON_ORE": 20,
		}},
	}
}

// planOnce drives the shared plan-assembly seam (planForState) once and returns the
// snapshot the coordinator handed the planner. planForState is THE seam that builds the
// good universe (BuildTourSnapshot) and calls the solver, shared by the live tour and the
// reposition pre-flight — so filtering here proves the blocklisted good reaches neither.
func planOnce(t *testing.T, h *RunTourCoordinatorHandler, fake *tourFakeRoutingClient) []routing.TourGoodSnapshot {
	t.Helper()
	cmd := &RunTourCoordinatorCommand{PlayerID: 1}
	ship := routing.TourShipState{
		ShipSymbol: "BL-1", CurrentWaypoint: "X1-BL1-A", CurrentSystem: "X1-BL1", HoldCapacity: 40,
	}
	_, snap, _, err := h.planForState(
		context.Background(), ship, []string{"X1-BL1"}, cmd, tourPlanBudget{maxHops: 6, maxSpend: 1_000_000},
	)
	if err != nil {
		t.Fatalf("planForState: %v", err)
	}
	if len(fake.snapshots) != 1 {
		t.Fatalf("expected exactly one planner call, got %d", len(fake.snapshots))
	}
	// The snapshot the planner received MUST be the same filtered universe planForState
	// returns — no divergence between what the solver plans over and what we report.
	if !reflect.DeepEqual(snap, fake.snapshots[0]) {
		t.Fatalf("returned snapshot != snapshot handed to planner:\n return=%+v\n planner=%+v", snap, fake.snapshots[0])
	}
	return fake.snapshots[0]
}

// RED #1 (behavioral) — a blocklisted good is filtered out of the good universe the solver
// plans cargo over, while a non-blocklisted good survives. This is the core requirement:
// the solver can only trade goods present in the snapshot, so dropping them here makes a
// blocklisted good structurally unselectable as cargo.
func TestTourCargoBlocklist_ExcludesGoodsFromSolverSnapshot(t *testing.T) {
	fx := blocklistFixture()
	fake := &tourFakeRoutingClient{plans: []*routing.TourPlan{{Feasible: true}}}
	h := newTourHandler(t, fx, fake, nil)
	h.SetCargoBlocklist([]string{"FUEL", "ALUMINUM", "PLASTICS"})

	goods := snapshotGoods(planOnce(t, h, fake))

	for _, blocked := range []string{"FUEL", "ALUMINUM", "PLASTICS"} {
		if goods[blocked] {
			t.Errorf("blocklisted good %q leaked into the solver snapshot: %v", blocked, goods)
		}
	}
	if !goods["IRON_ORE"] {
		t.Errorf("non-blocklisted good IRON_ORE was dropped: %v", goods)
	}
}

// RED #2 (byte-identical) — with no blocklist configured (the default), every good rides
// into the snapshot exactly as before. Proves the feature is opt-in: an unset config is
// byte-identical to previous behavior.
func TestTourCargoBlocklist_UnsetIsByteIdentical(t *testing.T) {
	fx := blocklistFixture()
	fake := &tourFakeRoutingClient{plans: []*routing.TourPlan{{Feasible: true}}}
	h := newTourHandler(t, fx, fake, nil) // no SetCargoBlocklist call

	goods := snapshotGoods(planOnce(t, h, fake))

	for _, g := range []string{"FUEL", "ALUMINUM", "PLASTICS", "IRON_ORE"} {
		if !goods[g] {
			t.Errorf("unset blocklist must keep every good; %q missing: %v", g, goods)
		}
	}
}

// RED #3 (surgical scope / refuel exemption) — the blocklist removes ONLY the named goods
// and touches nothing else. Refueling a ship is a separate command path (RefuelShipHandler
// -> API RefuelShip) that never consults the tour snapshot, so blocklisting FUEL-as-cargo
// leaves ship refueling entirely unaffected; this test pins the surgical scope of the
// snapshot filter that guarantees it (only FUEL is dropped; ALUMINUM/PLASTICS/IRON_ORE
// remain when only FUEL is blocklisted).
func TestTourCargoBlocklist_SurgicalScope_RefuelUnaffected(t *testing.T) {
	fx := blocklistFixture()
	fake := &tourFakeRoutingClient{plans: []*routing.TourPlan{{Feasible: true}}}
	h := newTourHandler(t, fx, fake, nil)
	h.SetCargoBlocklist([]string{"FUEL"}) // FUEL-as-cargo only

	goods := snapshotGoods(planOnce(t, h, fake))

	if goods["FUEL"] {
		t.Errorf("FUEL should be dropped from tour cargo: %v", goods)
	}
	for _, kept := range []string{"ALUMINUM", "PLASTICS", "IRON_ORE"} {
		if !goods[kept] {
			t.Errorf("blocklisting FUEL must not touch %q: %v", kept, goods)
		}
	}
}

// TestFilterBlocklistedCargo_DropsBlocklistedKeepsRest unit-tests the pure filter.
func TestFilterBlocklistedCargo_DropsBlocklistedKeepsRest(t *testing.T) {
	in := []routing.TourGoodSnapshot{
		{Waypoint: "W", Good: "FUEL"},
		{Waypoint: "W", Good: "IRON_ORE"},
		{Waypoint: "W", Good: "ALUMINUM"},
		{Waypoint: "W", Good: "ADVANCED_CIRCUITRY"},
		{Waypoint: "W", Good: "PLASTICS"},
	}
	block := stringSet([]string{"FUEL", "ALUMINUM", "PLASTICS"})

	got := snapshotGoods(filterBlocklistedCargo(in, block))

	if len(got) != 2 || !got["IRON_ORE"] || !got["ADVANCED_CIRCUITRY"] {
		t.Fatalf("expected only IRON_ORE + ADVANCED_CIRCUITRY, got %v", got)
	}
}

// TestFilterBlocklistedCargo_EmptyIsNoOp proves an empty/nil blocklist is a true no-op:
// the SAME slice header is returned (zero copy), so the default path is byte-identical.
func TestFilterBlocklistedCargo_EmptyIsNoOp(t *testing.T) {
	in := []routing.TourGoodSnapshot{{Good: "FUEL"}, {Good: "IRON_ORE"}}

	for _, block := range []map[string]bool{nil, {}} {
		got := filterBlocklistedCargo(in, block)
		if len(got) != len(in) {
			t.Fatalf("empty blocklist changed length: %d != %d", len(got), len(in))
		}
		if reflect.ValueOf(got).Pointer() != reflect.ValueOf(in).Pointer() {
			t.Fatalf("empty blocklist must return the same slice (no-op), got a copy")
		}
	}
}

// The look-back opportunistic buy is a SECOND tour cargo-selection path (it reads fresh
// listings and buys to fill the hold before an empty reposition jump, bypassing the solver
// snapshot). The blocklist must bar it there too, or a blocklisted good would leak back in
// as look-back cargo. The coordinator filters the buy-source listings before building the
// look-back manifest — these tests pin both the pure filter and its manifest-level effect.

// TestFilterBlocklistedListings_DropsBlocklisted unit-tests the GoodListing filter.
func TestFilterBlocklistedListings_DropsBlocklisted(t *testing.T) {
	in := []trading.GoodListing{
		gl("FUEL", "W", "EXPORT", 40, 100, 30),
		gl("PARTS", "W", "EXPORT", 40, 100, 30),
		gl("ALUMINUM", "W", "EXPORT", 40, 100, 30),
	}
	got := filterBlocklistedListings(in, stringSet([]string{"FUEL", "ALUMINUM"}))
	if len(got) != 1 || got[0].Good != "PARTS" {
		t.Fatalf("expected only PARTS to survive, got %+v", got)
	}
	// Empty blocklist is a true no-op (same slice) — byte-identical default.
	same := filterBlocklistedListings(in, nil)
	if reflect.ValueOf(same).Pointer() != reflect.ValueOf(in).Pointer() {
		t.Fatalf("empty blocklist must return the same slice (no-op)")
	}
}

// TestLookback_BlocklistedGoodExcludedFromManifest proves the end-to-end look-back effect:
// a blocklisted good with an otherwise-clearing look-back lane is absent from the manifest
// once the buy-source listings are filtered, while a non-blocklisted good with the same
// shape still makes the manifest. The nil-blocklist control proves the FUEL lane WOULD
// exist without the blocklist — so the filter is precisely what removes it.
func TestLookback_BlocklistedGoodExcludedFromManifest(t *testing.T) {
	src := []trading.GoodListing{
		gl("FUEL", "HU21-D46", "EXPORT", 40, 100, 30),
		gl("PARTS", "HU21-D46", "EXPORT", 40, 100, 30),
	}
	dest := []trading.GoodListing{
		gl("FUEL", "UQ16-A1", "IMPORT", 300, 999, 20),
		gl("PARTS", "UQ16-A1", "IMPORT", 300, 999, 20),
	}

	// Control: without a blocklist, BOTH FUEL and PARTS clear into the manifest.
	ctrl := manifestGoods(buildLookbackManifest(filterBlocklistedListings(src, nil), dest, 100, 10))
	if !ctrl["FUEL"] || !ctrl["PARTS"] {
		t.Fatalf("control: expected both FUEL and PARTS to clear, got %v", ctrl)
	}

	// With FUEL blocklisted, only PARTS survives — FUEL is never bought as look-back cargo.
	got := manifestGoods(buildLookbackManifest(filterBlocklistedListings(src, stringSet([]string{"FUEL"})), dest, 100, 10))
	if got["FUEL"] {
		t.Errorf("blocklisted FUEL leaked into the look-back manifest: %v", got)
	}
	if !got["PARTS"] {
		t.Errorf("non-blocklisted PARTS was dropped: %v", got)
	}
}

func manifestGoods(items []lookbackItem) map[string]bool {
	got := map[string]bool{}
	for _, it := range items {
		got[it.Good] = true
	}
	return got
}

// sp-2kuyo — construction-conditional blocklist. The tour's own ADVANCED_CIRCUITRY arbitrage
// (buy X1-DY75-D37, sell X1-DY75-A4) draws from the SAME market the gate-construction pipeline
// sources ADVANCED_CIRCUITRY from directly; the two uncoordinated buyers drove the
// construction-side buy price from 5,085 to 6,712cr/u in 17 minutes. ConstructionCargoBlocklist
// unions with the unconditional CargoBlocklist ONLY while h.workSensor.ConstructionHasWork
// reports true for the player, reusing the SAME enforcement points CargoBlocklist already uses
// (effectiveCargoBlocklist, run_tour_coordinator_budget.go) rather than adding a new filter path.
//
// tourFakeCapitalWorkSensor (constructionWork/err/calls) is defined in
// run_tour_coordinator_capital_budget_test.go and reused here unmodified.

// constructionBlocklistFixture: one system hosting ADVANCED_CIRCUITRY (the good sp-2kuyo gates)
// and IRON_ORE (an unrelated control good), so a test can prove the construction-conditional
// blocklist drops exactly the named good while active and leaves the control good untouched.
func constructionBlocklistFixture() *tourFixture {
	wp := "X1-BL1-A"
	return &tourFixture{
		cargo: map[string]int{}, location: wp, cargoCap: 40,
		markets: map[string][]string{"X1-BL1": {wp}},
		ask: map[string]map[string]int{wp: {
			"ADVANCED_CIRCUITRY": 5000, "IRON_ORE": 120,
		}},
		bid: map[string]map[string]int{wp: {
			"ADVANCED_CIRCUITRY": 4500, "IRON_ORE": 50,
		}},
		tv: map[string]map[string]int{wp: {
			"ADVANCED_CIRCUITRY": 20, "IRON_ORE": 20,
		}},
	}
}

// TestTourConstructionCargoBlocklist_BlocksWhileConstructionActive proves the core requirement:
// with the construction-conditional list armed AND the sensor reporting construction live, the
// named good is dropped from the solver snapshot exactly like the unconditional blocklist,
// while an unrelated good survives.
func TestTourConstructionCargoBlocklist_BlocksWhileConstructionActive(t *testing.T) {
	fx := constructionBlocklistFixture()
	fake := &tourFakeRoutingClient{plans: []*routing.TourPlan{{Feasible: true}}}
	h := newTourHandler(t, fx, fake, nil)
	h.SetConstructionCargoBlocklist([]string{"ADVANCED_CIRCUITRY"})
	h.SetCapitalWorkSensor(&tourFakeCapitalWorkSensor{constructionWork: true})

	goods := snapshotGoods(planOnce(t, h, fake))

	if goods["ADVANCED_CIRCUITRY"] {
		t.Errorf("ADVANCED_CIRCUITRY must be blocked while construction is active: %v", goods)
	}
	if !goods["IRON_ORE"] {
		t.Errorf("an unrelated good must be unaffected: %v", goods)
	}
}

// TestTourConstructionCargoBlocklist_TradeableWhenConstructionIdle proves the good returns to
// the full universe the moment ConstructionHasWork reports false (gate finished, or its
// material bill is filled) — the entire point of making this list CONDITIONAL rather than a
// second permanent blocklist.
func TestTourConstructionCargoBlocklist_TradeableWhenConstructionIdle(t *testing.T) {
	fx := constructionBlocklistFixture()
	fake := &tourFakeRoutingClient{plans: []*routing.TourPlan{{Feasible: true}}}
	h := newTourHandler(t, fx, fake, nil)
	h.SetConstructionCargoBlocklist([]string{"ADVANCED_CIRCUITRY"})
	h.SetCapitalWorkSensor(&tourFakeCapitalWorkSensor{constructionWork: false})

	goods := snapshotGoods(planOnce(t, h, fake))

	if !goods["ADVANCED_CIRCUITRY"] {
		t.Errorf("ADVANCED_CIRCUITRY must be tradeable once construction is idle: %v", goods)
	}
}

// TestTourConstructionCargoBlocklist_UnsetIsByteIdentical proves the feature is opt-in even
// when a capital-work sensor happens to be wired for the (unrelated) capital-budget purpose:
// with NO SetConstructionCargoBlocklist call, the good rides into the snapshot regardless of
// what the sensor reports.
func TestTourConstructionCargoBlocklist_UnsetIsByteIdentical(t *testing.T) {
	fx := constructionBlocklistFixture()
	fake := &tourFakeRoutingClient{plans: []*routing.TourPlan{{Feasible: true}}}
	h := newTourHandler(t, fx, fake, nil)
	h.SetCapitalWorkSensor(&tourFakeCapitalWorkSensor{constructionWork: true}) // wired for capital budgeting, NOT the blocklist

	goods := snapshotGoods(planOnce(t, h, fake))

	if !goods["ADVANCED_CIRCUITRY"] {
		t.Errorf("an unset construction blocklist must never additionally block a good: %v", goods)
	}
}

// TestEffectiveCargoBlocklist unit-tests the fail-direction contract directly, matching the
// convention documented in run_tour_coordinator_budget.go's tradeCapitalBudget — the only OTHER
// consumer of h.workSensor.ConstructionHasWork on this handler: constructionHasWork defaults
// true and is overwritten only by a clean, error-free read, so a nil sensor or a sensor error
// fails the SAME direction tradeCapitalBudget does (toward "construction is live") — here,
// toward standing the tour down from the good rather than risk re-igniting the exact
// self-inflicted price-impact spiral this bead exists to prevent. Only a clean, explicit
// "false" read restores the full, unconditional cargoBlocklist.
func TestEffectiveCargoBlocklist(t *testing.T) {
	cases := []struct {
		name             string
		cargoBlocklist   []string
		constructionList []string
		sensor           *tourFakeCapitalWorkSensor // nil = SetCapitalWorkSensor never called
		wantBlocked      []string
		wantAllowed      []string
		wantConsulted    bool
	}{
		{
			name:             "no construction list configured — sensor never consulted",
			cargoBlocklist:   []string{"FUEL"},
			constructionList: nil,
			sensor:           &tourFakeCapitalWorkSensor{constructionWork: true},
			wantBlocked:      []string{"FUEL"},
			wantAllowed:      []string{"ADVANCED_CIRCUITRY"},
			wantConsulted:    false,
		},
		{
			name:             "construction list configured, sensor reports true — union applies",
			cargoBlocklist:   []string{"FUEL"},
			constructionList: []string{"ADVANCED_CIRCUITRY"},
			sensor:           &tourFakeCapitalWorkSensor{constructionWork: true},
			wantBlocked:      []string{"FUEL", "ADVANCED_CIRCUITRY"},
			wantConsulted:    true,
		},
		{
			name:             "construction list configured, sensor reports false — static list only",
			cargoBlocklist:   []string{"FUEL"},
			constructionList: []string{"ADVANCED_CIRCUITRY"},
			sensor:           &tourFakeCapitalWorkSensor{constructionWork: false},
			wantBlocked:      []string{"FUEL"},
			wantAllowed:      []string{"ADVANCED_CIRCUITRY"},
			wantConsulted:    true,
		},
		{
			name:             "construction list configured, sensor errors — fails toward blocking (matches tradeCapitalBudget)",
			cargoBlocklist:   nil,
			constructionList: []string{"ADVANCED_CIRCUITRY"},
			sensor:           &tourFakeCapitalWorkSensor{err: errors.New("container registry unreadable")},
			wantBlocked:      []string{"ADVANCED_CIRCUITRY"},
			wantConsulted:    true,
		},
		{
			name:             "construction list configured, sensor never wired — fails toward blocking (matches tradeCapitalBudget)",
			cargoBlocklist:   nil,
			constructionList: []string{"ADVANCED_CIRCUITRY"},
			sensor:           nil,
			wantBlocked:      []string{"ADVANCED_CIRCUITRY"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &RunTourCoordinatorHandler{}
			h.SetCargoBlocklist(tc.cargoBlocklist)
			h.SetConstructionCargoBlocklist(tc.constructionList)
			if tc.sensor != nil {
				h.SetCapitalWorkSensor(tc.sensor)
			}

			got := h.effectiveCargoBlocklist(context.Background(), 1)

			for _, want := range tc.wantBlocked {
				if !got[want] {
					t.Errorf("expected %q blocked, got %v", want, got)
				}
			}
			for _, want := range tc.wantAllowed {
				if got[want] {
					t.Errorf("expected %q allowed, got %v", want, got)
				}
			}
			if tc.sensor != nil && (tc.sensor.calls > 0) != tc.wantConsulted {
				t.Errorf("sensor consulted = %v, want %v", tc.sensor.calls > 0, tc.wantConsulted)
			}
		})
	}
}
