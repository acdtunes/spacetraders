package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/application/system/gategraph"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	shipyardDomain "github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

// heavyHullCounter is the concrete ship-repository capability the heavy census needs. It is
// asserted rather than added to navigation.ShipRepository so the broad interface (and every fake
// implementing it) stays untouched; the wiring WARNs loudly if the assertion fails, because a
// missing census fails closed and silently stops heavy buying.
type heavyHullCounter interface {
	CountHeavyHulls(ctx context.Context, playerID shared.PlayerID) (int, error)
}

// autosizerHeavyCensus adapts the ship repository's tag-INDEPENDENT owned-heavy count. This is
// deliberately NOT autosizerHeavySources.HeavyCount, which counts DedicatedFleet=="trade" and
// therefore measures the trade POOL: a heavy tagged elsewhere would be invisible to it, leaving
// the reservation open and authorising a re-buy of a hull we already own.
type autosizerHeavyCensus struct {
	counter heavyHullCounter
}

func (c *autosizerHeavyCensus) HeaviesOwned(ctx context.Context, playerID int) (int, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return 0, err
	}
	return c.counter.CountHeavyHulls(ctx, pid)
}

// heavyYardInventory is the SHARED heavy-target read behind the reservation's price term — the
// very same implementation the sensing buy-floor consumes, so the spender and the withholder can
// never end up saving toward different yards. Satisfied by *shipyardQueries.HeavyTargetFinder.
type heavyYardInventory interface {
	HeavyTarget(ctx context.Context, playerID int) (shipyardQueries.HeavyTarget, error)
}

// autosizerHeavyYardReader adapts the shared heavy-target query to the coordinator's port. It
// TRANSPORTS the answer and re-derives nothing: a second opinion on which yard we are saving
// toward is precisely how a reservation drifts.
type autosizerHeavyYardReader struct {
	yards heavyYardInventory
}

func (r *autosizerHeavyYardReader) HeavyTarget(ctx context.Context, playerID int) (fleetCmd.HeavyTargetYard, error) {
	target, err := r.yards.HeavyTarget(ctx, playerID)
	if err != nil {
		return fleetCmd.HeavyTargetYard{}, err
	}
	return fleetCmd.HeavyTargetYard{
		CapabilityOpen: target.CapabilityOpen,
		Priced:         target.Priced,
		WaypointSymbol: target.WaypointSymbol,
		PurchasePrice:  target.PurchasePrice,
	}, nil
}

// heavyYardRanker is the narrow slice of the reachable-yard finder the errand needs: the rank that
// INCLUDES availability-only rows. Satisfied by *shipyardQueries.ReachableYardFinder.
type heavyYardRanker interface {
	AllYardsSelling(ctx context.Context, playerID int, shipTypes []string, fromSystems []string) ([]shipyardQueries.YardCandidate, error)
}

// heavyYardReachBoundHops is the gate-jump bound a heavy-yard candidate must lie within to be
// flyable — the SAME bound the ranker's own BFS is capped at (ReachableYardFinder is constructed
// with gategraph.MaxJumpPath), named here so the two can be read against each other instead of
// merely believed to agree.
//
// A const, not a knob (RULINGS #5): it is the reach heavies are held to everywhere else — the strict
// path/routability bound the pre-buy guard runs — and a second, looser copy of it living in an
// adapter is exactly how an errand gets dispatched to a yard the purchase can never route to.
const heavyYardReachBoundHops = gategraph.MaxJumpPath

// heavyYardReachable derives whether a ranked candidate is inside the heavy reach bound FROM THE
// ROW'S OWN HOP COUNT, which is the only reachability evidence the candidate actually carries.
//
// A literal `true` here, justified by "rows the ranker returns are reachable by construction",
// is a claim about an implementation this adapter does not own. It holds for
// *ReachableYardFinder — rankYardsSelling drops every row whose
// system is absent from the bounded multi-source BFS, so a returned row's Hops is a real distance in
// [0, gategraph.MaxJumpPath] — but the port depends on the narrow heavyYardRanker interface,
// and a literal cannot tell the difference
// between a rank that filtered and one that did not. Deriving the flag costs nothing, changes no
// verdict for the real ranker (every row it emits satisfies the bound), and fails CLOSED for any
// row that does not: an out-of-bound yard is dropped by the errand policy rather than flown to.
//
// A negative hop count is not evidence of nearness — it is nonsense, and nonsense reads as
// unreachable rather than as "0 hops away".
func heavyYardReachable(hops int) bool {
	return hops >= 0 && hops <= heavyYardReachBoundHops
}

// autosizerHeavyYardCatalog reports every KNOWN heavy yard — priced or not — with its gate reach
// from the systems the fleet currently stands in.
//
// Reachability is measured from WHERE OUR HULLS ARE, never from where a hull is parked on station:
// the errand has to fly, so a yard outside the jump bound is not a candidate at any price.
type autosizerHeavyYardCatalog struct {
	ranker   heavyYardRanker
	shipRepo navigation.ShipRepository
}

func (c *autosizerHeavyYardCatalog) KnownHeavyYards(ctx context.Context, playerID int) ([]fleetCmd.KnownHeavyYard, error) {
	if c.ranker == nil || c.shipRepo == nil {
		return nil, nil
	}
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return nil, err
	}
	ships, err := c.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return nil, fmt.Errorf("read the fleet to measure heavy-yard reach: %w", err)
	}
	candidates, err := c.ranker.AllYardsSelling(ctx, playerID, shipyardDomain.DefaultHeavyShipTypes, distinctShipSystems(ships))
	if err != nil {
		return nil, err
	}
	out := make([]fleetCmd.KnownHeavyYard, 0, len(candidates))
	for _, y := range candidates {
		out = append(out, fleetCmd.KnownHeavyYard{
			SystemSymbol:   y.SystemSymbol,
			WaypointSymbol: y.WaypointSymbol,
			ShipType:       y.ShipType,
			PurchasePrice:  int64(y.PurchasePrice),
			Hops:           y.Hops,
			Reachable:      heavyYardReachable(y.Hops),
		})
	}
	return out, nil
}

// autosizerPricingErrand reads the fleet for the errand policy and flies the chosen hull.
//
// ErrandHulls deliberately reports EVERY hull with its raw facts and filters nothing: the
// eligibility rule — trade-dedicated, cargo-capable, idle, not already flying — is the application
// layer's, and it is the rule that keeps a parked sensing probe out. Pre-filtering here would move
// that rule where the tests pinning it cannot reach.
type autosizerPricingErrand struct {
	med      common.Mediator
	shipRepo navigation.ShipRepository
}

func (e *autosizerPricingErrand) ErrandHulls(ctx context.Context, playerID int) ([]fleetCmd.PricingErrandHull, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return nil, err
	}
	ships, err := e.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return nil, err
	}
	out := make([]fleetCmd.PricingErrandHull, 0, len(ships))
	for _, sh := range ships {
		if sh == nil {
			continue
		}
		// A hull's location IS its destination while it is in transit (Ship.StartTransit), which
		// is what makes "an errand is already under way" a pure read of durable rows.
		at := ""
		if loc := sh.CurrentLocation(); loc != nil {
			at = loc.Symbol
		}
		out = append(out, fleetCmd.PricingErrandHull{
			Symbol:        sh.ShipSymbol(),
			Fleet:         sh.DedicatedFleet(),
			Location:      at,
			Idle:          sh.IsIdle(),
			InTransit:     sh.IsInTransit(),
			CargoCapacity: sh.CargoCapacity(),
		})
	}
	return out, nil
}

// SendToYard navigates the hull to the yard. NAVIGATION ONLY — presence in orbit is enough for a
// shipyard listing to price, and the purchase path docks on its own account, so the errand never
// docks, never quotes and never spends. It reuses the same route+refuel command the bootstrap
// cold-yard positioner and every other repositioning path use.
func (e *autosizerPricingErrand) SendToYard(ctx context.Context, playerID int, shipSymbol, waypointSymbol string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}
	if _, err := e.med.Send(ctx, &navCmd.NavigateRouteCommand{ShipSymbol: shipSymbol, Destination: waypointSymbol, PlayerID: pid}); err != nil {
		return fmt.Errorf("navigate %s to heavy yard %s: %w", shipSymbol, waypointSymbol, err)
	}
	return nil
}
