package commands

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeDemandProvider is a stub ClassDemandProvider recording its calls, shared across the
// autosizer tests.
type fakeDemandProvider struct {
	class      HullClass
	demand     ClassDemand
	err        error
	calls      int
	lastP      int
	lastParams DemandParams
}

func (f *fakeDemandProvider) Class() HullClass { return f.class }

func (f *fakeDemandProvider) Demand(ctx context.Context, playerID int, params DemandParams) (ClassDemand, error) {
	f.calls++
	f.lastP = playerID
	f.lastParams = params
	if f.err != nil {
		return ClassDemand{}, f.err
	}
	d := f.demand
	d.Class = f.class
	return d, nil
}

func newHandlerWith(providers ...ClassDemandProvider) *RunFleetAutosizerCoordinatorHandler {
	h := NewRunFleetAutosizerCoordinatorHandler(nil)
	for _, p := range providers {
		h.AddDemandProvider(p)
	}
	return h
}

// LIVE BY DEFAULT (Admiral no-dark-shipping): an absent config (all zero-value command fields,
// Disabled=false) boots ACTIVE — every enabled class provider is evaluated.
func TestReconcile_LiveByDefault_EvaluatesProviders(t *testing.T) {
	light := &fakeDemandProvider{class: HullClassLight, demand: ClassDemand{Demand: 5, Current: 2, Readable: true}}
	h := newHandlerWith(light)

	res, err := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{PlayerID: 42, ContainerID: "c1"})
	if err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if light.calls != 1 {
		t.Fatalf("expected the live provider evaluated once, got %d", light.calls)
	}
	if light.lastP != 42 {
		t.Fatalf("expected playerID threaded to provider, got %d", light.lastP)
	}
	if res.ClassesEvaluated != 1 {
		t.Fatalf("expected 1 class evaluated, got %d", res.ClassesEvaluated)
	}
	if res.ShortfallClasses != 1 {
		t.Fatalf("expected 1 class with shortfall (lights 5>2), got %d", res.ShortfallClasses)
	}
}

// EXACTLY ONE HEAVY BUYER. The growth coordinator owns trade capacity; the autosizer must not
// evaluate the heavy class at all while both are deployed, or two coordinators would each judge
// affordability against one treasury without seeing the other's spend.
func TestReconcile_HeavyClassIsDisabled(t *testing.T) {
	heavy := &fakeDemandProvider{class: HullClassHeavy, demand: ClassDemand{Demand: 9, Current: 0, Readable: true}}
	h := newHandlerWith(heavy)
	if _, err := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{ContainerID: "c1"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if heavy.calls != 0 {
		t.Fatalf("the autosizer must not evaluate heavy demand, got %d calls", heavy.calls)
	}
}

// A provider infra error must not abort the whole tick — the loop is over PROVIDERS, so a healthy
// sibling still sizes. Both are of the one class this coordinator still sizes; a disabled class
// never reaches the provider at all and so could not express this.
func TestReconcile_ProviderError_DoesNotAbortTick(t *testing.T) {
	broken := &fakeDemandProvider{class: HullClassLight, err: errors.New("db down")}
	healthy := &fakeDemandProvider{class: HullClassLight, demand: ClassDemand{Demand: 4, Current: 1, Readable: true}}
	h := newHandlerWith(broken, healthy)

	res, err := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{ContainerID: "c1"})
	if err != nil {
		t.Fatalf("a single provider error must not fail the tick, got %v", err)
	}
	if healthy.calls != 1 {
		t.Fatalf("the healthy provider must still be evaluated after a sibling errored, got %d", healthy.calls)
	}
	if res.ClassesEvaluated != 1 {
		t.Fatalf("expected 1 class evaluated (the healthy provider; the other errored), got %d", res.ClassesEvaluated)
	}
}

// An unreadable demand read (Readable=false) is fail-closed: counted as evaluated but never a
// shortfall (a missing signal must never drive a buy).
func TestReconcile_UnreadableDemand_NeverShortfall(t *testing.T) {
	light := &fakeDemandProvider{class: HullClassLight, demand: ClassDemand{Demand: 9, Current: 0, Readable: false, Reason: "treasury unreadable"}}
	h := newHandlerWith(light)

	res, err := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{ContainerID: "c1"})
	if err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if res.ShortfallClasses != 0 {
		t.Fatalf("unreadable demand must never register a shortfall (fail-closed), got %d", res.ShortfallClasses)
	}
}

func TestResolveConfig_Defaults(t *testing.T) {
	cfg := resolveFleetAutosizerConfig(&RunFleetAutosizerCoordinatorCommand{})

	if cfg.Tick != defaultAutosizerTickSeconds*time.Second {
		t.Errorf("tick default = %v, want %v", cfg.Tick, defaultAutosizerTickSeconds*time.Second)
	}
	if cfg.PurchaseCapPerTick != defaultPurchaseCapPerTick {
		t.Errorf("purchase cap default = %d, want %d", cfg.PurchaseCapPerTick, defaultPurchaseCapPerTick)
	}
	if cfg.PurchaseMarginOverFloor != defaultPurchaseMarginOverFloor {
		t.Errorf("purchase margin default = %d, want %d", cfg.PurchaseMarginOverFloor, defaultPurchaseMarginOverFloor)
	}
	if cfg.LightRotationSlots != defaultLightRotationSlots {
		t.Errorf("light rotation default = %v, want %v", cfg.LightRotationSlots, defaultLightRotationSlots)
	}
	if cfg.ShipTypeLights != defaultShipTypeLights || cfg.ShipTypeHeavies != defaultShipTypeHeavies {
		t.Errorf("ship type defaults = %q/%q, want %q/%q", cfg.ShipTypeLights, cfg.ShipTypeHeavies, defaultShipTypeLights, defaultShipTypeHeavies)
	}
	// PreferDemandProximalYard is default-TRUE when unset (nil *bool).
	if !cfg.PreferDemandProximalYard {
		t.Errorf("prefer_demand_proximal_yard default = false, want true")
	}
}

// THE OTHER HEAVY BUYER RESOLVES THE SAME ANSWER. This coordinator's percentage-of-treasury knob is
// a different constant on a path disabled by classDisabled, so it cannot bite today — which is
// exactly why it is pinned rather than left alone: two heavy buyers silently disagreeing about a
// revoked ceiling means whichever one is woken next decides fleet policy by accident.
func TestResolveConfig_HeavyTreasuryPctCeilingIsOffAndZeroDoesNotReassertIt(t *testing.T) {
	if pct := resolveFleetAutosizerConfig(&RunFleetAutosizerCoordinatorCommand{}).HeavyTreasuryPctPerPurchase; pct != 0 {
		t.Errorf("an unset knob must leave the ceiling unapplied, got %d%%", pct)
	}
	if pct := resolveFleetAutosizerConfig(&RunFleetAutosizerCoordinatorCommand{HeavyTreasuryPctPerPurchase: 25}).HeavyTreasuryPctPerPurchase; pct != 25 {
		t.Errorf("a configured ceiling must survive the resolve, got %d%%", pct)
	}
	// The class is disabled here, so the resolved knob must never reach a live purchase request.
	if !(autosizerRunConfig{}).classDisabled(HullClassHeavy) {
		t.Fatal("the heavy class must stay disabled in this coordinator — the growth coordinator owns heavy buying")
	}
}

func TestResolveConfig_ExplicitFalseProximalYard(t *testing.T) {
	no := false
	cfg := resolveFleetAutosizerConfig(&RunFleetAutosizerCoordinatorCommand{PreferDemandProximalYard: &no})
	if cfg.PreferDemandProximalYard {
		t.Errorf("explicit prefer_demand_proximal_yard=false must be honoured, got true")
	}
}

func TestResolveConfig_OverridesRespected(t *testing.T) {
	cfg := resolveFleetAutosizerConfig(&RunFleetAutosizerCoordinatorCommand{
		TickIntervalSecs:   60,
		PurchaseCapPerTick: 3,
		LightRotationSlots: 4.0,
	})
	if cfg.Tick != 60*time.Second {
		t.Errorf("tick override = %v, want 60s", cfg.Tick)
	}
	if cfg.PurchaseCapPerTick != 3 {
		t.Errorf("cap override = %d, want 3", cfg.PurchaseCapPerTick)
	}
	if cfg.LightRotationSlots != 4.0 {
		t.Errorf("rotation override = %v, want 4.0", cfg.LightRotationSlots)
	}
}
