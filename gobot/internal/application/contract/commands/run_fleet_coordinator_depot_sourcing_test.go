package commands

import (
	"testing"

	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
)

// sp-obtr: an owned-destination (depot-routed) contract must branch hull selection on
// whether the good is BUFFERED in the hub warehouse. The buffered/unbuffered signal is
// the sourcing plan's Source (resolved by PlanSourcing/InventorySourceFinder):
// SourceInventory == staged at the hub warehouse (zero-ask withdrawal, deliver from
// stock); SourceMarket == must be bought at a REMOTE source market.
//
// resolveContractHullRoute is the seam the coordinator consults AFTER routeContractViaDepot
// (the depot match) and PlanSourcing (the sourcing plan), combining the two:
//
//   - BUFFERED (SourceInventory): keep sp-9j9c's co-located, destination-nearest depot
//     delivery hull — it withdraws from stock and delivers locally (~0 source travel).
//   - UNBUFFERED (SourceMarket): the good must be fetched from a remote source market, so
//     the destination-pinned depot hull would fly empty to the far source then back (~2x)
//     and leave its hub uncovered. The coordinator must NOT use the depot hull; it falls
//     through to source-nearest idle-hull selection (SelectClosestShip toward the SOURCE
//     market over the idle pool, which already excludes the depot hull per sp-3l64).
//
// Live case (2026-07-15, player-3 X1-VB74): AMMONIA_ICE x50 -> J58 (owned destination),
// NOT in the J58 buffer, source ~750 units away at H53. The depot hull T15 sits AT J58
// (~750 from the source); idle hull 18 sits AT the source. Routing to T15 costs
// ~1500 units (J58->source empty->J58 loaded); sourcing with 18 costs ~750 (source->J58
// loaded) — HALF the travel, on an already-idle hull, and T15 stays on its hub.

// ammoniaUnbufferedPlan builds the live UNBUFFERED shape: the good is not staged, so it
// must be bought at the remote source market H53 (~750 from the J58 destination).
func ammoniaUnbufferedPlan() *appContract.SourcingPlan {
	return &appContract.SourcingPlan{
		Good:   "AMMONIA_ICE",
		Market: "X1-VB74-H53", // remote SOURCE market, far from the J58 destination
		Source: appContract.SourceMarket,
	}
}

// stagedBufferedPlan builds the BUFFERED shape: the good is staged in the hub warehouse at
// zero ask, so the co-located depot hull withdraws from stock and delivers locally.
func stagedBufferedPlan(warehouse string) *appContract.SourcingPlan {
	return &appContract.SourcingPlan{
		Good:   "AMMONIA_ICE",
		Market: warehouse, // co-located destination warehouse (withdraw-local)
		Source: appContract.SourceInventory,
	}
}

// TestResolveContractHullRoute_UnbufferedGood_DoesNotUseDepotHull is the sp-obtr fix at the
// coordinator seam: for an owned-destination contract whose good is UNBUFFERED (must be
// sourced from a remote market), the coordinator must NOT dispatch the destination-pinned
// depot delivery hull. It falls through to source-nearest idle-hull selection, so the
// sourcing hull is the one nearest the SOURCE market (the live hull-18 pick), cutting total
// travel ~2x -> ~1x and keeping the depot hull on its hub.
func TestResolveContractHullRoute_UnbufferedGood_DoesNotUseDepotHull(t *testing.T) {
	const dest = "X1-VB74-J58"
	route := depotRoute{DepotID: "vb74", DeliveryHull: "T15", Warehouse: dest} // depot hull pinned AT J58
	plan := ammoniaUnbufferedPlan()

	hullRoute := resolveContractHullRoute(route, true /* depot owns destination */, plan)

	if hullRoute.UseDepotHull {
		t.Fatalf("UNBUFFERED good must NOT dispatch the destination-pinned depot hull %q "+
			"(it would fly empty ~750 to the source then ~750 back = ~2x, and leave its hub "+
			"uncovered); the coordinator must fall through to source-nearest idle-hull selection",
			route.DeliveryHull)
	}
	if hullRoute.DepotHull != "" {
		t.Fatalf("unbuffered route must carry no depot hull to dispatch (fall through to "+
			"source-nearest selection), got %q", hullRoute.DepotHull)
	}
}

// TestResolveContractHullRoute_BufferedGood_UsesCoLocatedDepotHull is the regression guard:
// when the good IS buffered in the hub warehouse (SourceInventory), the destination-nearest
// depot delivery hull is still used (sp-9j9c preserved) — it withdraws from co-located stock
// and delivers locally, so no ~2x sourcing trip and the depot hull is not pulled off its hub.
func TestResolveContractHullRoute_BufferedGood_UsesCoLocatedDepotHull(t *testing.T) {
	const dest = "X1-VB74-J58"
	route := depotRoute{DepotID: "vb74", DeliveryHull: "T15", Warehouse: dest}
	plan := stagedBufferedPlan(dest)

	hullRoute := resolveContractHullRoute(route, true, plan)

	if !hullRoute.UseDepotHull {
		t.Fatal("BUFFERED good must keep the co-located destination-nearest depot delivery hull " +
			"(sp-9j9c) - deliver from stock, no regression")
	}
	if hullRoute.DepotHull != "T15" {
		t.Fatalf("buffered route must dispatch the co-located depot delivery hull T15, got %q", hullRoute.DepotHull)
	}
}

// TestResolveContractHullRoute_NoOwningDepot_FallsThroughToDefault keeps the sp-u9xa
// regression guard: when no depot owns the destination (routeMatched == false), the default
// source-nearest selection runs regardless of the sourcing source — a depot hull is never
// fabricated. Parametrized over both sourcing sources (Mandate 5: input variations of one
// behavior).
func TestResolveContractHullRoute_NoOwningDepot_FallsThroughToDefault(t *testing.T) {
	cases := []struct {
		name   string
		source appContract.SourcingSource
	}{
		{"no depot + unbuffered market source", appContract.SourceMarket},
		{"no depot + inventory source", appContract.SourceInventory},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			plan := &appContract.SourcingPlan{Good: "IRON_ORE", Market: "X1-SYS-MKT", Source: tc.source}

			hullRoute := resolveContractHullRoute(depotRoute{}, false /* no owning depot */, plan)

			if hullRoute.UseDepotHull {
				t.Fatal("no owning depot must fall through to default source-nearest selection " +
					"(sp-u9xa off-switch), never a depot hull")
			}
		})
	}
}
