package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/contract/depotstore"
	contractScalerCmd "github.com/andrescamacho/spacetraders-go/internal/application/contractscaler/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
)

// contractScalerDepotPriceReader adapts the autosizer yard-price walk's home-scoped PriceForSystem
// (sp-fihvy) to the scaler's DepotPriceReader port: the depot STOCKER's buy-fallback yard search,
// restricted to the warehouse's own home system — never a foreign-but-technically-routable yard —
// because a bought stocker hull is crewed at its purchase yard with no repositioning step of its own.
// readable=false ⇒ the depot stocker buy holds (fail-closed — no price, no cushion check, no buy).
type contractScalerDepotPriceReader struct{ yards *autosizerYardPriceReader }

func (p *contractScalerDepotPriceReader) NextHullPriceForHome(ctx context.Context, playerID int, shipType, homeSystem string) (int64, string, bool, error) {
	if p.yards == nil {
		return 0, "", false, nil
	}
	return p.yards.PriceForSystem(ctx, playerID, shipType, homeSystem)
}

// contractScalerDepotCounter reads the contract depot's warehouse/stocker element counts from the
// persistent depot registry (LoadRegistry → len(Warehouses()) / len(Stockers())), summed across the
// player's depots — there is one contract depot, but summing is correct and future-proof. This is the
// ramp's per-role Current for the depot roles: reading the REGISTRY (not the "contract"-tag ship count)
// is what makes a ceiling raise RECONCILE the existing depot (add only the plan-short warehouse/stocker)
// rather than buy a duplicate of the already-present TORWIND-15/11. storeFor builds the player-scoped
// Store exactly as every depot handler does (s.depotStore(playerID)), so the count is the same registry
// the boot reload + contract routing consult — restart-safe (RULINGS #2).
type contractScalerDepotCounter struct {
	storeFor func(playerID int) *depotstore.Store
}

func (c *contractScalerDepotCounter) WarehouseCount(ctx context.Context, playerID int) (int, error) {
	return c.count(ctx, playerID, func(d *depot.ContractDepot) int { return len(d.Warehouses()) })
}

func (c *contractScalerDepotCounter) StockerCount(ctx context.Context, playerID int) (int, error) {
	return c.count(ctx, playerID, func(d *depot.ContractDepot) int { return len(d.Stockers()) })
}

// count loads the player's depot registry and sums one element class across every depot. A load error
// surfaces (the ramp holds fail-closed); an empty registry yields 0 (nothing actuated yet).
func (c *contractScalerDepotCounter) count(ctx context.Context, playerID int, of func(*depot.ContractDepot) int) (int, error) {
	reg, err := c.storeFor(playerID).LoadRegistry(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, d := range reg.Depots() {
		total += of(d)
	}
	return total, nil
}

// depotGrowthLauncher is the narrow slice of the depot launch verbs the grower dispatches — the warehouse
// + stocker launches only (*DaemonServer satisfies it). launchDepotWarehouse itself now PINS the fixed
// far-source whitelist (the miner is gone), so the grower passes no goods — it wires placement only.
// Kept narrow + injectable so the grower's store+launch composition is unit-tested against a spy.
type depotGrowthLauncher interface {
	launchDepotWarehouse(ctx context.Context, shipSymbol, warehouseWaypoint string, playerID int) error
	launchDepotStocker(ctx context.Context, shipSymbol, warehouseWaypoint string, playerID int) error
}

// contractScalerDepotGrower grows the EXISTING contract depot one element at a time by composing the
// persistent depot store (AddElement / AddDepot — RULINGS #2/#3, single-writer durable topology) with
// the depot launch verbs. It ADDS to the depot at the plan hub (never rebuilds it), so a ceiling raise
// reconciles TORWIND-15/11 + -18 up to the plan targets rather than duplicating warehouses. The warehouse
// goods are the FIXED far-source whitelist pinned by launchDepotWarehouse itself (universe-invariant,
// never recomputed — sp-9le3x / st-wisp-2h6r5), so the grower owns placement, not the buffer policy. Same
// persist-then-launch idiom startDepot uses (container_ops_depot_lifecycle.go).
type contractScalerDepotGrower struct {
	storeFor func(playerID int) *depotstore.Store
	launcher depotGrowthLauncher
}

// GrowWarehouse adds one warehouse hull to the depot anchored at order.Hub — AddElement onto the
// existing depot, or AddDepot when none exists yet (the anchor warehouse NewContractDepot requires) —
// then launches the warehouse coordinator on the hull. launchDepotWarehouse pins the FIXED far-source
// whitelist itself; it re-dedicates the hull to the "warehouse" fleet via positionDepotElementHull, so an
// undedicated reclaimed/bought hull is adopted cleanly.
func (g *contractScalerDepotGrower) GrowWarehouse(ctx context.Context, order contractScalerCmd.DepotGrowOrder) error {
	store := g.storeFor(order.PlayerID)
	depotID, exists, err := g.depotAtHub(ctx, order.PlayerID, order.Hub)
	if err != nil {
		return err
	}
	element := depot.Element{Waypoint: order.Hub, ShipSymbol: order.ShipSymbol}
	if exists {
		if err := store.AddElement(ctx, depotID, depot.RoleWarehouse, element); err != nil {
			return err
		}
	} else {
		created, err := depot.NewContractDepot(depotID, []depot.Element{element}, nil, nil, nil)
		if err != nil {
			return err
		}
		if err := store.AddDepot(ctx, created); err != nil {
			return err
		}
	}
	return g.launcher.launchDepotWarehouse(ctx, order.ShipSymbol, order.Hub, order.PlayerID)
}

// GrowStocker adds one stocker hull to the depot anchored at order.Hub and launches the stocker pointed
// at the warehouse anchor (the deposit target = the hub). The fixed plan's fill order guarantees a
// warehouse (the anchor) already exists before any stocker; a stocker with no anchor depot is a
// programming error surfaced loudly rather than a fabricated depot.
func (g *contractScalerDepotGrower) GrowStocker(ctx context.Context, order contractScalerCmd.DepotGrowOrder) error {
	store := g.storeFor(order.PlayerID)
	depotID, exists, err := g.depotAtHub(ctx, order.PlayerID, order.Hub)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("contract scaler: cannot grow a stocker at %s — no depot anchored there (a warehouse fills first)", order.Hub)
	}
	if err := store.AddElement(ctx, depotID, depot.RoleStocker, depot.Element{Waypoint: order.Hub, ShipSymbol: order.ShipSymbol}); err != nil {
		return err
	}
	return g.launcher.launchDepotStocker(ctx, order.ShipSymbol, order.Hub, order.PlayerID)
}

// depotAtHub finds the id of the depot whose warehouse anchor sits at hub (the one contract depot),
// returning exists=false + a deterministic new id when none is anchored there yet (so a fresh grow
// creates a hub-stable, restart-idempotent depot). A registry-load error surfaces (fail-closed).
func (g *contractScalerDepotGrower) depotAtHub(ctx context.Context, playerID int, hub string) (string, bool, error) {
	reg, err := g.storeFor(playerID).LoadRegistry(ctx)
	if err != nil {
		return "", false, err
	}
	for _, d := range reg.Depots() {
		for _, w := range d.Warehouses() {
			if w.Waypoint == hub {
				return d.ID(), true, nil
			}
		}
	}
	return "contract-depot-" + hub, false, nil
}
