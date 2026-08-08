package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractCmd "github.com/andrescamacho/spacetraders-go/internal/application/contract/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/contract/depotstore"
	contractScalerCmd "github.com/andrescamacho/spacetraders-go/internal/application/contractscaler/commands"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/hullbuy"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// NewContractScalerCoordinatorHandler assembles the standing contract auto-scaler handler, wiring every
// coordinator port to a concrete daemon collaborator: the NOVEL RoleResolver (home-system geometry +
// market roles), the treasury/yard-price REUSE of the autosizer idioms, the exclusive-"contract"-fleet
// counter, the buy+dedicate+home Purchaser (the kept autosizer buy primitive + the demand-ranked homing
// consumer), and the live-tunable ceiling (the Pattern-C ContainerConfigReader). gateGraph (sp-fihvy) is
// the SAME cross-system reachability service the daemon's depot stocker viability guard uses — passed
// through here so the depot STOCKER role's reuse/buy tiers can be scoped to the home system too (RULINGS
// #14), never inventing a second reachability notion. Registering it changes NO live behaviour — nothing
// launches the coordinator until the bootstrap early-scaling arm fires (default-off), so a bare deploy is
// byte-identical.
func NewContractScalerCoordinatorHandler(
	server *DaemonServer,
	apiClient *api.SpaceTradersClient,
	ledgerTreasury *persistence.LedgerTreasury,
	shipRepo navigation.ShipRepository,
	med common.Mediator,
	waypointRepo *persistence.GormWaypointRepository,
	marketRepo market.MarketRepository,
	scannedYards scannedYardRanker,
	gateGraph depotHomeRouter,
) *contractScalerCmd.RunContractScalerHandler {
	h := contractScalerCmd.NewRunContractScalerHandler(nil)

	// The NOVEL port: the once-at-arm role lookup. The waypoint lister is assigned only when non-nil so
	// a typed-nil pointer inside the interface field cannot defeat the reader's nil guard (fail-closed on
	// an unwired waypoint surface → empty era) with a runtime panic instead.
	resolver := &contractScalerRoleResolver{
		home:    &contractScalerShipHomeReader{shipRepo: shipRepo},
		markets: marketRepo,
	}
	if waypointRepo != nil {
		resolver.waypoints = waypointRepo
	}
	h.SetRoleResolver(resolver)

	// Treasury: the SAME reader the autosizer's cushion guard uses (fail-closed on an unreadable balance).
	h.SetTreasuryReader(&fleetTreasuryReader{api: apiClient, ledger: ledgerTreasury})

	// Yard price: the autosizer's cheapest-known-yard walk, adapted to the scaler's narrower
	// NextHullPrice port. The concrete waypoint repo is assigned only when non-nil (the same typed-nil
	// guard the autosizer applies).
	yardPriceReader := &fleetYardPriceReader{med: med, shipRepo: shipRepo, scannedYards: scannedYards}
	if waypointRepo != nil {
		yardPriceReader.waypointRepo = waypointRepo
	}
	h.SetPriceReader(&contractScalerPriceReader{reader: yardPriceReader})

	// Current fleet = the exclusive "contract"-dedicated hulls (the ramp's Current).
	h.SetFleetCounter(&contractScalerFleetCounter{shipRepo: shipRepo})

	// Purchaser: the kept autosizer buy+dedicate primitive (dedicates to "contract" via the
	// contract-delivery HullClass mapping) composed with the demand-ranked homing dispatch.
	h.SetPurchaser(&contractScalerPurchaser{
		buyer:    &fleetHullPurchaser{med: med, shipRepo: shipRepo},
		med:      med,
		shipRepo: shipRepo,
	})

	// Reclaimer: the ZERO-SPEND reuse tier tried before every buy (RULINGS #7 — reclaim only an idle
	// UNDEDICATED cargo-capable hull, never poach). Reuses the SAME ship repo the FleetCounter reads +
	// the SAME mediator the purchaser homes through — no new daemon dependency. The SAME instance also
	// serves BOTH depot roles' home-scoped reuse tier (sp-fihvy stocker; generalized to the warehouse
	// by sp-fis8y, RULINGS #14): FindReclaimableForHome additionally requires the candidate be in, or
	// gate-reachable to, the depot's home system via gateGraph — the identical Routable notion the
	// daemon's depot-element-viability guard consults.
	reclaimer := &contractScalerReclaimer{shipRepo: shipRepo, med: med, gateGraph: gateGraph}
	h.SetIdleHullReclaimer(reclaimer)
	h.SetDepotHullReclaimer(reclaimer)

	// Surplus-delivery releaser: un-dedicates idle "contract" delivery hulls over the knee so
	// the depot fill reclaims them into warehouse roles — the re-role that re-composes the live 8+5+1 to
	// 6+7+1 with no new hulls. Reuses the SAME ship repo; no new daemon dependency.
	h.SetDeliverySurplusReleaser(&contractScalerDeliveryReleaser{shipRepo: shipRepo})

	// Depot-hull buy price (sp-fihvy, RULINGS #14): the STOCKER role's buy-fallback yard search is
	// home-system-ONLY — a bought stocker hull is crewed at its purchase yard with no repositioning
	// step of its own, unlike a delivery buy (cross-gated home afterward) or the warehouse role
	// (unchanged, out of this fix's scope, still the shared fleet-wide-cheapest PriceReader below).
	// Wraps the SAME yardPriceReader constructed above — no new yard-price mechanism.
	h.SetDepotPriceReader(&contractScalerDepotPriceReader{yards: yardPriceReader})

	// Ceiling: the live Pattern-C snapshot of the container's own config column (contract_fleet_max_hulls),
	// so a `tune --operation contractscaler` lands on the next tick with no restart.
	h.SetCeilingReader(NewContainerConfigReader(server.containerRepo))

	// Depot actuation: arm the warehouse/stocker half of the fixed plan. The counter reads the
	// persistent depot registry (per-role Current); the grower adds one element at a time via the depot
	// store + the surviving launch verbs. BOTH over server.depotStore — the SAME player-scoped store the
	// boot reload + contract routing consult — so the reconcile is restart-safe (RULINGS #2). *DaemonServer
	// is the launcher (its launchDepotWarehouse/launchDepotStocker). Registering changes NO live behaviour:
	// at the default ceiling (2) the ramp reaches no warehouse index → zero depot calls → byte-identical.
	wireContractScalerDepotActuation(h, server.depotStore, server)
	return h
}

// wireContractScalerDepotActuation arms the ramp's depot half: the depot-registry counter (per-role
// Current) and the AddElement + launch grower, both over the player-scoped depot store. Extracted so
// the wiring is unit-tested against an in-memory store + a spy launcher (no DaemonServer), and so the
// counter/grower share ONE construction point. Unset (never called) ⇒ the ramp is delivery-only.
func wireContractScalerDepotActuation(h *contractScalerCmd.RunContractScalerHandler, storeFor func(playerID int) *depotstore.Store, launcher depotGrowthLauncher) {
	h.SetDepotElementCounter(&contractScalerDepotCounter{storeFor: storeFor})
	h.SetDepotGrower(&contractScalerDepotGrower{storeFor: storeFor, launcher: launcher})
}

// contractScalerPriceReader wraps the autosizer's YardPriceReader, asking it for the contract-delivery
// class's cheapest priced yard and projecting the (price, yard, readable) triple the scaler needs.
// readable=false ⇒ the ramp holds (fail-closed — no price, no cushion check, no buy).
type contractScalerPriceReader struct{ reader hullbuy.YardPriceReader }

func (p *contractScalerPriceReader) NextHullPrice(ctx context.Context, playerID int, shipType string) (int64, string, bool, error) {
	price, _, yard, readable, err := p.reader.PriceFor(ctx, playerID, hullbuy.HullClassContractDelivery, shipType, false)
	return price, yard, readable, err
}

// contractScalerFleetCounter counts the exclusive "contract"-dedicated pool — the ramp's Current.
type contractScalerFleetCounter struct{ shipRepo navigation.ShipRepository }

func (c *contractScalerFleetCounter) ContractHullCount(ctx context.Context, playerID int) (int, error) {
	return countShips(ctx, c.shipRepo, playerID, func(sh *navigation.Ship) bool {
		return sh.DedicatedFleet() == contractFleetTag
	})
}

// contractScalerBuyer is the narrow buy+dedicate primitive the purchaser composes — the kept
// fleetHullPurchaser.BuyAndDedicate (the money-integrity batch path + dedicate-at-purchase).
type contractScalerBuyer interface {
	BuyAndDedicate(ctx context.Context, order hullbuy.BuyOrder) (hullbuy.BuyResult, error)
}

// contractScalerPurchaser executes ONE scaler buy: it drives the kept autosizer buy primitive to
// purchase the light frame and stamp it EXCLUSIVE to the "contract" fleet (via the contract-delivery
// HullClass mapping), then dispatches the contract HomeShipCommand to home the fresh hull DEMAND-RANKED
// across the spread standby set. Homing is best-effort: a completed purchase is never failed because a
// homing hop hiccupped — the fleet coordinator's between-legs homing re-homes idle hulls anyway.
type contractScalerPurchaser struct {
	buyer    contractScalerBuyer
	med      commandSender
	shipRepo navigation.ShipRepository
}

// commandSender is the narrow mediator slice the purchaser needs to dispatch the HomeShipCommand.
// Satisfied by common.Mediator.
type commandSender interface {
	Send(ctx context.Context, request common.Request) (common.Response, error)
}

func (p *contractScalerPurchaser) BuyAndHome(ctx context.Context, order contractScalerCmd.BuyOrder) (contractScalerCmd.BuyResult, error) {
	res, err := p.buyer.BuyAndDedicate(ctx, hullbuy.BuyOrder{
		PlayerID:      order.PlayerID,
		Class:         hullbuy.HullClassContractDelivery, // → dedicates to the exclusive "contract" fleet
		ShipType:      order.Unit.ShipType,
		Yard:          order.Yard,
		ExpectedPrice: order.ExpectedPrice,
	})
	if err != nil {
		return contractScalerCmd.BuyResult{}, err
	}
	homeContractHull(ctx, contractHoming{med: p.med, shipRepo: p.shipRepo, standbyStations: order.StandbyStations}, res.ShipSymbol, order.PlayerID)
	return contractScalerCmd.BuyResult{ShipSymbol: res.ShipSymbol, Price: res.Price}, nil
}

// BuyHull buys ONE UNDEDICATED light hull for a DEPOT role (warehouse/stocker) — the kept buy
// primitive driven with HullClassLight, whose dedicate-at-purchase tag is EMPTY
// (hullbuy.DedicatedFleet(light)==""). It is deliberately NOT contract-dedicated and NOT homed: the
// grower's launch (positionDepotElementHull) re-dedicates the idle hull to its "warehouse"/"stocker"
// fleet, so leaving it undedicated is what lets that adoption succeed (a "contract"-tagged hull would
// be refused by the depot never-poach guard). This mirrors the reclaim path, which likewise hands the
// grower an undedicated idle hull; the buy is the fallback when no reclaimable hull is free.
func (p *contractScalerPurchaser) BuyHull(ctx context.Context, order contractScalerCmd.BuyOrder) (contractScalerCmd.BuyResult, error) {
	res, err := p.buyer.BuyAndDedicate(ctx, hullbuy.BuyOrder{
		PlayerID:      order.PlayerID,
		Class:         hullbuy.HullClassLight, // undedicated (no tag) → the grower re-dedicates to the role fleet
		ShipType:      order.Unit.ShipType,
		Yard:          order.Yard,
		ExpectedPrice: order.ExpectedPrice,
	})
	if err != nil {
		return contractScalerCmd.BuyResult{}, err
	}
	return contractScalerCmd.BuyResult{ShipSymbol: res.ShipSymbol, Price: res.Price}, nil
}

// contractHoming is the collaborator pair plus the placement set a home-return runs against.
type contractHoming struct {
	med             commandSender
	shipRepo        navigation.ShipRepository
	standbyStations []string
}

// homeContractHull dispatches the FIXED-placement homing for a hull that JUST JOINED the contract
// fleet — bought OR reclaimed — carrying the ≤6 fixed placement slots the HomeShipCommand zips this
// hull onto by symbol (no demand). SHARED so a reclaimed hull homes byte-for-byte like a bought one.
// Best-effort: it logs and swallows a homing error so a completed buy/reclaim is never rolled back.
// FleetShips (the contract-fleet roster the placement slots are zipped against) is populated best-effort
// from the ship repo — an empty roster degrades to this hull owning the first slot, never an error.
func homeContractHull(ctx context.Context, h contractHoming, shipSymbol string, playerID int) {
	if len(h.standbyStations) == 0 {
		return // no placement set → nothing to home to (homing is opt-in on the standby set)
	}
	logger := common.LoggerFromContext(ctx)
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return
	}
	// A hull reclaimed/bought while idle in a FOREIGN system cannot be homed by the
	// intra-system spread-home below — HomeShipHandler resolves the standby parks in the hull's
	// CURRENT system graph, so a foreign hull fails ("none of the configured standby stations found
	// in system X graph") and STRANDS (dedicated to "contract" but idle in the wrong system, never
	// re-picked). Cross-gate it back to a home-system park FIRST; a hull already in the home system
	// skips this entirely (byte-identical). SCALER-ONLY: homeContractHull is called only by the
	// scaler's buy/reclaim home-return — idle-arb's deliberately leashed hub-local re-home never runs here.
	repositionForeignHullHome(ctx, h, shipSymbol, pid, logger)

	if _, err := h.med.Send(ctx, &contractCmd.HomeShipCommand{
		ShipSymbol:      shipSymbol,
		PlayerID:        pid,
		StandbyStations: h.standbyStations,
		FleetShips:      contractFleetPeers(ctx, h.shipRepo, pid),
	}); err != nil {
		logger.Log("WARN", fmt.Sprintf("Contract scaler homed %s but homing failed (best-effort; between-legs homing will retry): %v", shipSymbol, err), map[string]interface{}{
			"action": "contract_scaler_home_failed", "ship_symbol": shipSymbol,
		})
	}
}

// repositionForeignHullHome cross-gates a hull that JUST JOINED the contract fleet (bought OR reclaimed)
// from a FOREIGN system back to a home-system standby park, so the subsequent intra-system spread-home
// can place it rather than fail-and-strand (sp-orooy — HomeShipHandler resolves parks in the hull's
// CURRENT system only). The home system is read straight off a standby park symbol (the parks ARE the
// home-system geometry). It is a NO-OP when the hull is already in the home system (byte-identical to the
// previous homing) or when its location is unreadable (defer to the intra-system home + between-legs self-
// heal). Best-effort: a reposition hiccup only logs — the completed reclaim/buy is never rolled back, and
// the contract coordinator's between-legs homing still retries. The move REUSES NavigateRouteCommand's
// wired cross-system delegation (sp-9l4p → RepositionToWaypoint → the SAME multi-jump travel() the
// scout/arb/trade circuits ride), never hand-rolled pathfinding.
func repositionForeignHullHome(ctx context.Context, h contractHoming, shipSymbol string, pid shared.PlayerID, logger common.ContainerLogger) {
	if h.shipRepo == nil {
		return
	}
	homeSystem := shared.ExtractSystemSymbol(h.standbyStations[0])
	ship, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, pid)
	if err != nil || ship == nil || ship.CurrentLocation() == nil {
		return // unreadable location → let the intra-system home try (and self-heal via between-legs homing)
	}
	currentSystem := shared.ExtractSystemSymbol(ship.CurrentLocation().Symbol)
	if currentSystem == "" || currentSystem == homeSystem {
		return // already home (or unknown) → the intra-system spread-home handles it (byte-identical)
	}
	if _, err := h.med.Send(ctx, &shipNav.NavigateRouteCommand{
		ShipSymbol:  shipSymbol,
		Destination: h.standbyStations[0],
		PlayerID:    pid,
	}); err != nil {
		logger.Log("WARN", fmt.Sprintf("Contract scaler cross-gate reposition of %s from %s to home system %s failed (best-effort; between-legs homing will retry): %v", shipSymbol, currentSystem, homeSystem, err), map[string]interface{}{
			"action": "contract_scaler_cross_gate_home_failed", "ship_symbol": shipSymbol, "from_system": currentSystem, "home_system": homeSystem,
		})
	}
}

// contractFleetPeers lists the exclusive "contract"-dedicated hull symbols — the homing peers whose
// positions seed the demand-ranked spread occupancy. A read failure yields nil (homing degrades to
// nearest-station), never an error (best-effort positioning must not block a buy/reclaim).
func contractFleetPeers(ctx context.Context, shipRepo navigation.ShipRepository, pid shared.PlayerID) []string {
	if shipRepo == nil {
		return nil
	}
	ships, err := shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return nil
	}
	var peers []string
	for _, ship := range ships {
		if ship.DedicatedFleet() == contractFleetTag {
			peers = append(peers, ship.ShipSymbol())
		}
	}
	return peers
}
