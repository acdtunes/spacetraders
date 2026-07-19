package commands

import (
	"math"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
)

// These tests cover the sp-fp3y scaled-GATE-entry gate (ktio-C). THE ktio DEADLOCK: derivePhase entered
// GATE the instant instantaneous income cleared the 10000 income_bar — trivially crossed by ONE contract
// payout (staging: rolling income −55k→+105k in 53s) — so bootstrap drove GATE with ZERO haulers and
// latched sticky on ConstructionStarted, permanently. THE FIX (armed via scaled_gate_entry, DEFAULT OFF)
// requires a genuinely SCALED contract op to enter GATE: coverage ≥ coverage_bar AND haulers ≥
// gate_min_haulers AND a SUSTAINED (rolling-window mean, NOT instantaneous) $/hr ≥ gate_income_bar. It
// ARMS TOGETHER WITH ktio-B (autosizer-early): armed alone the op never scales haulers and the arc would
// wedge in INCOME, which is why it ships OFF (this suite proves both the OFF byte-identical path and the
// armed behavior).

// armedCfg resolves the coordinator config with scaled_gate_entry armed via the sp-r6yq live seam, so the
// gate's two calibration knobs (gate_income_bar 50000, gate_min_haulers 2) carry their documented defaults.
func armedCfg(t *testing.T) bootstrapRunConfig {
	t.Helper()
	cfg := resolveBootstrapConfig(baseCmd(), liveconfig.Snapshot{"scaled_gate_entry": 1})
	if !cfg.ScaledGateEntry {
		t.Fatalf("scaled_gate_entry=1 must arm the gate")
	}
	if cfg.GateIncomeBar != defaultGateIncomeBar || cfg.GateMinHaulers != defaultGateMinHaulers {
		t.Fatalf("armed gate must carry the documented bars: income=%v haulers=%d", cfg.GateIncomeBar, cfg.GateMinHaulers)
	}
	return cfg
}

// --- byte-identical: flag OFF is exactly today's instantaneous income_bar trigger ---

// The default-off guarantee at the derivePhase seam: with scaled_gate_entry unset, the exact ktio spike
// (one payout over income_bar, ZERO haulers, 30% coverage) STILL derives GATE — byte-identical to today.
// This is the flag-off half of the ktio-C contract (the fix must not change anything until armed).
func TestBootstrap_ScaledGate_FlagOff_ByteIdentical_InstantSpikeStillGates(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd(), nil)
	if cfg.ScaledGateEntry {
		t.Fatalf("scaled_gate_entry must default OFF")
	}
	obs := Observation{MarketsTotal: 10, MarketsCovered: 3, IncomePerHour: 12000} // over income_bar 10000, 0 haulers
	if p := derivePhase(obs, cfg); p != PhaseGate {
		t.Fatalf("flag OFF must be byte-identical: instantaneous income over income_bar → GATE, got %s", p)
	}
}

// --- armed: GATE requires coverage AND haulers AND a sustained $/hr (all three, together) ---

// All three conditions met (coverage ≥ bar, haulers ≥ floor, a sustained $/hr ≥ gate_income_bar) → GATE:
// a genuinely scaled, funded contract op is the legitimate entry.
func TestBootstrap_ScaledGate_Armed_AllThreeConditions_EntersGate(t *testing.T) {
	cfg := armedCfg(t)
	obs := Observation{
		MarketsTotal: 10, MarketsCovered: 10, // coverage 100% ≥ 0.90 bar
		Haulers:       []HaulerSnapshot{{Symbol: "H1"}, {Symbol: "H2"}}, // 2 ≥ floor
		IncomePerHour: 60000,                                            // (as substituted: the sustained mean) ≥ 50000
	}
	if p := derivePhase(obs, cfg); p != PhaseGate {
		t.Fatalf("armed: coverage+haulers+sustained income all met → GATE, got %s", p)
	}
}

// THE ktio case under the armed gate: income spiked far over the bar but with ZERO haulers (a frigate-only
// contract payout, not a scaled op). Coverage is met. Must NOT enter GATE — the hauler floor is the primary
// defense against the 0-hauler deadlock, regardless of how high income spikes.
func TestBootstrap_ScaledGate_Armed_SpikeWithZeroHaulers_DoesNotGate(t *testing.T) {
	cfg := armedCfg(t)
	obs := Observation{MarketsTotal: 10, MarketsCovered: 10, Haulers: nil, IncomePerHour: 300000}
	if p := derivePhase(obs, cfg); p == PhaseGate {
		t.Fatalf("armed: a 0-hauler income spike must NOT enter GATE (unscaled op — the ktio deadlock), got %s", p)
	}
}

// Coverage + haulers met, but the sustained $/hr is under gate_income_bar → not yet funded, hold in INCOME.
func TestBootstrap_ScaledGate_Armed_SustainedBelowBar_HoldsIncome(t *testing.T) {
	cfg := armedCfg(t)
	obs := Observation{MarketsTotal: 10, MarketsCovered: 10, Haulers: []HaulerSnapshot{{Symbol: "H1"}, {Symbol: "H2"}}, IncomePerHour: 40000}
	if p := derivePhase(obs, cfg); p != PhaseIncome {
		t.Fatalf("armed: coverage+haulers met but sustained income under the bar → INCOME (not GATE), got %s", p)
	}
}

// Haulers + sustained income met, but coverage under the bar (still scanning) → not a real op yet, stay DATA.
func TestBootstrap_ScaledGate_Armed_CoverageUnderBar_DoesNotGate(t *testing.T) {
	cfg := armedCfg(t)
	obs := Observation{MarketsTotal: 10, MarketsCovered: 3, Haulers: []HaulerSnapshot{{Symbol: "H1"}, {Symbol: "H2"}}, IncomePerHour: 60000}
	if p := derivePhase(obs, cfg); p == PhaseGate {
		t.Fatalf("armed: coverage under the bar must NOT gate (op not yet real), got %s", p)
	}
}

// Sticky-latch safety: ConstructionStarted still forces GATE (a legitimately-started pipeline resumes on
// restart), and — because construction can only START after a legit armed entry — this latch can never be
// tripped by a spurious spike. Pins that the sticky branch is unchanged (checked before the entry gate).
func TestBootstrap_ScaledGate_Armed_ConstructionStartedStaysGateEvenUnfunded(t *testing.T) {
	cfg := armedCfg(t)
	// Repurposed haulers have pulled the op back under every entry condition, but a pipeline exists.
	obs := Observation{MarketsTotal: 10, MarketsCovered: 10, Haulers: nil, IncomePerHour: -55000, ConstructionStarted: true}
	if p := derivePhase(obs, cfg); p != PhaseGate {
		t.Fatalf("a started pipeline must stay sticky-GATE regardless of income/haulers, got %s", p)
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

// --- acceptance (armed, multi-tick through reconcileOnce): the smoother is WIRED into the phase
// derivation — a lone income spike never enters GATE, but a SUSTAINED $/hr does once the window fills. ---

func TestBootstrap_ScaledGate_Armed_SpikeStaysIncome_SustainedEntersGate(t *testing.T) {
	// A scaled op: coverage 100%, 2 haulers — so ONLY the sustained-income condition is in question here.
	base := Observation{
		HomeSystem: "X1-HQ", ProbeCount: 3, ProbesScouting: 3, HasIdlePurchaser: true,
		MarketsTotal: 10, MarketsCovered: 10,
		Haulers:  []HaulerSnapshot{{Symbol: "H1"}, {Symbol: "H2"}},
		Treasury: 1_000_000, Readable: true,
	}
	obsvr := &fakeObserver{obs: base}
	armed := &fakeLiveConfig{snap: liveconfig.Snapshot{"scaled_gate_entry": 1}}
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(obsvr)
	h.SetProbeAcquirer(&fakeAcquirer{price: 40000, yard: "Y", readable: true})
	h.SetScoutAssigner(&fakeScouter{})
	h.SetLiveConfigReader(armed)
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
		t.Fatalf("sustained $/hr over the bar (a scaled op) must enter GATE once the rolling window fills")
	}
}

// Byte-identical at the reconcileOnce seam: flag OFF, the ktio spike (one tick over income_bar, 0 haulers)
// enters GATE on the very first tick exactly as today — the smoother is never consulted.
func TestBootstrap_ScaledGate_FlagOff_ReconcileGatesOnInstantSpike(t *testing.T) {
	obs := Observation{
		HomeSystem: "X1-HQ", ProbeCount: 3, ProbesScouting: 3, HasIdlePurchaser: true,
		MarketsTotal: 10, MarketsCovered: 3, // 30% coverage — under the bar
		Treasury: 1_000_000, IncomePerHour: 12000, Readable: true, // one payout over income_bar, 0 haulers
	}
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeObserver{obs: obs})
	h.SetProbeAcquirer(&fakeAcquirer{price: 40000, yard: "Y", readable: true})
	h.SetScoutAssigner(&fakeScouter{})
	h.SetLiveConfigReader(&fakeLiveConfig{snap: liveconfig.Snapshot{}}) // armed reader, nothing tuned → OFF

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if res.Phase != PhaseGate {
		t.Fatalf("flag OFF: an instantaneous spike over income_bar must GATE on the first tick (byte-identical), got %s", res.Phase)
	}
}
