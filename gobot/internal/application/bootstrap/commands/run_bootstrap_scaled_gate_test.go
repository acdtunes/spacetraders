package commands

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractScalerCmd "github.com/andrescamacho/spacetraders-go/internal/application/contractscaler/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contractscaler"
)

// These tests cover the scaled GATE-entry gate, driven by the sp-gm7r DYNAMIC bar (the full contract fleet
// must reach the auto-scaler's live target) rather than a static hauler floor. THE ORIGINAL ktio DEADLOCK:
// derivePhase entered GATE the instant realized income cleared the 10000 income_bar — trivially crossed by
// ONE contract payout — so bootstrap drove GATE with ZERO haulers and latched sticky on ConstructionStarted,
// permanently. THE sp-gm7r FIX requires a genuinely SCALED AND FUNDED contract op to enter GATE: the FULL
// fleet (delivery + depot) ≥ ContractScalerTarget AND a treasury surplus. Neither coverage nor realized
// $/hr is a gate — both are continuous background readings.

// scaledGateCfg resolves the coordinator config for the GATE-entry gate. gate_min_haulers (2) is the
// escape-hatch starved-earner floor (GATE ENTRY uses the scaler target), carrying its documented default.
func scaledGateCfg(t *testing.T) bootstrapRunConfig {
	t.Helper()
	cfg := resolveBootstrapConfig(baseCmd(), nil)
	if cfg.GateMinHaulers != defaultGateMinHaulers {
		t.Fatalf("scaled gate must carry the documented starved-earner floor: haulers=%d", cfg.GateMinHaulers)
	}
	return cfg
}

// gateSurplusTreasury is a treasury that clears the GATE-entry surplus floor (≥ 550k ⇒ surplus ≥ 500k).
const gateSurplusTreasury = 600_000

// --- GATE requires the full fleet at the scaler target AND a surplus (neither coverage nor $/hr is a gate) ---

// Both conditions met (full fleet ≥ target, a treasury surplus) → GATE: a genuinely scaled, funded contract
// op is the legitimate entry.
func TestBootstrap_ScaledGate_FullFleetAtTargetAndFunded_EntersGate(t *testing.T) {
	cfg := scaledGateCfg(t)
	obs := Observation{
		Haulers:              []HaulerSnapshot{{Symbol: "H1"}, {Symbol: "H2"}}, // 2 delivery
		ContractScalerTarget: 2,                                                // full fleet (2) == target
		Treasury:             gateSurplusTreasury,                              // surplus ≥ floor
	}
	if p := derivePhase(obs, cfg); p != PhaseGate {
		t.Fatalf("full fleet at target + surplus → GATE, got %s", p)
	}
}

// THE ktio case: income spiked enormously but with ZERO haulers (a frigate-only contract payout, not a scaled
// op). Must NOT enter GATE — the full fleet (0) is below the scaler target, and no income reading can buy its
// way past that.
func TestBootstrap_ScaledGate_SpikeWithZeroHaulers_DoesNotGate(t *testing.T) {
	cfg := scaledGateCfg(t)
	obs := Observation{Haulers: nil, ContractScalerTarget: 2, IncomePerHour: 300000, Treasury: 2_000_000}
	if p := derivePhase(obs, cfg); p == PhaseGate {
		t.Fatalf("a 0-hauler income spike must NOT enter GATE (full fleet below target — the ktio deadlock), got %s", p)
	}
}

// Realized $/hr is NOT a GATE-entry condition, at ANY reading. Holding a scaled, funded op fixed and sweeping
// income from deep net-negative (a heavy spend tick) through a huge payout spike derives GATE every time —
// and the SAME sweep on an op one hull SHORT of the target derives GATE at no reading at all. Income is a
// heartbeat number, never a phase signal: it can neither open the gate nor hold it shut.
func TestBootstrap_ScaledGate_IncomeIsNotAPhaseSignal(t *testing.T) {
	cfg := scaledGateCfg(t)
	funded := func(hulls int, income float64) Observation {
		return Observation{
			Haulers:              nHaulers(hulls),
			ContractScalerTarget: 2,
			IncomePerHour:        income,
			Treasury:             gateSurplusTreasury,
		}
	}
	for _, income := range []float64{-500_000, -55_000, 0, 9_999, 60_000, 1_000_000} {
		if p := derivePhase(funded(2, income), cfg); p != PhaseGate {
			t.Errorf("a scaled, funded op at target must enter GATE at income %.0f, got %s", income, p)
		}
		if p := derivePhase(funded(1, income), cfg); p == PhaseGate {
			t.Errorf("an op one hull short of target must NOT enter GATE at income %.0f, got %s", income, p)
		}
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
		Treasury:             gateSurplusTreasury,
	}
	if p := derivePhase(obs, cfg); p != PhaseGate {
		t.Fatalf("a scaled, funded op must enter GATE regardless of coverage (30%%), got %s", p)
	}
}

// Sticky-latch safety: ConstructionStarted still forces GATE (a legitimately-started pipeline resumes on
// restart), and — because construction can only START after a legit scaled+funded entry — this latch can never
// be tripped by an unscaled op. Pins that the sticky branch is unchanged (checked before the entry gate).
func TestBootstrap_ScaledGate_ConstructionStartedStaysGateEvenUnfunded(t *testing.T) {
	cfg := scaledGateCfg(t)
	// Repurposed haulers have pulled the op back under every entry condition, but a pipeline exists.
	obs := Observation{MarketsTotal: 10, MarketsCovered: 10, Haulers: nil, ConstructionStarted: true}
	if p := derivePhase(obs, cfg); p != PhaseGate {
		t.Fatalf("a started pipeline must stay sticky-GATE regardless of fleet size or treasury, got %s", p)
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
		IncomePerHour:        68000, // ≈ live realized $/hr — a healthy-looking number on a 0-hull op
		ProbeCount:           3,     // == defaultProbeTarget: scanning workstream done
		ProbesScouting:       3,     // all scouting
		MarketsTotal:         9,
		MarketsCovered:       8, // coverage ≈ 0.89 — must NOT route to DATA (coverage is not a phase gate)
	}

	// gateFunded is FALSE — the full fleet (0) is below the scaler target (10), and the healthy 68000 $/hr
	// buys no relief — and ConstructionStarted is false (no sticky latch), so the arc falls through to
	// INCOME (probes at target & scouting). The cure the restart delivers.
	if p := derivePhase(stuck, resolveBootstrapConfig(baseCmd(), nil)); p != PhaseIncome {
		t.Fatalf("restart must re-derive INCOME from the stuck-GATE live state (cure the latch), got %s", p)
	}
}

// --- acceptance (multi-tick through reconcileOnce): the ktio trace, replayed through the live coordinator.
// An UNDER-SCALED op rides out the whole income trace — spend ticks and a fat payout spike alike — without
// ever entering GATE; the same trace on an op that has REACHED the scaler target gates immediately. ---

func TestBootstrap_ScaledGate_UnderScaledRidesOutSpike_AtTargetEntersGate(t *testing.T) {
	// The ktio shape: a fat treasury (surplus well over the floor) and a scaler driving toward 2 hulls, but
	// the op holds ONE — so the fleet-vs-target bar is the only thing standing between it and GATE.
	base := Observation{
		HomeSystem: "X1-HQ", ProbeCount: 3, ProbesScouting: 3, HasIdlePurchaser: true,
		MarketsTotal: 10, MarketsCovered: 10,
		Haulers:              nHaulers(1),
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

	// The ktio trace: four net-negative spend ticks, one big contract payout, then spending again. An
	// under-scaled op stays in INCOME through every one of them — the payout buys no entry.
	for i, income := range []float64{-55000, -55000, -55000, -55000, 105000, -55000} {
		obsvr.obs.IncomePerHour = income
		res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), cmd)
		if err != nil {
			t.Fatalf("spike tick %d: %v", i, err)
		}
		if res.Phase == PhaseGate {
			t.Fatalf("an under-scaled op must never enter GATE (tick %d income=%v phase=%s)", i, income, res.Phase)
		}
	}

	// The scaler's second hull lands: the full fleet has reached the target, so the op gates on the VERY
	// NEXT tick — and on the trace's most net-negative reading, proving the fleet bar alone let it in.
	obsvr.obs.Haulers = nHaulers(2)
	obsvr.obs.IncomePerHour = -55000
	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), cmd)
	if err != nil {
		t.Fatalf("at-target tick: %v", err)
	}
	if res.Phase != PhaseGate {
		t.Fatalf("a scaled, funded op at the scaler target must enter GATE, got %s", res.Phase)
	}
}

// --- The era-5 cold-start retune: GATE is reached by a SMALLER contract operation ---

// coldStartScalerTarget is the GATE-entry hull bar an era-5 cold start actually faces: the contract
// auto-scaler's achievable target under its SHIPPED default ceiling. The observer derives this same
// min(plan slots, live ceiling) into obs.ContractScalerTarget, so deriving it from the scaler's own default
// here keeps the phase bar coupled to the knob instead of to a copied literal.
func coldStartScalerTarget() int {
	planSlots := contractscaler.MaxDeliveryHulls + contractscaler.WarehouseUnits + contractscaler.StockerUnits
	return min(planSlots, contractScalerCmd.DefaultContractFleetMaxHulls)
}

// THE ADMIRAL'S ERA-5 BAR: GATE is reached once the contract operation holds THREE hulls. The default
// ceiling is what sets it, so this pins the operational number the retune exists to deliver.
func TestBootstrap_ColdStartGate_EntryBarIsThreeContractHulls(t *testing.T) {
	if got := coldStartScalerTarget(); got != 3 {
		t.Fatalf("cold-start GATE-entry bar = %d contract hulls, want 3 (the era-5 default ceiling)", got)
	}
}

// A funded operation at THREE hulls enters GATE — and the same operation one hull short does NOT. The
// boundary is the whole point: the hull bar moved down, it did not disappear.
func TestBootstrap_ColdStartGate_EntersAtThreeHullsNotTwo(t *testing.T) {
	cfg := scaledGateCfg(t)
	target := coldStartScalerTarget()

	funded := func(hulls int) Observation {
		return Observation{
			Haulers:              nHaulers(hulls),
			ContractScalerTarget: target,
			Treasury:             gateSurplusTreasury,
		}
	}
	if p := derivePhase(funded(target), cfg); p != PhaseGate {
		t.Fatalf("a funded op at the %d-hull target must enter GATE, got %s", target, p)
	}
	if p := derivePhase(funded(target-1), cfg); p == PhaseGate {
		t.Fatalf("a funded op one hull SHORT of the %d-hull target must NOT enter GATE, got %s", target, p)
	}
}

// The depot hulls count toward the bar exactly as the delivery haulers do — the bar is the FULL contract
// fleet, so a mixed 2-delivery + 1-depot operation reaches GATE at the same three hulls.
func TestBootstrap_ColdStartGate_FullFleetCountsDepotHulls(t *testing.T) {
	cfg := scaledGateCfg(t)
	target := coldStartScalerTarget()
	obs := Observation{
		Haulers:                nHaulers(target - 1),
		ContractDepotHullCount: 1,
		ContractScalerTarget:   target,
		Treasury:               gateSurplusTreasury,
	}
	if p := derivePhase(obs, cfg); p != PhaseGate {
		t.Fatalf("delivery+depot reaching the %d-hull target must enter GATE, got %s", target, p)
	}
}

// THE FUNDING BAR SURVIVES THE SMALLER FLEET. At the new three-hull bar the treasury war chest still
// independently BLOCKS entry: an operation whose surplus over the immutable reserve floor is one credit
// short does not gate. Lowering the hull bar must not become a way in on an unfunded operation.
func TestBootstrap_ColdStartGate_ThreeHullsStillBlockedWhenUnfunded(t *testing.T) {
	cfg := scaledGateCfg(t)
	target := coldStartScalerTarget()

	obs := Observation{
		Haulers: nHaulers(target), ContractScalerTarget: target,
		Treasury: common.ImmutableReserveFloor + int64(cfg.GateSurplusFloor) - 1,
	}
	if p := derivePhase(obs, cfg); p == PhaseGate {
		t.Fatalf("a %d-hull op whose surplus is under the gate war chest must NOT enter GATE, got %s", target, p)
	}
}
