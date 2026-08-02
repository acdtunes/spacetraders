package grpc

import (
	"context"
	"fmt"
	"sort"

	contractScalerCmd "github.com/andrescamacho/spacetraders-go/internal/application/contractscaler/commands"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// contractScalerReclaimer implements the coordinator's IdleHullReclaimer: it finds ONE idle, UNDEDICATED,
// CARGO-CAPABLE hull already owned and re-dedicates it EXCLUSIVE to the contract fleet (single-writer
// AssignFleet, RULINGS #3), then homes it demand-ranked exactly like a bought hull (the SAME C1 path).
// It uses the SAME ship repo the FleetCounter reads and the SAME mediator the purchaser homes through —
// no new daemon dependency. RULINGS #7: only DedicatedFleet=="" hulls are ever reclaimed; a hull
// dedicated to ANY fleet (trade/mfg/warehouse/contract) is never poached. Reuse is FREE — never cushion-
// gated — and strictly reduces spend.
type contractScalerReclaimer struct {
	shipRepo navigation.ShipRepository
	med      commandSender
	// gateGraph is the cross-system reachability signal FindReclaimableForHome consults (sp-fihvy). A
	// nil gateGraph (unwired) is tolerated — homeReachable fails open, degrading to the plain scan.
	gateGraph depotHomeRouter
}

// FindReclaimable returns the FIRST reuse-eligible hull symbol, or ok=false when none exists. A fleet-
// read error fails closed (surfaced as an error) so the coordinator falls through to a buy rather than
// silently treating an unknown fleet as "no reusable hull".
func (r *contractScalerReclaimer) FindReclaimable(ctx context.Context, playerID int) (string, bool, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return "", false, err
	}
	ships, err := r.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return "", false, err
	}
	for _, ship := range ships {
		if isReclaimable(ship) {
			return ship.ShipSymbol(), true, nil
		}
	}
	return "", false, nil
}

// FindReclaimableForHome is the depot element's home-scoped reuse tier (sp-fihvy stocker;
// generalized to the warehouse role by sp-fis8y, RULINGS #14): the FIRST reuse-eligible hull (the
// SAME isReclaimable guard — idle, not in transit, undedicated, cargo-capable) that is ALSO in, or
// gate-reachable to, homeSystem — the exact reachability notion depotElementHullViable /
// foreignMarketReachable use (gateGraph.Routable), never a second one invented here. Skipping a
// foreign-but-otherwise-idle candidate here is what stops GrowStocker/GrowWarehouse from ever being
// handed a hull it cannot place viably (e.g. TORWIND-19, stranded at X1-ZK26 with no gate route
// home); the ramp falls through to the equally home-scoped buy tier instead. Role-agnostic by
// signature — it takes a homeSystem, not a role — so no reclaimer-level change was needed to extend
// it to the warehouse; only findReclaimableForDepot's routing (run_contract_scaler.go) picks it for
// both roles now. A read error fails closed (falls through to a buy) exactly like FindReclaimable.
func (r *contractScalerReclaimer) FindReclaimableForHome(ctx context.Context, playerID int, homeSystem string) (string, bool, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return "", false, err
	}
	ships, err := r.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return "", false, err
	}
	for _, ship := range ships {
		if isReclaimable(ship) && r.homeReachable(ctx, ship, homeSystem, playerID) {
			return ship.ShipSymbol(), true, nil
		}
	}
	return "", false, nil
}

// homeReachable reports whether ship's current location is in, or gate-reachable to, homeSystem —
// same-system trivially true; a nil gateGraph (unwired) or an unreadable location fails OPEN so the
// reuse tier degrades to the plain scan rather than refusing every candidate; a Routable read error
// fails CLOSED (skip this candidate) — a scan-time skip is cheap and instantly retried, unlike an
// eviction, so this mirrors foreignMarketReachable's polarity exactly (never invents a second notion).
func (r *contractScalerReclaimer) homeReachable(ctx context.Context, ship *navigation.Ship, homeSystem string, playerID int) bool {
	loc := ship.CurrentLocation()
	if loc == nil {
		return true
	}
	currentSystem := shared.ExtractSystemSymbol(loc.Symbol)
	if currentSystem == "" || currentSystem == homeSystem {
		return true
	}
	if r.gateGraph == nil {
		return true
	}
	routable, err := r.gateGraph.Routable(ctx, currentSystem, homeSystem, playerID)
	if err != nil {
		return false
	}
	return routable
}

// contractScalerDeliveryReleaser implements the coordinator's DeliverySurplusReleaser: it un-dedicates
// surplus "contract" delivery hulls back to the idle UNDEDICATED pool, from where the depot fill's
// reuse-before-buy tier reclaims them into warehouse roles — the re-role of the live-surplus delivery
// hulls after the knee dropped 8→6. It releases IDLE hulls only (never mid-delivery), HIGHEST-symbol
// first (so the ≤6 lowest-symbol hulls keep the fixed slots domainContract.AssignedSlot zips them onto,
// making the released surplus exactly the hulls that own no slot), and NEVER the command frigate
// (RULINGS #7). Un-dedicate is the single write path (AssignFleet to "", RULINGS #3) — free, no spend.
type contractScalerDeliveryReleaser struct {
	shipRepo navigation.ShipRepository
}

// ReleaseSurplusDelivery un-dedicates up to count idle "contract" delivery hulls, returning how many it
// released. A fleet-read error surfaces (the scaler holds the re-role fail-closed); an AssignFleet error
// stops early and returns the partial count (a hull left dedicated is safe — re-roled a later pass).
func (r *contractScalerDeliveryReleaser) ReleaseSurplusDelivery(ctx context.Context, playerID, count int) (int, error) {
	if count <= 0 {
		return 0, nil
	}
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return 0, err
	}
	ships, err := r.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return 0, err
	}
	var releasable []string
	for _, ship := range ships {
		if ship.DedicatedFleet() != contractFleetTag {
			continue // only the exclusive "contract" delivery pool
		}
		if !ship.IsIdle() || ship.IsInTransit() {
			continue // never yank a hull mid-delivery
		}
		if domainContract.IsCommandHull(ship) {
			continue // the flagship is never re-roled (RULINGS #7)
		}
		releasable = append(releasable, ship.ShipSymbol())
	}
	// Highest-symbol first: the ≤6 lowest-symbol hulls keep the fixed slots (AssignedSlot's symbol-zip),
	// so the released surplus is exactly the hulls beyond the knee that own no slot.
	sort.Sort(sort.Reverse(sort.StringSlice(releasable)))
	released := 0
	for _, symbol := range releasable {
		if released >= count {
			break
		}
		if err := r.shipRepo.AssignFleet(ctx, symbol, "", pid); err != nil {
			return released, fmt.Errorf("release surplus delivery %s → undedicated: %w", symbol, err)
		}
		released++
	}
	return released, nil
}

// isReclaimable is the reuse-eligibility guard: IDLE (never mid-task → no stranding), NOT in transit,
// UNDEDICATED (RULINGS #7 — never poach a dedicated hull of ANY fleet), CARGO-CAPABLE (a probe /
// 0-cargo hull re-dedicated to contract can't haul — mirrors the hauling-pin cargo guard), and NOT the
// COMMAND FRIGATE (sp-gvvph, RULINGS #7 — the flagship is excluded from ALL scaler reclaim even when
// undedicated: it stays free for its flagship/last-resort role, never pulled into a delivery/depot seat
// by the scaler's routine scaling). Shared by both FindReclaimable (delivery reuse) and
// FindReclaimableForHome (depot warehouse/stocker reuse), so the exclusion covers every reclaim tier.
func isReclaimable(ship *navigation.Ship) bool {
	return ship.IsIdle() && !ship.IsInTransit() && ship.DedicatedFleet() == "" && ship.CargoCapacity() > 0 && !domainContract.IsCommandHull(ship)
}

// Reclaim re-dedicates the hull EXCLUSIVE to the contract fleet via the single write path (AssignFleet,
// RULINGS #3), then homes it to its fixed placement slot like a bought hull (best-effort homing). It
// returns an error ONLY when the re-dedicate itself fails — the hull is then NOT taken, so the
// coordinator safely buys without double-counting. NO SPEND: reclaim is free and never cushion-gated.
func (r *contractScalerReclaimer) Reclaim(ctx context.Context, order contractScalerCmd.ReclaimOrder) error {
	pid, err := shared.NewPlayerID(order.PlayerID)
	if err != nil {
		return err
	}
	if err := r.shipRepo.AssignFleet(ctx, order.ShipSymbol, contractFleetTag, pid); err != nil {
		return fmt.Errorf("reclaim re-dedicate %s → contract: %w", order.ShipSymbol, err)
	}
	homeContractHull(ctx, contractHoming{med: r.med, shipRepo: r.shipRepo, standbyStations: order.StandbyStations}, order.ShipSymbol, order.PlayerID)
	return nil
}
