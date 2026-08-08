package grpc

import (
	"context"
	"fmt"
	"log"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/application/system/gategraph"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
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

// fleetHeavyCensus adapts the ship repository's tag-INDEPENDENT owned-heavy count. This is
// deliberately NOT autosizerHeavySources.HeavyCount, which counts DedicatedFleet=="trade" and
// therefore measures the trade POOL: a heavy tagged elsewhere would be invisible to it, leaving
// the reservation open and authorising a re-buy of a hull we already own.
type fleetHeavyCensus struct {
	counter heavyHullCounter
}

func (c *fleetHeavyCensus) HeaviesOwned(ctx context.Context, playerID int) (int, error) {
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

// fleetHeavyYardReader adapts the shared heavy-target query to the coordinator's port. It
// TRANSPORTS the answer and re-derives nothing: a second opinion on which yard we are saving
// toward is precisely how a reservation drifts.
type fleetHeavyYardReader struct {
	yards heavyYardInventory
}

func (r *fleetHeavyYardReader) HeavyTarget(ctx context.Context, playerID int) (fleetCmd.HeavyTargetYard, error) {
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

// fleetHeavyYardCatalog reports every KNOWN heavy yard — priced or not — with its gate reach
// from the systems the fleet currently stands in.
//
// Reachability is measured from WHERE OUR HULLS ARE, never from where a hull is parked on station:
// the errand has to fly, so a yard outside the jump bound is not a candidate at any price.
type fleetHeavyYardCatalog struct {
	ranker   heavyYardRanker
	shipRepo navigation.ShipRepository
}

func (c *fleetHeavyYardCatalog) KnownHeavyYards(ctx context.Context, playerID int) ([]fleetCmd.KnownHeavyYard, error) {
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

// scoutPostRoster is the narrow read the errand needs of the scout-post table: every post LIVE in
// the open era, so the hulls those posts name can be marked and left alone. Satisfied by
// *persistence.GormScoutPostRepository, whose ListActive is already era-scoped — a dead era's post
// must not reserve a hull that is genuinely free now.
type scoutPostRoster interface {
	ListActive(ctx context.Context, playerID int) ([]*domainScouting.ScoutPost, error)
}

// heavyYardPricingErrand reads the fleet, proves reach, and either flies a hull or reads a yard.
//
// ErrandHulls deliberately reports EVERY hull with its raw facts and filters nothing: the
// eligibility rule — a spare parked probe, unclaimed by any scout post, idle, not already flying —
// is the application layer's. Pre-filtering here would move that rule where the tests pinning it
// cannot reach.
//
// The scout-post roster is read HERE rather than judged here for the same reason: "a live post
// names this hull" is a durable fact of the scout_posts table, and the refusal it earns is the
// policy's to make.
type heavyYardPricingErrand struct {
	med      common.Mediator
	shipRepo navigation.ShipRepository
	posts    scoutPostRoster
	hops     storedHopDistancer
}

// storedHopDistancer is the narrow gate-graph slice the errand needs: a bounded, PURE STORE walk
// from one origin, costing no API budget. Asserted off the daemon's already-injected gate graph
// (the heavyHullCounter idiom) and pinned below, since a failing assertion looks like a dead feature.
type storedHopDistancer interface {
	StoredHopDistances(ctx context.Context, fromSystem string, targets []string, maxJumps int) (map[string]int, error)
}

var _ storedHopDistancer = (*gategraph.Service)(nil)

// newHeavyYardPricingErrand builds the errand with its scout-post roster taken off the daemon's own
// connection, the way bootstrap_ports builds its era repository.
//
// IT TAKES THE SERVER RATHER THAN THE ROSTER SO THE WIRING CANNOT FORGET THE ROSTER. Without it the
// errand refuses on every tick — fail-closed and correct, and therefore invisible, which is the
// precise failure class sp-gmfvw was filed for. Handing this constructor the object the composition
// already holds makes the omission unexpressible instead of merely tested for.
//
// A nil server or a nil connection leaves the roster nil ON PURPOSE: the errand then refuses at
// READ time (see mannedScoutPostHulls) rather than selecting hulls it cannot prove are free, and
// the port itself stays wired so the boot-time wiring probe still reports the errand as present.
// The gate graph comes from the SAME field the depot launch guard reads, so every cross-system
// reachability verdict has one source; a nil one leaves hops nil and the errand refuses at reach.
func newHeavyYardPricingErrand(server *DaemonServer, med common.Mediator, shipRepo navigation.ShipRepository) *heavyYardPricingErrand {
	e := &heavyYardPricingErrand{med: med, shipRepo: shipRepo}
	if server == nil {
		return e
	}
	e.posts = serverScoutPostRoster(server)
	if hops, ok := server.gateGraph.(storedHopDistancer); ok {
		e.hops = hops
	} else {
		log.Printf("WARNING: fleet growth heavy-pricing errand reach UNWIRED — the gate graph cannot answer stored hop distances, so no carrier's route to a heavy yard can be proved and NO pricing errand will ever fly")
	}
	return e
}

// serverScoutPostRoster resolves the daemon's scout-post table, or nil when it has no connection.
// Written once so the errand and the hull buy read the SAME roster.
func serverScoutPostRoster(server *DaemonServer) scoutPostRoster {
	if server == nil || server.db == nil {
		return nil
	}
	return persistence.NewGormScoutPostRepository(server.db)
}

func (e *heavyYardPricingErrand) ErrandHulls(ctx context.Context, playerID int) ([]fleetCmd.PricingErrandHull, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return nil, err
	}
	manned, err := e.mannedScoutPostHulls(ctx, playerID)
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
			Symbol:          sh.ShipSymbol(),
			Fleet:           sh.DedicatedFleet(),
			Location:        at,
			Idle:            sh.IsIdle(),
			InTransit:       sh.IsInTransit(),
			CargoCapacity:   sh.CargoCapacity(),
			MannedScoutPost: manned[sh.ShipSymbol()],
		})
	}
	return out, nil
}

// mannedScoutPostHulls indexes every hull a live scout post NAMES, across all of its manning slots.
//
// AN UNREADABLE ROSTER REFUSES, and an unwired one refuses too. Reporting the fleet with this fact
// zeroed would reach the policy as "no post mans anything" — the single reading that lets the
// errand take a working scout off station, which is precisely what the roster exists to prevent.
// The whole errand then waits a tick and says so, which costs nothing.
//
// Primary AND extra slots, via MannedHulls: a multi-hull post's second probe is no more available
// than its first, and reading only the scalar column would leave every extra slot exposed.
func (e *heavyYardPricingErrand) mannedScoutPostHulls(ctx context.Context, playerID int) (map[string]bool, error) {
	return mannedScoutPostHulls(ctx, e.posts, playerID)
}

// mannedScoutPostHulls indexes every hull a live scout post NAMES, for every engine drawing on the
// shared probe pool — ONE read, so they cannot disagree about which probes a post already owns.
func mannedScoutPostHulls(ctx context.Context, posts scoutPostRoster, playerID int) (map[string]bool, error) {
	if posts == nil {
		return nil, fmt.Errorf("the scout-post roster is unwired, so no hull can be shown free of a post")
	}
	rows, err := posts.ListActive(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("read the scout posts to leave manned hulls alone: %w", err)
	}
	manned := make(map[string]bool, len(rows))
	for _, p := range rows {
		if p == nil {
			continue
		}
		for _, hull := range p.MannedHulls() {
			manned[hull] = true
		}
	}
	return manned, nil
}

// SendToYard navigates the hull to the yard. NAVIGATION ONLY — presence in orbit is enough for a
// shipyard listing to price, and the purchase path docks on its own account, so the errand never
// docks, never quotes and never spends. It reuses the same route+refuel command the bootstrap
// cold-yard positioner and every other repositioning path use.
//
// NO RETURN TRIP IS ISSUED, and that is a deliberate boundary rather than an oversight. The hull
// arrives idle and the sensing engine's own placement passes own where a spare probe belongs next;
// a return leg written here would be a second placement authority for the same pool. What this
// engine owes the sensing engine is restraint in the SELECTION (one hull at a time, never a hull a
// live post names) — see pricingErrandCarrier — not a competing itinerary.
func (e *heavyYardPricingErrand) SendToYard(ctx context.Context, playerID int, shipSymbol, waypointSymbol string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}
	if _, err := e.med.Send(ctx, &navCmd.NavigateRouteCommand{ShipSymbol: shipSymbol, Destination: waypointSymbol, PlayerID: pid}); err != nil {
		return fmt.Errorf("navigate %s to heavy yard %s: %w", shipSymbol, waypointSymbol, err)
	}
	return nil
}

// HopsFrom asks the gate graph the question the NAVIGATOR will ask, from the system the flight
// actually starts in, at the same heavyYardReachBoundHops the catalogue is capped at. The STORED
// adjacency is a SUBSET of the fetch-through resolver's, so its errors run toward refusing a
// flight; an unwired graph REFUSES, since an empty map reads as an absence of routes.
func (e *heavyYardPricingErrand) HopsFrom(ctx context.Context, fromSystem string, toSystems []string) (map[string]int, error) {
	if e.hops == nil {
		return nil, fmt.Errorf("the stored gate graph is unwired, so no carrier's reach to a heavy yard can be proved")
	}
	return e.hops.StoredHopDistances(ctx, fromSystem, toSystems, heavyYardReachBoundHops)
}

// PriceYardInPlace reads the shipyard a hull of ours is ALREADY STANDING at, so the presence-gated
// ask lands without anything flying. It goes through the shipyard-listings query — the fleet's one
// metered read, which persists what it finds — so it stays counted, paced and rescan-windowed, with
// the token supplied by the mediator from PlayerID. THE CLASS IS LEFT AT ITS DISCRETIONARY ZERO
// VALUE: no money guard consumes the result (every spend guard downstream still takes its own live
// Earning read before a credit moves, RULINGS #4 untouched), and it keeps all three limiters on.
func (e *heavyYardPricingErrand) PriceYardInPlace(ctx context.Context, playerID int, waypointSymbol string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}
	if _, err := e.med.Send(ctx, &shipyardQueries.GetShipyardListingsQuery{
		SystemSymbol:   shared.ExtractSystemSymbol(waypointSymbol),
		WaypointSymbol: waypointSymbol,
		PlayerID:       pid,
	}); err != nil {
		return fmt.Errorf("read the heavy yard %s where our hull already stands: %w", waypointSymbol, err)
	}
	return nil
}
