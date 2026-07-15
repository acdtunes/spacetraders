package commands

import (
	"context"
	"testing"
)

// The explorer class is OPT-IN: classDisabled is true (skipped) unless explicitly armed. This is the
// coordinator-level arming gate — when disarmed the provider is not even called (belt to the
// provider's suspenders).
func TestExplorer_ClassDisabled_OptInDefaultOff(t *testing.T) {
	disarmed := resolveFleetAutosizerConfig(&RunFleetAutosizerCoordinatorCommand{})
	if !disarmed.classDisabled(HullClassExplorer) {
		t.Fatalf("explorer must be DISABLED by default (opt-in) — nothing boot-arms it")
	}
	armed := resolveFleetAutosizerConfig(&RunFleetAutosizerCoordinatorCommand{ExplorerHullsEnabled: true})
	if armed.classDisabled(HullClassExplorer) {
		t.Fatalf("explorer must be ENABLED once explorer_hulls_enabled=true")
	}
}

// Resolve fills the explorer's protective defaults and — crucially — leaves it DISARMED. Nothing
// boot-arms the ~819k ROI-exempt buy.
func TestExplorer_ResolveDefaults_NothingBootArms(t *testing.T) {
	cfg := resolveFleetAutosizerConfig(&RunFleetAutosizerCoordinatorCommand{})
	if cfg.ExplorerHullsEnabled {
		t.Fatalf("explorer_hulls_enabled must default FALSE (disarmed) — nothing boot-arms it")
	}
	if cfg.FleetCeilingExplorer != defaultFleetCeilingExplorer {
		t.Errorf("explorer hard cap default = %d, want %d", cfg.FleetCeilingExplorer, defaultFleetCeilingExplorer)
	}
	if cfg.FleetCeilingExplorer != 1 {
		t.Errorf("explorer hard cap must be 1, got %d", cfg.FleetCeilingExplorer)
	}
	if cfg.MaxPriceExplorer != defaultMaxPriceExplorer {
		t.Errorf("explorer price ceiling default = %d, want %d (a REAL cap, never 0=off)", cfg.MaxPriceExplorer, defaultMaxPriceExplorer)
	}
	if cfg.ExplorerTreasuryPctPerPurchase != defaultExplorerTreasuryPctPerPurchase {
		t.Errorf("explorer treasury pct default = %d, want %d", cfg.ExplorerTreasuryPctPerPurchase, defaultExplorerTreasuryPctPerPurchase)
	}
	if cfg.ShipTypeExplorer != defaultShipTypeExplorer {
		t.Errorf("explorer ship type default = %q, want %q", cfg.ShipTypeExplorer, defaultShipTypeExplorer)
	}
}

// classGuardConfig hands the explorer its REAL guard bounds — the hard-cap-1 ceiling, the price
// ceiling, and the 25% rule. (The payback exemption is applied inside EvaluateGuards, not here.)
func TestExplorer_ClassGuardConfig_RealBounds(t *testing.T) {
	cfg := resolveFleetAutosizerConfig(&RunFleetAutosizerCoordinatorCommand{ExplorerHullsEnabled: true})
	shipType, ceiling, maxPrice, treasuryPct := classGuardConfig(HullClassExplorer, cfg)
	if shipType != "SHIP_EXPLORER" {
		t.Errorf("explorer ship type = %q, want SHIP_EXPLORER", shipType)
	}
	if ceiling != 1 {
		t.Errorf("explorer class ceiling (hard cap) = %d, want 1", ceiling)
	}
	if maxPrice != defaultMaxPriceExplorer {
		t.Errorf("explorer price ceiling = %d, want %d", maxPrice, defaultMaxPriceExplorer)
	}
	if treasuryPct != 25 {
		t.Errorf("explorer treasury pct = %d, want 25", treasuryPct)
	}
}

// End-to-end at the coordinator: an ARMED explorer provider whose off-gate demand is FIRING drives a
// shortfall through classDisabled+sizeClass; a DISARMED one is skipped entirely. This is the reconcile
// -level proof that arming (config) gates the whole class, complementing the provider-level proof.
func TestExplorer_Reconcile_DisarmedSkipsClassEntirely(t *testing.T) {
	// A provider that would WANT to buy if ever asked (demand firing, pool empty). If the class is
	// skipped when disarmed, its Demand() is never called and the spy never records a call.
	spy := &spyExplorerProvider{inner: NewExplorerDemandProvider(&fakeOffGateSource{demanded: true, wantCount: 1, ok: true}, &fakeExplorerFleet{count: 0})}

	h := NewRunFleetAutosizerCoordinatorHandler(nil)
	h.AddDemandProvider(spy)

	// DISARMED: the class is skipped, the provider is never consulted.
	if _, err := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{ContainerID: "c1"}); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}
	if spy.calls != 0 {
		t.Fatalf("DISARMED explorer class must be SKIPPED (provider never called), got %d calls", spy.calls)
	}

	// ARMED: the class runs and the provider is consulted.
	if _, err := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{ContainerID: "c1", ExplorerHullsEnabled: true}); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}
	if spy.calls == 0 {
		t.Fatalf("ARMED explorer class must be evaluated (provider consulted at least once)")
	}
}

// spyExplorerProvider records how many times the coordinator consults the explorer provider, so a
// test can prove the DISARMED class is skipped before any demand read.
type spyExplorerProvider struct {
	inner *ExplorerDemandProvider
	calls int
}

func (s *spyExplorerProvider) Class() HullClass { return HullClassExplorer }
func (s *spyExplorerProvider) Demand(ctx context.Context, playerID int, params DemandParams) (ClassDemand, error) {
	s.calls++
	return s.inner.Demand(ctx, playerID, params)
}
