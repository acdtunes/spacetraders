package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/tactics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-382j: the construction-supply TERMINAL on the shared ProductionExecutor
// engine. DeliverToConstructionSite flies the (already-sourced) hauler to the
// construction site and supplies whatever it carries of the material via the
// construction supply API — the acquire->navigate->supply->record leg recovered
// from the deleted DeliverToConstructionExecutor (ef2281b8), but as a thin
// terminal that reuses NavigateAndDock instead of a parallel coordinator.

const (
	deliveryTestSiteWP = "X1-DR-GATE" // the jump-gate construction site (distinct from origin)
	deliveryTestGood   = "FAB_MATS"
)

// fakeConstructionRepo records SupplyMaterial calls at the port boundary and
// returns a scripted result. Embeds the domain interface so any unused method
// panics, keeping the fake honest about what the terminal actually calls.
type fakeConstructionRepo struct {
	manufacturing.ConstructionSiteRepository

	supplyCalls []constructionSupplyCall
	unitsResult int // UnitsDelivered to report; when 0, echoes the supplied units
	err         error
}

type constructionSupplyCall struct {
	ship, site, good string
	units, playerID  int
}

func (r *fakeConstructionRepo) SupplyMaterial(_ context.Context, shipSymbol, waypointSymbol, tradeSymbol string, units int, playerID int) (*manufacturing.ConstructionSupplyResult, error) {
	r.supplyCalls = append(r.supplyCalls, constructionSupplyCall{
		ship: shipSymbol, site: waypointSymbol, good: tradeSymbol, units: units, playerID: playerID,
	})
	if r.err != nil {
		return nil, r.err
	}
	delivered := r.unitsResult
	if delivered == 0 {
		delivered = units
	}
	return &manufacturing.ConstructionSupplyResult{UnitsDelivered: delivered}, nil
}

// newDeliveryExecutor wires a ProductionExecutor over the dock-race ship/mediator
// fakes with a fake construction repo. The ship starts DOCKED at a factory origin
// (having just been sourced) and must be flown to the construction site.
func newDeliveryExecutor(t *testing.T, cargo []*shared.CargoItem, constructionRepo manufacturing.ConstructionSiteRepository) (*ProductionExecutor, *dockRaceShipRepo, *dockRaceMediator) {
	t.Helper()
	units := 0
	for _, item := range cargo {
		units += item.Units
	}
	repo := &dockRaceShipRepo{
		location:       dockRaceOrigin,
		navStatus:      navigation.NavStatusDocked,
		cargoCapacity:  40,
		cargoUnits:     units,
		cargoInventory: cargo,
	}
	mediator := &dockRaceMediator{
		repo:        repo,
		dockHandler: tactics.NewDockShipHandler(repo),
	}
	marketLocator := NewMarketLocator(nil, nil, nil, nil)
	executor := NewProductionExecutorWithConfig(
		mediator, repo, nil, marketLocator, &dockRaceClock{}, []time.Duration{time.Millisecond}, nil,
	)
	executor.SetConstructionRepo(constructionRepo)
	return executor, repo, mediator
}

// Acceptance core (sp-382j): a hauler carrying the material is flown to the
// construction site and its cargo is supplied THERE via SupplyMaterial; the
// terminal returns the units the site accepted.
func TestDeliverToConstructionSite_SuppliesOnboardUnitsToSite(t *testing.T) {
	construction := &fakeConstructionRepo{}
	executor, repo, mediator := newDeliveryExecutor(t, makeCargo(deliveryTestGood, 30), construction)

	delivered, err := executor.DeliverToConstructionSite(
		context.Background(), dockRaceShip, deliveryTestGood, deliveryTestSiteWP, shared.MustNewPlayerID(1),
	)
	if err != nil {
		t.Fatalf("supplying a carried material to the site must succeed, got %v", err)
	}
	if delivered != 30 {
		t.Fatalf("expected 30 units delivered to the site, got %d", delivered)
	}
	if len(construction.supplyCalls) != 1 {
		t.Fatalf("expected exactly 1 SupplyMaterial call, got %d", len(construction.supplyCalls))
	}
	call := construction.supplyCalls[0]
	if call.site != deliveryTestSiteWP || call.good != deliveryTestGood || call.units != 30 {
		t.Fatalf("SupplyMaterial called with wrong args: %+v", call)
	}
	if got := repo.locationNow(); got != deliveryTestSiteWP {
		t.Fatalf("expected the hauler flown to the site %s, ended at %s", deliveryTestSiteWP, got)
	}
	if mediator.navAttempts() != 1 {
		t.Fatalf("expected exactly 1 navigation leg to the site, got %d", mediator.navAttempts())
	}
}

// An empty hull must not fly or supply: nothing to deliver short-circuits to a
// zero-unit result before any navigation or API call.
func TestDeliverToConstructionSite_NothingOnboard_NoSupply(t *testing.T) {
	construction := &fakeConstructionRepo{}
	executor, _, mediator := newDeliveryExecutor(t, nil, construction)

	delivered, err := executor.DeliverToConstructionSite(
		context.Background(), dockRaceShip, deliveryTestGood, deliveryTestSiteWP, shared.MustNewPlayerID(1),
	)
	if err != nil {
		t.Fatalf("an empty hull must be a clean no-op, got %v", err)
	}
	if delivered != 0 {
		t.Fatalf("expected 0 units delivered from an empty hull, got %d", delivered)
	}
	if len(construction.supplyCalls) != 0 {
		t.Fatalf("expected no SupplyMaterial call for an empty hull, got %d", len(construction.supplyCalls))
	}
	if mediator.navAttempts() != 0 {
		t.Fatalf("an empty hull must not fly to the site, got %d nav legs", mediator.navAttempts())
	}
}

// With no construction repository wired (the optional-port contract), the terminal is
// unavailable and returns a clear error rather than delivering nothing silently.
func TestDeliverToConstructionSite_RepoNotWired_Errors(t *testing.T) {
	executor, _, mediator := newDeliveryExecutor(t, makeCargo(deliveryTestGood, 30), nil) // nil repo, left unset

	delivered, err := executor.DeliverToConstructionSite(
		context.Background(), dockRaceShip, deliveryTestGood, deliveryTestSiteWP, shared.MustNewPlayerID(1),
	)
	if err == nil {
		t.Fatal("expected an error when the construction repository is not wired")
	}
	if delivered != 0 {
		t.Fatalf("expected 0 delivered when the repo is unwired, got %d", delivered)
	}
	if mediator.navAttempts() != 0 {
		t.Fatalf("an unwired terminal must not fly the hull, got %d nav legs", mediator.navAttempts())
	}
}

// sp-v5d1/sp-j09q (REPRO — the ROOT, fail-when-reverted): after a successful supply removes N units
// server-side, the delivering hull's CACHED cargo MUST be decremented by N. Without the post-supply
// write-back the cache keeps the pre-delivery value (a PHANTOM) — the next drain tick reads the hull
// as still laden, re-routes it to re-deliver, and the server rejects it with API 4219 (→ the sp-6zkg
// hang). This asserts the cache reflects the server-side removal (0 remaining), which is the fix that
// collapses the whole cascade at its root.
func TestDeliverToConstructionSite_WritesBackDecrementedCargo(t *testing.T) {
	construction := &fakeConstructionRepo{}
	executor, repo, _ := newDeliveryExecutor(t, makeCargo(deliveryTestGood, 30), construction)

	delivered, err := executor.DeliverToConstructionSite(
		context.Background(), dockRaceShip, deliveryTestGood, deliveryTestSiteWP, shared.MustNewPlayerID(1),
	)
	if err != nil {
		t.Fatalf("supplying a carried material must succeed, got %v", err)
	}
	if delivered != 30 {
		t.Fatalf("expected 30 units delivered, got %d", delivered)
	}

	// The daemon cache must now reflect the server-side removal: NO phantom cargo remains onboard.
	reloaded, err := repo.FindBySymbol(context.Background(), dockRaceShip, shared.MustNewPlayerID(1))
	if err != nil {
		t.Fatalf("reload after supply: %v", err)
	}
	if got := reloaded.Cargo().GetItemUnits(deliveryTestGood); got != 0 {
		t.Fatalf("expected cached cargo decremented to 0 after supply (no phantom); got %d — a phantom re-routes the hull to re-deliver (sp-j09q) and hangs the drain (sp-6zkg)", got)
	}
	if got := reloaded.Cargo().Units; got != 0 {
		t.Fatalf("expected total cached cargo units 0 after full delivery, got %d", got)
	}
}

// sp-v5d1 (write-back precision): the cache is decremented by the units the site ACCEPTED
// (result.UnitsDelivered), not merely the units offered. A hull carrying 30 whose site accepts only 20
// must leave 10 in the cache — never a stale 30 (phantom) and never an over-decremented negative.
func TestDeliverToConstructionSite_WriteBackUsesDeliveredUnits(t *testing.T) {
	construction := &fakeConstructionRepo{unitsResult: 20} // site accepts 20 of the 30 offered
	executor, repo, _ := newDeliveryExecutor(t, makeCargo(deliveryTestGood, 30), construction)

	delivered, err := executor.DeliverToConstructionSite(
		context.Background(), dockRaceShip, deliveryTestGood, deliveryTestSiteWP, shared.MustNewPlayerID(1),
	)
	if err != nil {
		t.Fatalf("supply must succeed, got %v", err)
	}
	if delivered != 20 {
		t.Fatalf("expected 20 units accepted by the site, got %d", delivered)
	}

	reloaded, err := repo.FindBySymbol(context.Background(), dockRaceShip, shared.MustNewPlayerID(1))
	if err != nil {
		t.Fatalf("reload after supply: %v", err)
	}
	if got := reloaded.Cargo().GetItemUnits(deliveryTestGood); got != 10 {
		t.Fatalf("expected cache decremented by the 20 DELIVERED units (30-20=10 remaining), got %d", got)
	}
}

// A construction supply API failure surfaces as an error and delivers nothing (the drain
// then fails the task) — the error is not swallowed.
func TestDeliverToConstructionSite_SupplyError_Bubbles(t *testing.T) {
	construction := &fakeConstructionRepo{err: errors.New("api rejected supply")}
	executor, repo, _ := newDeliveryExecutor(t, makeCargo(deliveryTestGood, 30), construction)

	delivered, err := executor.DeliverToConstructionSite(
		context.Background(), dockRaceShip, deliveryTestGood, deliveryTestSiteWP, shared.MustNewPlayerID(1),
	)
	if err == nil {
		t.Fatal("expected the supply API error to surface")
	}
	if delivered != 0 {
		t.Fatalf("expected 0 delivered on a supply failure, got %d", delivered)
	}
	// The hull still flew to the site (the failure is at the supply call, after arrival).
	if got := repo.locationNow(); got != deliveryTestSiteWP {
		t.Fatalf("expected the hull to have flown to the site before the failed supply, at %s", got)
	}
}
