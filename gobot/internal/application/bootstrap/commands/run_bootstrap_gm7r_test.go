package commands

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
)

// These tests cover the scaled-gate hardening (P0): the cold-start GATE death spiral where the
// bootstrap latched GATE on a 2-hauler PEAK-income blip with no war chest, then cannibalized the contract
// op below the capacity reconciler's depot-staging pool (2→1), drove FleetPerHullCrHr negative, and could
// never rebuy — stuck at 1 hauler, no depot, treasury crashed on a starved gate build. Every guard fired
// locally-correct; the SYSTEM self-defeated. The fix is at the phase-machine × fleet-allocation level and
// ships DEFAULT-OFF behind one flag (gate_surplus_hardening). Three coupled parts:
//   Part 1 (keystone) — gateFunded ALSO requires a RAISED hauler floor + a SUSTAINED $/hr + a treasury surplus.
//   Part 2 — planGateWorkers keeps ≥ gate_contract_floor haulers earning (repurposes only the surplus).
//   Part 3 — reDeriveUnderScaledGate releases a sticky GATE that latched under-scaled with ~no construction
//            back to INCOME, after an anti-thrash consecutive-tick hysteresis streak.
// Gate entry becomes STRICTER, never looser (RULINGS #4). Tested at the STATE-MACHINE level (not per-guard),
// because this bug hid precisely because each single-guard check looked "correct" in isolation.

// hardenedCfg resolves the coordinator config with scaled-gate hardening ARMED (gate_surplus_hardening=1).
// scaled_gate_entry is also on (its default-ON state), which is the realistic deployment. Asserts the flag
// and the documented calibration defaults so the behavior tests below read against known bars.
func hardenedCfg(t *testing.T) bootstrapRunConfig {
	t.Helper()
	cfg := resolveBootstrapConfig(baseCmd(), liveconfig.Snapshot{"gate_surplus_hardening": 1})
	if !cfg.GateSurplusHardening {
		t.Fatalf("gate_surplus_hardening=1 must arm hardening")
	}
	if cfg.GateHaulerFloor != defaultGateHaulerFloor || cfg.GateSurplusFloor != defaultGateSurplusFloor ||
		cfg.GateContractFloor != defaultGateContractFloor || cfg.GateReentryConstructionPct != defaultGateReentryConstructionPct ||
		cfg.GateReentryStreakTicks != defaultGateReentryStreakTicks {
		t.Fatalf("armed hardening must carry the documented calibration defaults, got %+v", cfg)
	}
	return cfg
}

// offCfg resolves the config with hardening OFF (nothing tuned) — today's behavior (scaled_gate_entry on,
// hardening off). The byte-identical baseline.
func offCfg(t *testing.T) bootstrapRunConfig {
	t.Helper()
	cfg := resolveBootstrapConfig(baseCmd(), nil)
	if cfg.GateSurplusHardening {
		t.Fatalf("hardening must be OFF by default (byte-identical baseline)")
	}
	return cfg
}

// --- Part 0: byte-identical when hardening is OFF (the safety net) ---

// With hardening OFF, derivePhase is exactly today's scaled-gate machine across DATA/INCOME/GATE/COMPLETE.
// The load-bearing discriminator vs the armed path: a 2-hauler op at 60k with a THIN treasury STILL enters
// GATE — because neither the raised hauler floor NOR the surplus floor is applied while the flag is off. If
// any hardening condition leaked into the off path, this 2-hauler/thin-treasury case would be blocked.
func TestBootstrap_Gm7r_ByteIdentical_WhenHardeningOff(t *testing.T) {
	cfg := offCfg(t)

	// COMPLETE: a built gate is terminal.
	if p := derivePhase(Observation{ConstructionComplete: true}, cfg); p != PhaseComplete {
		t.Fatalf("off: ConstructionComplete → COMPLETE, got %s", p)
	}
	// GATE sticky: a started pipeline stays GATE regardless of income/haulers.
	if p := derivePhase(Observation{ConstructionStarted: true}, cfg); p != PhaseGate {
		t.Fatalf("off: ConstructionStarted → sticky GATE, got %s", p)
	}
	// GATE entry (today's scaled gate): 2 haulers + sustained 60k enters GATE — EVEN with a thin treasury
	// (100k) and low coverage. This is the exact case hardening will BLOCK; off, it must still gate.
	thin := Observation{
		Haulers:       []HaulerSnapshot{{Symbol: "H1"}, {Symbol: "H2"}},
		IncomePerHour: 60000, Treasury: 100_000, MarketsTotal: 10, MarketsCovered: 3,
	}
	if p := derivePhase(thin, cfg); p != PhaseGate {
		t.Fatalf("off must be byte-identical: 2 haulers + 60k → GATE even on a thin treasury, got %s", p)
	}
	// INCOME: probes at target + scouting, not funded.
	if p := derivePhase(Observation{ProbeCount: 3, ProbesScouting: 3, IncomePerHour: 0}, cfg); p != PhaseIncome {
		t.Fatalf("off: provisioned + unfunded → INCOME, got %s", p)
	}
	// DATA: probes under target.
	if p := derivePhase(Observation{ProbeCount: 1, ProbesScouting: 1}, cfg); p != PhaseData {
		t.Fatalf("off: probes under target → DATA, got %s", p)
	}
}

// --- Part 1 (keystone): GATE entry demands a RAISED hauler floor + sustained $/hr + a treasury surplus ---

// Each armed sub-case is genuinely UNDER-SCALED or UNFUNDED on exactly one axis, so GATE must NOT be
// entered — the op stays INCOME (provisioned: probes at target & scouting). The plain scaled gate (today)
// would gate every one of these (2 haulers + 50k clears it); hardening blocks them.
func TestBootstrap_Gm7r_GateEntry_Blocked_WhenUnscaledOrNoSurplus(t *testing.T) {
	cfg := hardenedCfg(t)
	provisioned := Observation{ProbeCount: 3, ProbesScouting: 3, MarketsTotal: 10, MarketsCovered: 10}

	cases := []struct {
		name string
		obs  Observation
	}{
		{
			// 2 haulers is below the RAISED floor (4) though above the plain floor (2). A 2-hull op is not a
			// real fleet — the very shape the ktio/gm7r spiral latched on.
			name: "below_raised_hauler_floor",
			obs:  Observation{Haulers: twoHaulers(), IncomePerHour: 60000, Treasury: 2_000_000},
		},
		{
			// A real 4-hull fleet earning well, but the treasury has NO surplus (100k − 50k = 50k < 500k) —
			// gating here would race the gate build on a war chest that can't pay the material bill (the spiral).
			name: "no_treasury_surplus",
			obs:  Observation{Haulers: nHaulers(4), IncomePerHour: 60000, Treasury: 100_000},
		},
		{
			// A real 4-hull fleet with a fat treasury, but sustained $/hr under the bar (40k < 50k) — not yet
			// earning enough to fund a gate build; a warming op, not a funded one.
			name: "sustained_income_under_bar",
			obs:  Observation{Haulers: nHaulers(4), IncomePerHour: 40000, Treasury: 2_000_000},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs := tc.obs
			obs.ProbeCount = provisioned.ProbeCount
			obs.ProbesScouting = provisioned.ProbesScouting
			obs.MarketsTotal = provisioned.MarketsTotal
			obs.MarketsCovered = provisioned.MarketsCovered
			if p := derivePhase(obs, cfg); p == PhaseGate {
				t.Fatalf("armed hardening must NOT enter GATE for %s (unscaled/unfunded op), got %s", tc.name, p)
			}
		})
	}
}

// A genuinely SCALED and FUNDED op enters GATE: haulers ≥ the raised floor (4), a sustained $/hr ≥ the bar
// (60k ≥ 50k), AND a treasury surplus ≥ the floor. The surplus check is boundary-exact: surplus = treasury −
// ImmutableReserveFloor(50k), so treasury 550k ⇒ surplus 500k == floor ⇒ gates; 549_999 ⇒ 499_999 < floor ⇒
// does not. This proves hardening admits the legitimate entry and is not merely blocking everything.
func TestBootstrap_Gm7r_GateEntry_Allowed_WhenScaledAndFunded(t *testing.T) {
	cfg := hardenedCfg(t)
	scaledFunded := Observation{Haulers: nHaulers(4), IncomePerHour: 60000, Treasury: 600_000}
	if p := derivePhase(scaledFunded, cfg); p != PhaseGate {
		t.Fatalf("armed: scaled (4 haulers) + sustained 60k + 550k surplus → GATE, got %s", p)
	}

	// Boundary: surplus exactly at the floor gates (≥), one credit under does not (fail-closed direction).
	atFloor := Observation{Haulers: nHaulers(4), IncomePerHour: 60000, Treasury: common.ImmutableReserveFloor + defaultGateSurplusFloor}
	if p := derivePhase(atFloor, cfg); p != PhaseGate {
		t.Fatalf("armed: surplus exactly at the floor must gate (≥), got %s", p)
	}
	underFloor := atFloor
	underFloor.Treasury--
	if p := derivePhase(underFloor, cfg); p == PhaseGate {
		t.Fatalf("armed: surplus one credit under the floor must NOT gate, got %s", p)
	}
}

// --- Part 2: GATE must not cannibalize contract haulers below the scaled floor ---

// planGateWorkers keeps ≥ gate_contract_floor (2) haulers EARNING while hardening is armed and repurposes
// only the surplus above it; with the flag off it keeps only min_contract_earners (1). Same 4-hauler input,
// only the flag differs — so the released count changes from 3 (off) to 2 (on), and the kept-on-contract
// pool never drops below the depot-staging floor of 2. This is the cannibalization the spiral turned on.
func TestBootstrap_Gm7r_Gate_DoesNotCannibalizeContractsBelowFloor(t *testing.T) {
	four := Observation{Haulers: nHaulers(4)} // GateMaterialChains 0 → no buy noise; pure release accounting

	off := planGateWorkers(four, offCfg(t))
	if len(off.ReleaseShips) != 3 {
		t.Fatalf("off (min_contract_earners=1): 4 haulers → release 3, got %d (%v)", len(off.ReleaseShips), off.ReleaseShips)
	}

	on := planGateWorkers(four, hardenedCfg(t))
	if len(on.ReleaseShips) != 2 {
		t.Fatalf("armed (gate_contract_floor=2): 4 haulers → release only the 2-hauler surplus, got %d (%v)", len(on.ReleaseShips), on.ReleaseShips)
	}
	if kept := len(four.Haulers) - len(on.ReleaseShips); kept < defaultGateContractFloor {
		t.Fatalf("armed: contract pool kept earning (%d) must never drop below the depot-staging floor (%d)", kept, defaultGateContractFloor)
	}
	// The released hulls are exactly the SURPLUS above the floor (Haulers[floor:]), never the earners kept.
	if on.ReleaseShips[0] != "H3" || on.ReleaseShips[1] != "H4" {
		t.Fatalf("armed: only the surplus above the floor is released, got %v (want [H3 H4])", on.ReleaseShips)
	}

	// At the floor exactly (2 haulers), armed hardening repurposes NOTHING — the op is never cannibalized.
	atFloor := planGateWorkers(Observation{Haulers: nHaulers(2)}, hardenedCfg(t))
	if len(atFloor.ReleaseShips) != 0 {
		t.Fatalf("armed: a 2-hauler pool at the floor must release nothing, got %v", atFloor.ReleaseShips)
	}
}

// --- Part 3: escape hatch — re-derive GATE→INCOME when it latched under-scaled, with hysteresis ---

// A sticky GATE that latched under-scaled (0 haulers, income under the bar) with ~no construction (0% <
// gate_reentry_construction_pct 5%) re-derives INCOME so the op can re-scale — but ONLY after
// gate_reentry_streak_ticks (3) CONSECUTIVE such ticks (anti-thrash). Ticks 1-2 hold GATE (streak building);
// tick 3 flips to INCOME. Driven through reconcileOnce so the real per-container hysteresis state runs.
func TestBootstrap_Gm7r_ReDerivesIncome_WhenLatchedUnderScaled(t *testing.T) {
	stuck := Observation{
		ConstructionStarted: true, // sticky-GATE latch holds
		ConstructionPercent: 0,    // never really built — below the 5% escape ceiling
		Haulers:             nil,  // 0 < gate_min_haulers(2): under-scaled
		IncomePerHour:       -20000,
		Treasury:            125_000,
		Readable:            true,
	}
	obsvr := &fakeObserver{obs: stuck}
	h := newHardenedEscapeHandler(obsvr)
	cmd := baseCmd()

	for tick := 1; tick <= 2; tick++ {
		res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), cmd)
		if err != nil {
			t.Fatalf("tick %d: %v", tick, err)
		}
		if res.Phase != PhaseGate {
			t.Fatalf("tick %d: streak not yet full (< 3) must HOLD sticky GATE (anti-thrash), got %s", tick, res.Phase)
		}
	}
	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), cmd)
	if err != nil {
		t.Fatalf("tick 3: %v", err)
	}
	if res.Phase != PhaseIncome {
		t.Fatalf("tick 3: 3 consecutive under-scaled low-progress ticks must re-derive INCOME (escape the latch), got %s", res.Phase)
	}
}

// Anti-thrash: a single tick that BREAKS the condition (construction crosses the 5% ceiling — real materials
// are flowing) RESETS the streak, so the re-derive requires 3 fresh CONSECUTIVE ticks again. Were the streak
// cumulative, tick 4 below would fire INCOME; because it is consecutive, ticks 1-5 all hold GATE and only the
// 3rd fresh consecutive stuck tick (tick 6) escapes.
func TestBootstrap_Gm7r_EscapeHysteresis_ResetsOnConditionBreak(t *testing.T) {
	stuck := Observation{ConstructionStarted: true, ConstructionPercent: 0, Haulers: nil, IncomePerHour: -20000, Treasury: 125_000, Readable: true}
	obsvr := &fakeObserver{obs: stuck}
	h := newHardenedEscapeHandler(obsvr)
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
	mustPhase(4, PhaseGate)   // streak 1 again (NOT 3 — proves consecutive, not cumulative)
	mustPhase(5, PhaseGate)   // streak 2
	mustPhase(6, PhaseIncome) // streak 3 → escape
}

// Byte-identical at the reconcileOnce seam: with hardening OFF, the SAME stuck-GATE state stays sticky GATE
// forever — the escape hatch never fires (it is entirely behind the flag). Five ticks, all GATE.
func TestBootstrap_Gm7r_EscapeOff_StuckGateStaysGate_ByteIdentical(t *testing.T) {
	stuck := Observation{ConstructionStarted: true, ConstructionPercent: 0, Haulers: nil, IncomePerHour: -20000, Treasury: 125_000, Readable: true}
	obsvr := &fakeObserver{obs: stuck}
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(obsvr)
	// No live-config reader wired → hardening OFF (and scaled_gate_entry off too — pre-arm sticky path).
	cmd := baseCmd()

	for tick := 1; tick <= 5; tick++ {
		res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), cmd)
		if err != nil {
			t.Fatalf("tick %d: %v", tick, err)
		}
		if res.Phase != PhaseGate {
			t.Fatalf("hardening OFF: a sticky GATE must never escape (byte-identical), tick %d got %s", tick, res.Phase)
		}
	}
}

// newHardenedEscapeHandler wires a coordinator with hardening ARMED via the live-config reader and the
// minimal collaborators the escape-hatch reconcile path needs (refresher + observer; the GATE/INCOME action
// collaborators are nil-safe logged skips, which is all these phase-derivation tests exercise).
func newHardenedEscapeHandler(obsvr *fakeObserver) *RunBootstrapCoordinatorHandler {
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(obsvr)
	h.SetLiveConfigReader(&fakeLiveConfig{snap: liveconfig.Snapshot{"gate_surplus_hardening": 1}})
	return h
}

// twoHaulers / nHaulers build symbol-tagged contract-hauler pools (H1..Hn) for the sizing + entry tests.
func twoHaulers() []HaulerSnapshot { return nHaulers(2) }

func nHaulers(n int) []HaulerSnapshot {
	hs := make([]HaulerSnapshot, 0, n)
	for i := 1; i <= n; i++ {
		hs = append(hs, HaulerSnapshot{Symbol: haulerSymbol(i)})
	}
	return hs
}

func haulerSymbol(i int) string { return "H" + string(rune('0'+i)) }
