package commands

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// These tests cover the sp-gm7r P0 fix: the cold-start GATE death spiral where bootstrap advanced
// COLDSTART→GATE PREMATURELY. gateFunded entered GATE on a lightly-scaled contract op (as little as 2
// haulers), actGate started a construction pipeline, and derivePhase's ConstructionStarted sticky latch
// then held GATE forever — even though the op was never genuinely scaled — so the op cannibalized its
// contract haulers into construction and death-spiralled.
//
// THE FIX (two coupled parts, UNCONDITIONALLY ON — the flag is gone):
//   A. The former gate_surplus_hardening hardening is now the SOLE path (no flag, no default-off).
//   B. The static hauler floor is replaced by a DYNAMIC bar: GATE entry requires the FULL contract
//      fleet (delivery Haulers + depot warehouse/stocker hulls) to have reached the auto-scaler's
//      live achievable target (ContractScalerTarget), AND a treasury surplus.
//
// The target is a HARD bar: an op that cannot reach the target stays in cold start by design. It is
// fail-closed — a 0/unread target NEVER gates. Tested at the STATE-MACHINE level (derivePhase /
// planGateWorkers / reconcileOnce), because this bug hid precisely because each single-guard check
// looked "correct" in isolation.

// provisionedScanning stamps the "scanning workstream done" fields (probes at target & scouting,
// fully covered) onto an entry-gate observation, so a blocked GATE falls through to COLDSTART
// — isolating the GATE-entry decision as the only variable.
func provisionedScanning(obs Observation) Observation {
	obs.ProbeCount = 3
	obs.ProbesScouting = 3
	obs.MarketsTotal = 10
	obs.MarketsCovered = 10
	return obs
}

// --- Part B (keystone): GATE entry demands the FULL fleet reach the scaler target (+ a treasury surplus) ---

// Each case is genuinely UNDER the scaler target or missing the surplus on exactly one axis, so GATE must
// NOT be entered — the op stays in cold start. The OLD static gate would have entered GATE on the first case
// (2 haulers clears the old 2-hauler floor); the dynamic target blocks it.
func TestBootstrap_Gm7r_GateEntry_BelowScalerTarget_StaysColdStart(t *testing.T) {
	surplus := common.ImmutableReserveFloor + gateSurplusFloor // treasury with exactly the required surplus

	cases := []struct {
		name string
		obs  Observation
	}{
		{
			// The very shape the spiral latched on: a 2-hull op earning well with a fat treasury, but far
			// below the scaler's 10-hull target. A 2-hull op is not a scaled fleet.
			name: "full_fleet_far_below_target",
			obs:  Observation{Haulers: twoHaulers(), ContractScalerTarget: 10, IncomePerHour: 60000, Treasury: 2_000_000},
		},
		{
			// One hull short of the target — still not the fleet the scaler is driving toward.
			name: "full_fleet_one_short_of_target",
			obs:  Observation{Haulers: nHaulers(9), ContractScalerTarget: 10, IncomePerHour: 60000, Treasury: 2_000_000},
		},
		{
			// Full fleet AT target and earning well, but NO treasury surplus (150k − 50k = 100k < 500k) —
			// gating here would race the gate build on a war chest that cannot pay the material bill.
			name: "at_target_but_no_surplus",
			obs:  Observation{Haulers: nHaulers(10), ContractScalerTarget: 10, IncomePerHour: 60000, Treasury: 150_000},
		},
		{
			// Surplus exactly one credit under the floor — fail-closed direction (surplus must be ≥ floor).
			name: "surplus_one_credit_under_floor",
			obs:  Observation{Haulers: nHaulers(10), ContractScalerTarget: 10, IncomePerHour: 60000, Treasury: surplus - 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if p := derivePhase(provisionedScanning(tc.obs)); p == PhaseGate {
				t.Fatalf("%s: an op that has not reached the scaler target (or lacks the surplus) must NOT enter GATE, got %s", tc.name, p)
			}
		})
	}
}

// A genuinely SCALED and FUNDED op enters GATE: the full contract fleet has reached the scaler target AND
// the treasury holds a surplus ≥ the floor. The surplus check is boundary-exact:
// surplus = treasury − ImmutableReserveFloor(50k), so treasury 550k ⇒ surplus 500k == floor ⇒ gates;
// one credit under ⇒ does not. This proves the bar admits the legitimate entry, not merely blocks everything.
func TestBootstrap_Gm7r_GateEntry_AtOrAboveScalerTarget_EntersGate(t *testing.T) {

	// Full fleet == target, treasury 600k ⇒ surplus 550k ≥ floor → GATE.
	scaledFunded := Observation{Haulers: nHaulers(10), ContractScalerTarget: 10, Treasury: 600_000}
	if p := derivePhase(scaledFunded); p != PhaseGate {
		t.Fatalf("scaled (full fleet at target) + 550k surplus → GATE, got %s", p)
	}

	// Full fleet ABOVE target still gates (the bar is a floor, not an equality).
	above := Observation{Haulers: nHaulers(12), ContractScalerTarget: 10, Treasury: 600_000}
	if p := derivePhase(above); p != PhaseGate {
		t.Fatalf("full fleet above target must still gate, got %s", p)
	}

	// Boundary: surplus exactly at the floor gates (≥), one credit under does not (fail-closed direction).
	atFloor := Observation{Haulers: nHaulers(10), ContractScalerTarget: 10, Treasury: common.ImmutableReserveFloor + gateSurplusFloor}
	if p := derivePhase(atFloor); p != PhaseGate {
		t.Fatalf("surplus exactly at the floor must gate (≥), got %s", p)
	}
	underFloor := atFloor
	underFloor.Treasury--
	if p := derivePhase(underFloor); p == PhaseGate {
		t.Fatalf("surplus one credit under the floor must NOT gate, got %s", p)
	}
}

// The DEPOT half of the contract fleet counts toward the bar: 6 delivery haulers ALONE fall short of a
// 10-hull target (would NOT gate), but 6 haulers + 4 depot (warehouse/stocker) hulls reach it and gate.
// This is the load-bearing behavior of the "FULL fleet" bar vs the old delivery-only floor.
func TestBootstrap_Gm7r_DepotHullsCountTowardTarget(t *testing.T) {
	funded := func(depot int) Observation {
		return Observation{
			Haulers:                nHaulers(6),
			ContractDepotHullCount: depot,
			ContractScalerTarget:   10,
			Treasury:               2_000_000,
		}
	}

	// 6 delivery haulers + 0 depot = 6 < 10 target → NOT gated.
	if p := derivePhase(provisionedScanning(funded(0))); p == PhaseGate {
		t.Fatalf("6 delivery haulers alone (< 10 target) must NOT gate, got %s", p)
	}
	// 6 delivery + 4 depot = 10 == target → gated. The depot hulls are what cross the bar.
	if p := derivePhase(funded(4)); p != PhaseGate {
		t.Fatalf("6 delivery + 4 depot hulls (= 10 target) must gate — the depot half counts, got %s", p)
	}
}

// FAIL-CLOSED: a 0 scaler target (no scaler running / unread) NEVER gates, even with a huge fleet, huge
// income, and a huge surplus — bootstrap must never enter GATE on an unknown target (it would gate blind).
func TestBootstrap_Gm7r_FailClosed_ZeroScalerTarget_NeverGates(t *testing.T) {
	huge := Observation{
		Haulers:                nHaulers(20),
		ContractDepotHullCount: 10,
		ContractScalerTarget:   0, // unknown target
		IncomePerHour:          500000,
		Treasury:               50_000_000,
	}
	if p := derivePhase(provisionedScanning(huge)); p == PhaseGate {
		t.Fatalf("a 0 scaler target must NEVER gate (fail-closed on an unknown target), got %s", p)
	}
}

// --- Part 2: GATE never cannibalizes the contract fleet (superseded by sp-cdxy2: keep the WHOLE fleet) ---
//
// The sp-gm7r cure held a gate_contract_floor (2) and repurposed only the surplus ABOVE it. sp-cdxy2
// SUPERSEDED that with the exclusive-fleet rule (sp-9le3x): GATE keeps the WHOLE contract delivery fleet and
// repurposes NONE of it (the floor knob is gone), so the cannibalization the spiral turned on cannot happen
// at all — the gate builds its own construction fleet by BUYING. That stronger invariant is pinned in
// run_bootstrap_gate_test.go: TestBootstrap_PlanGateWorkers_KeepsWholeContractFleet_ReleasesNone (the sizing)
// and TestBootstrap_Gate_NeverRepurposesContractFleet (through the driving port).

// --- Part 2b: the escape hatch is UNCONDITIONALLY ON — a legit fresh GATE entry is never re-derived ---

// A legitimately-funded FRESH GATE (full fleet at target + surplus, no pipeline yet) is NOT a stuck latch,
// so the escape hatch leaves it in GATE across many ticks. This proves reDeriveUnderScaledGate (now
// unconditional) only releases a STICKY, under-scaled latch — never a genuine entry.
func TestBootstrap_Gm7r_Escape_DoesNotReDeriveLegitFreshGate(t *testing.T) {
	funded := Observation{
		HomeSystem: "X1-HQ", ProbeCount: 3, ProbesScouting: 3, MarketsTotal: 10, MarketsCovered: 10,
		Haulers: nHaulers(10), ContractScalerTarget: 10, Treasury: 2_000_000,
		Readable: true,
	}
	obsvr := &fakeObserver{obs: funded}
	h := newEscapeHandler(obsvr)
	cmd := baseCmd()
	for tick := 1; tick <= gateReentryStreakThreshold+2; tick++ {
		res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), cmd)
		if err != nil {
			t.Fatalf("tick %d: %v", tick, err)
		}
		if res.Phase != PhaseGate {
			t.Fatalf("tick %d: a legit funded fresh GATE (not a stuck latch) must stay GATE, got %s", tick, res.Phase)
		}
	}
}

// --- Part 3: escape hatch — re-derive GATE→COLDSTART when it latched under-scaled, with hysteresis ---

// A sticky GATE that latched under-scaled (0 haulers) with ~no construction (0% <
// gateReentryConstructionPct 5%) re-derives COLDSTART so the op can re-scale — but ONLY after
// gateReentryStreakTicks (3) CONSECUTIVE such ticks (anti-thrash). Ticks 1-2 hold GATE (streak
// building); tick 3 flips to COLDSTART. Driven through reconcileOnce so the real per-container hysteresis
// state runs. Unconditional now (no flag).
func TestBootstrap_Gm7r_ReDerivesColdStart_WhenLatchedUnderScaled(t *testing.T) {
	stuck := Observation{
		ConstructionStarted: true, // sticky-GATE latch holds
		ConstructionPercent: 0,    // never really built — below the 5% escape ceiling
		Haulers:             nil,  // 0 < gate_min_haulers(2): under-scaled
		Treasury:            125_000,
		Readable:            true,
	}
	obsvr := &fakeObserver{obs: stuck}
	h := newEscapeHandler(obsvr)
	cmd := baseCmd()

	for tick := 1; tick < gateReentryStreakThreshold; tick++ {
		res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), cmd)
		if err != nil {
			t.Fatalf("tick %d: %v", tick, err)
		}
		if res.Phase != PhaseGate {
			t.Fatalf("tick %d: streak not yet full (< %d) must HOLD sticky GATE (anti-thrash), got %s", tick, gateReentryStreakThreshold, res.Phase)
		}
	}
	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), cmd)
	if err != nil {
		t.Fatalf("tick %d: %v", gateReentryStreakThreshold, err)
	}
	if res.Phase != PhaseColdStart {
		t.Fatalf("%d consecutive under-scaled low-progress ticks must re-derive COLDSTART (escape the latch), got %s", gateReentryStreakThreshold, res.Phase)
	}
}

// Anti-thrash: a single tick that BREAKS the condition (construction crosses the 5% ceiling — real
// materials are flowing) RESETS the streak, so the re-derive requires 3 fresh CONSECUTIVE ticks again.
// Were the streak cumulative, tick 4 would fire COLDSTART; because it is consecutive, ticks 1-5 all hold
// GATE and only the 3rd fresh consecutive stuck tick (tick 6) escapes.
func TestBootstrap_Gm7r_EscapeHysteresis_ResetsOnConditionBreak(t *testing.T) {
	stuck := Observation{ConstructionStarted: true, ConstructionPercent: 0, Haulers: nil, Treasury: 125_000, Readable: true}
	obsvr := &fakeObserver{obs: stuck}
	h := newEscapeHandler(obsvr)
	cmd := baseCmd()

	mustPhase := func(tick int, want Phase) {
		res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), cmd)
		if err != nil {
			t.Fatalf("tick %d: %v", tick, err)
		}
		if res.Phase != want {
			t.Fatalf("tick %d: want %s, got %s", tick, want, res.Phase)
		}
	}

	obsvr.obs.ConstructionPercent = 0
	mustPhase(1, PhaseGate) // streak 1
	mustPhase(2, PhaseGate) // streak 2
	obsvr.obs.ConstructionPercent = 10
	mustPhase(3, PhaseGate) // condition broken (progress ≥ 5%) → streak RESET, legit building GATE
	obsvr.obs.ConstructionPercent = 0
	mustPhase(4, PhaseGate)      // streak 1 again (NOT 3 — proves consecutive, not cumulative)
	mustPhase(5, PhaseGate)      // streak 2
	mustPhase(6, PhaseColdStart) // streak 3 → escape
}

// newEscapeHandler wires a coordinator with the minimal collaborators the escape-hatch / entry-gate
// reconcile path needs (refresher + observer; the GATE/contract action collaborators are nil-safe logged
// skips, which is all these phase-derivation tests exercise). No live-config reader is needed — the
// hardening + escape hatch are UNCONDITIONALLY ON (the flag is gone), so the documented defaults resolve
// from a nil snapshot.
func newEscapeHandler(obsvr *fakeObserver) *RunBootstrapCoordinatorHandler {
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(obsvr)
	return h
}

// gateReentryStreakThreshold mirrors the documented default so the escape tests read against the
// consecutive-tick count without hard-coding it.
const gateReentryStreakThreshold = gateReentryStreakTicks

// twoHaulers / nHaulers build symbol-tagged contract-hauler pools (H1..Hn) for the sizing + entry tests.
func twoHaulers() []HaulerSnapshot { return nHaulers(2) }

func nHaulers(n int) []HaulerSnapshot {
	hs := make([]HaulerSnapshot, 0, n)
	for i := 1; i <= n; i++ {
		hs = append(hs, HaulerSnapshot{Symbol: haulerSymbol(i)})
	}
	return hs
}

func haulerSymbol(i int) string {
	if i < 10 {
		return "H" + string(rune('0'+i))
	}
	return "H1" + string(rune('0'+i-10))
}
