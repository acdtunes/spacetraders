package commands

import (
	"math"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
)

// These tests cover the scaled GATE-entry gate, now driven by the sp-gm7r DYNAMIC bar (the full contract
// fleet must reach the auto-scaler's live target) rather than a static hauler floor. THE ORIGINAL ktio
// DEADLOCK: derivePhase entered GATE the instant instantaneous income cleared the 10000 income_bar —
// trivially crossed by ONE contract payout — so bootstrap drove GATE with ZERO haulers and latched sticky on
// ConstructionStarted, permanently. THE sp-gm7r FIX requires a genuinely SCALED AND FUNDED contract op to
// enter GATE: the FULL fleet (delivery + depot) ≥ ContractScalerTarget, a SUSTAINED (rolling-window mean, NOT
// instantaneous) $/hr ≥ gate_income_bar, AND a treasury surplus (coverage is NOT a gate — it is continuous
// background).

// scaledGateCfg resolves the coordinator config for the GATE-entry gate. gate_income_bar (50000) is the
// sustained-$/hr bar; gate_min_haulers (2) is now the escape-hatch starved-earner floor (GATE ENTRY uses the
// scaler target). Both carry their documented defaults.
func scaledGateCfg(t *testing.T) bootstrapRunConfig {
	t.Helper()
	cfg := resolveBootstrapConfig(baseCmd(), nil)
	if cfg.GateIncomeBar != defaultGateIncomeBar || cfg.GateMinHaulers != defaultGateMinHaulers {
		t.Fatalf("scaled gate must carry the documented bars: income=%v haulers=%d", cfg.GateIncomeBar, cfg.GateMinHaulers)
	}
	return cfg
}

// gateSurplusTreasury is a treasury that clears the GATE-entry surplus floor (≥ 550k ⇒ surplus ≥ 500k).
const gateSurplusTreasury = 600_000

// --- GATE requires the full fleet at the scaler target AND a sustained $/hr AND a surplus (coverage is NOT a gate) ---

// All three conditions met (full fleet ≥ target, a sustained $/hr ≥ gate_income_bar, a treasury surplus) →
// GATE: a genuinely scaled, funded contract op is the legitimate entry.
func TestBootstrap_ScaledGate_FullFleetAtTargetAndFunded_EntersGate(t *testing.T) {
	cfg := scaledGateCfg(t)
	obs := Observation{
		Haulers:              []HaulerSnapshot{{Symbol: "H1"}, {Symbol: "H2"}}, // 2 delivery
		ContractScalerTarget: 2,                                               // full fleet (2) == target
		IncomePerHour:        60000,                                           // (as substituted: the sustained mean) ≥ 50000
		Treasury:             gateSurplusTreasury,                             // surplus ≥ floor
	}
	if p := derivePhase(obs, cfg); p != PhaseGate {
		t.Fatalf("full fleet at target + sustained income + surplus → GATE, got %s", p)
	}
}

// THE ktio case under the dynamic bar: income spiked far over the bar but with ZERO haulers (a frigate-only
// contract payout, not a scaled op). Must NOT enter GATE — the full fleet (0) is below the scaler target,
// regardless of how high income spikes.
func TestBootstrap_ScaledGate_SpikeWithZeroHaulers_DoesNotGate(t *testing.T) {
	cfg := scaledGateCfg(t)
	obs := Observation{Haulers: nil, ContractScalerTarget: 2, IncomePerHour: 300000, Treasury: 2_000_000}
	if p := derivePhase(obs, cfg); p == PhaseGate {
		t.Fatalf("a 0-hauler income spike must NOT enter GATE (full fleet below target — the ktio deadlock), got %s", p)
	}
}

// Full fleet at target and a surplus, but the sustained $/hr is under gate_income_bar → not yet funded, must
// NOT enter GATE.
func TestBootstrap_ScaledGate_SustainedBelowBar_DoesNotGate(t *testing.T) {
	cfg := scaledGateCfg(t)
	obs := Observation{
		Haulers:              []HaulerSnapshot{{Symbol: "H1"}, {Symbol: "H2"}},
		ContractScalerTarget: 2,
		IncomePerHour:        40000, // under the 50000 bar
		Treasury:             gateSurplusTreasury,
	}
	if p := derivePhase(obs, cfg); p == PhaseGate {
		t.Fatalf("full fleet at target but sustained income under the bar must NOT enter GATE, got %s", p)
	}
}

// Coverage is NOT a GATE-entry condition: a scaled, funded op enters GATE even while the home system is barely
// scanned (30% coverage). Scan-completeness runs as continuous background (the freshness sizer), never a phase
// gate.
func TestBootstrap_ScaledGate_LowCoverage_StillEntersGate(t *testing.T) {
	cfg := scaledGateCfg(t)
	obs := Observation{
		MarketsTotal: 10, MarketsCovered: 3, // 30% coverage
		Haulers:              []HaulerSnapshot{{Symbol: "H1"}, {Symbol: "H2"}},
		ContractScalerTarget: 2,
		IncomePerHour:        60000,
		Treasury:             gateSurplusTreasury,
	}
	if p := derivePhase(obs, cfg); p != PhaseGate {
		t.Fatalf("a scaled, funded op must enter GATE regardless of coverage (30%%), got %s", p)
	}
}

// Sticky-latch safety: ConstructionStarted still forces GATE (a legitimately-started pipeline resumes on
// restart), and — because construction can only START after a legit scaled+funded entry — this latch can never
// be tripped by a spurious spike. Pins that the sticky branch is unchanged (checked before the entry gate).
func TestBootstrap_ScaledGate_ConstructionStartedStaysGateEvenUnfunded(t *testing.T) {
	cfg := scaledGateCfg(t)
	// Repurposed haulers have pulled the op back under every entry condition, but a pipeline exists.
	obs := Observation{MarketsTotal: 10, MarketsCovered: 10, Haulers: nil, IncomePerHour: -55000, ConstructionStarted: true}
	if p := derivePhase(obs, cfg); p != PhaseGate {
		t.Fatalf("a started pipeline must stay sticky-GATE regardless of income/haulers, got %s", p)
	}
}

// --- CURATIVE re-derive: the deploy's daemon RESTART cures a LIVE stuck-GATE latch ---

// The gate gates GATE ENTRY, not EXIT — a running container stuck in GATE stays there. The CURE is the
// deploy's fresh container BUILD (restart), which re-derives phase from live signals (no persisted phase
// cursor). This pins that re-derive against a stuck-GATE live state: construction 0% (never started — blocked
// on no_purchaser), ZERO haulers, income ≈68000/hr, coverage ≈0.89, probes at target & scouting. It must
// re-derive INCOME — NOT GATE (the pre-fix wedge), NOT DATA (coverage is no longer a phase gate). GATES THE
// DEPLOY: if the restart did not cure the latch here, the live P1 would stay wedged.
func TestBootstrap_ScaledGate_DefaultOnRestart_ReDerivesIncomeFromStuckGateLiveState(t *testing.T) {
	// The live stuck-GATE observation, modeled faithfully. ConstructionStarted=false is load-bearing: the
	// pipeline never actually started, so NO sticky latch fights the re-derive. The scanning workstream is
	// mature (probes at target, all scouting; 89% coverage).
	stuck := Observation{
		ConstructionStarted:  false, // construction 0% — never started, so the sticky-GATE latch does not hold
		ConstructionComplete: false,
		Haulers:              nil,   // ZERO haulers — the op never scaled past the sole frigate
		ContractScalerTarget: 10,    // the scaler's live target; the full fleet (0) is far below it
		IncomePerHour:        68000, // ≈ live realized $/hr; clears gate_income_bar(50000) on its own
		ProbeCount:           3,     // == defaultProbeTarget: scanning workstream done
		ProbesScouting:       3,     // all scouting
		MarketsTotal:         9,
		MarketsCovered:       8, // coverage ≈ 0.89 — must NOT route to DATA (coverage is not a phase gate)
	}

	// gateFunded is FALSE — the full fleet (0) is below the scaler target (10) even though 68000 ≥
	// gate_income_bar 50000 — and ConstructionStarted is false (no sticky latch), so the arc falls through to
	// INCOME (probes at target & scouting). The cure the restart delivers.
	if p := derivePhase(stuck, resolveBootstrapConfig(baseCmd(), nil)); p != PhaseIncome {
		t.Fatalf("restart must re-derive INCOME from the stuck-GATE live state (cure the latch), got %s", p)
	}
}

// --- the sustained-income smoother: a spike is diluted; only a full window of sustained income clears ---

// A not-yet-full window can never clear a bar: sustained() returns −inf until it holds gateIncomeWindowTicks
// samples, so a spike on short history (the first ticks after arming, or after a restart drops the window)
// can never enter GATE.
func TestIncomeWindow_NotFull_NeverClearsAnyBar(t *testing.T) {
	w := &incomeWindow{}
	for i := 0; i < gateIncomeWindowTicks-1; i++ {
		if got := w.sustained(1_000_000); !math.IsInf(got, -1) {
			t.Fatalf("a not-yet-full window must return -inf (never clears a bar); sample %d got %v", i, got)
		}
	}
}

// Mirrors the ktio trace exactly: four net-negative spend ticks then one big contract payout. The window
// mean stays well under gate_income_bar, so the sustained metric never clears the bar off a lone spike.
func TestIncomeWindow_SpikeAmongSpendTicks_MeanStaysUnderBar(t *testing.T) {
	w := &incomeWindow{}
	var last float64
	for _, v := range []float64{-55000, -55000, -55000, -55000, 105000} {
		last = w.sustained(v)
	}
	if math.IsInf(last, -1) {
		t.Fatalf("a full window must return a real mean, got -inf")
	}
	if last >= defaultGateIncomeBar { // mean = (-55000*4 + 105000)/5 = -23000
		t.Fatalf("a lone payout among spend ticks must not clear the bar: mean=%v bar=%v", last, defaultGateIncomeBar)
	}
}

// A full window of sustained earning clears the bar — the legitimate signal the gate is built to admit.
func TestIncomeWindow_SustainedHigh_ClearsBarOnceFull(t *testing.T) {
	w := &incomeWindow{}
	var last float64
	for i := 0; i < gateIncomeWindowTicks; i++ {
		last = w.sustained(60000)
	}
	if last < defaultGateIncomeBar {
		t.Fatalf("a full window of sustained 60000 must clear the 50000 bar, got mean=%v", last)
	}
}

// --- acceptance (multi-tick through reconcileOnce): the smoother is WIRED into the phase derivation —
// a lone income spike never enters GATE, but a SUSTAINED $/hr does once the window fills. ---

func TestBootstrap_ScaledGate_SpikeStaysIncome_SustainedEntersGate(t *testing.T) {
	// A scaled, funded op: full fleet at target (2), a fat treasury (surplus over the floor) — so ONLY the
	// sustained-income condition is in question here.
	base := Observation{
		HomeSystem: "X1-HQ", ProbeCount: 3, ProbesScouting: 3, HasIdlePurchaser: true,
		MarketsTotal: 10, MarketsCovered: 10,
		Haulers:              []HaulerSnapshot{{Symbol: "H1"}, {Symbol: "H2"}},
		ContractScalerTarget: 2,
		Treasury:             1_000_000, Readable: true,
	}
	obsvr := &fakeObserver{obs: base}
	live := &fakeLiveConfig{snap: liveconfig.Snapshot{}}
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(obsvr)
	h.SetProbeAcquirer(&fakeAcquirer{price: 40000, yard: "Y", readable: true})
	h.SetScoutPostDeclarer(&fakeDeclarer{})
	h.SetLiveConfigReader(live)
	cmd := baseCmd()

	// A lone spike among net-negative spend ticks: the window mean never clears the bar, so the arc stays
	// in INCOME (never GATE) — exactly the ktio scenario, now contained.
	for i, income := range []float64{-55000, -55000, -55000, -55000, 105000, -55000} {
		obsvr.obs.IncomePerHour = income
		res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), cmd)
		if err != nil {
			t.Fatalf("spike tick %d: %v", i, err)
		}
		if res.Phase == PhaseGate {
			t.Fatalf("a lone income spike must never enter GATE (tick %d income=%v phase=%s)", i, income, res.Phase)
		}
	}

	// Now income is SUSTAINED above the bar. Once the rolling window fills with sustained readings the arc
	// enters GATE — the legitimate scaled-op entry.
	gateEntered := false
	for i := 0; i < gateIncomeWindowTicks+2; i++ {
		obsvr.obs.IncomePerHour = 60000
		res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), cmd)
		if err != nil {
			t.Fatalf("sustained tick %d: %v", i, err)
		}
		if res.Phase == PhaseGate {
			gateEntered = true
			break
		}
	}
	if !gateEntered {
		t.Fatalf("sustained $/hr over the bar (a scaled, funded op at target) must enter GATE once the rolling window fills")
	}
}
