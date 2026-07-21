package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractCmd "github.com/andrescamacho/spacetraders-go/internal/application/contract/commands"
	contractScalerCmd "github.com/andrescamacho/spacetraders-go/internal/application/contractscaler/commands"
	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contractscaler"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// This file wires the dedicated contract auto-scaler's application ports to the concrete daemon
// collaborators — the twin of fleet_autosizer_ports.go. The coordinator brain (role lookup, fixed
// plan, ramp buy-loop, 200000 cushion) depends only on narrow interfaces (RoleResolver /
// TreasuryReader / PriceReader / FleetCounter / Purchaser / the live ceiling), tested against fakes;
// these are the thin bridges the daemon injects at boot. The NOVEL port is the RoleResolver: the
// once-at-arm "lookup, not a solve" that reads THIS era's home-system geometry + market roles and
// hands the coordinator its central parks / far sources / far sink + per-park demand. Every other port
// REUSES an autosizer idiom (treasury / yard-price / buy-and-dedicate / countShips).

// --- RoleResolver: the once-at-arm home-system role lookup (the novel port) ---

// contractScalerHomeReader resolves the player's home system — the home-system-only sourcing scope
// (RULINGS #14) the role lookup runs within. readable=false (no resolvable home, e.g. a cold
// pre-registration boot) makes the scaler no-op (an empty era) rather than guess.
type contractScalerHomeReader interface {
	HomeSystem(ctx context.Context, playerID int) (string, bool, error)
}

// contractScalerWaypointLister lists a system's waypoints with their coordinates — the geometry half of
// the lookup. Satisfied by *persistence.GormWaypointRepository.
type contractScalerWaypointLister interface {
	ListBySystem(ctx context.Context, systemSymbol string) ([]*shared.Waypoint, error)
}

// contractScalerMarketReader reads a waypoint's market roles — the trade-role half of the lookup.
// Satisfied by market.MarketRepository. A nil market (no scanned data) is geometry-only: the waypoint
// is neither importer nor exporter, so it fills no role this era.
type contractScalerMarketReader interface {
	GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error)
}

// contractScalerRoleResolver implements the coordinator's RoleResolver port. It reads the home system
// once, lists its waypoints (geometry) + their markets (trade roles), and hands the pure domain lookup
// (contractscaler.ResolveRoles) a []WaypointMarket, returning the resolved roles plus a per-park demand
// weight map. It computes NO plan and holds NO state — the coordinator memoizes the result at arm.
type contractScalerRoleResolver struct {
	home      contractScalerHomeReader
	waypoints contractScalerWaypointLister
	markets   contractScalerMarketReader
}

// ResolveRoles is the once-at-arm lookup. It returns empty roles + empty demand (no error) when the
// home system is unresolvable so the coordinator's armed plan is empty and it never spends against an
// unknown topology (fail-safe, not fail-error — an empty era is a valid state, not a failure).
func (r *contractScalerRoleResolver) ResolveRoles(ctx context.Context, playerID int) (contractscaler.EraRoles, map[string]float64, error) {
	system, readable, err := r.home.HomeSystem(ctx, playerID)
	if err != nil {
		return contractscaler.EraRoles{}, nil, err
	}
	if !readable || system == "" {
		return contractscaler.EraRoles{}, map[string]float64{}, nil
	}
	if r.waypoints == nil {
		return contractscaler.EraRoles{}, map[string]float64{}, nil // no waypoint surface wired → empty era, no spend
	}

	waypoints, err := r.waypoints.ListBySystem(ctx, system)
	if err != nil {
		return contractscaler.EraRoles{}, nil, err
	}

	markets := make([]contractscaler.WaypointMarket, 0, len(waypoints))
	demand := make(map[string]float64, len(waypoints))
	for _, waypoint := range waypoints {
		if waypoint == nil {
			continue
		}
		exports, imports, weight := r.tradeRoles(ctx, waypoint.Symbol, playerID)
		markets = append(markets, contractscaler.WaypointMarket{
			Symbol:  waypoint.Symbol,
			X:       waypoint.X,
			Y:       waypoint.Y,
			Exports: exports,
			Imports: imports,
		})
		if len(imports) > 0 {
			demand[waypoint.Symbol] = weight
		}
	}
	return contractscaler.ResolveRoles(markets), demand, nil
}

// tradeRoles reads one waypoint's market and splits its goods into EXPORT / IMPORT symbol lists
// (EXCHANGE goods — neither produced nor consumed — are omitted, matching WaypointMarket's contract).
// It also returns the per-waypoint demand weight: the SUM of import trade VOLUMES (open Q a: prefer
// volume over count as the freq×draw proxy), falling back to the import COUNT when no volume signal is
// present (a zero-volume market still ranks, never sinking to 0 and dropping out of the spread). A nil
// market (no scanned data) contributes geometry only: no roles, no demand.
func (r *contractScalerRoleResolver) tradeRoles(ctx context.Context, waypointSymbol string, playerID int) (exports, imports []string, weight float64) {
	data, err := r.markets.GetMarketData(ctx, waypointSymbol, playerID)
	if err != nil || data == nil {
		return nil, nil, 0
	}
	importVolume := 0
	for _, good := range data.TradeGoods() {
		switch good.TradeType() {
		case market.TradeTypeExport:
			exports = append(exports, good.Symbol())
		case market.TradeTypeImport:
			imports = append(imports, good.Symbol())
			importVolume += good.TradeVolume()
		}
	}
	if importVolume > 0 {
		return exports, imports, float64(importVolume)
	}
	return exports, imports, float64(len(imports)) // count fallback when volume is unavailable
}

// --- concrete home-system reader (over the ship repo, mirroring the bootstrap observer) ---

// contractScalerShipHomeReader resolves the home system from the player's hull locations: the command
// frigate's system anchors it (the retire target / home anchor), falling back to the lexicographically
// smallest ship system for determinism when no command frigate is present. Mirrors the bootstrap
// observer's commandHome/anyHome idiom.
type contractScalerShipHomeReader struct{ shipRepo navigation.ShipRepository }

func (r *contractScalerShipHomeReader) HomeSystem(ctx context.Context, playerID int) (string, bool, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return "", false, nil
	}
	ships, err := r.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return "", false, err
	}
	commandHome, anyHome := "", ""
	for _, ship := range ships {
		location := ship.CurrentLocation()
		if location == nil {
			continue
		}
		system := shared.ExtractSystemSymbol(location.Symbol)
		if system == "" {
			continue
		}
		if anyHome == "" || system < anyHome {
			anyHome = system
		}
		if ship.Role() == commandRole {
			commandHome = system
		}
	}
	if commandHome != "" {
		return commandHome, true, nil
	}
	if anyHome != "" {
		return anyHome, true, nil
	}
	return "", false, nil
}

// --- handler assembly: wire the coordinator's ports to the daemon collaborators ---

// NewContractScalerCoordinatorHandler assembles the standing contract auto-scaler handler, wiring every
// coordinator port to a concrete daemon collaborator: the NOVEL RoleResolver (home-system geometry +
// market roles), the treasury/yard-price REUSE of the autosizer idioms, the exclusive-"contract"-fleet
// counter, the buy+dedicate+home Purchaser (the kept autosizer buy primitive + the demand-ranked homing
// consumer), and the live-tunable ceiling (the Pattern-C ContainerConfigReader). Registering it changes
// NO live behaviour — nothing launches the coordinator until the bootstrap early-scaling arm fires
// (default-off), so a bare deploy is byte-identical.
func NewContractScalerCoordinatorHandler(
	server *DaemonServer,
	apiClient *api.SpaceTradersClient,
	shipRepo navigation.ShipRepository,
	med common.Mediator,
	waypointRepo *persistence.GormWaypointRepository,
	marketRepo market.MarketRepository,
	scannedYards scannedYardRanker,
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
	h.SetTreasuryReader(&autosizerTreasuryReader{api: apiClient})

	// Yard price: the autosizer's cheapest-known-yard walk, adapted to the scaler's narrower
	// NextHullPrice port. The concrete waypoint repo is assigned only when non-nil (the same typed-nil
	// guard the autosizer applies).
	yardPriceReader := &autosizerYardPriceReader{med: med, shipRepo: shipRepo, scannedYards: scannedYards}
	if waypointRepo != nil {
		yardPriceReader.waypointRepo = waypointRepo
	}
	h.SetPriceReader(&contractScalerPriceReader{reader: yardPriceReader})

	// Current fleet = the exclusive "contract"-dedicated hulls (the ramp's Current).
	h.SetFleetCounter(&contractScalerFleetCounter{shipRepo: shipRepo})

	// Purchaser: the kept autosizer buy+dedicate primitive (dedicates to "contract" via the
	// contract-delivery HullClass mapping) composed with the demand-ranked homing dispatch.
	h.SetPurchaser(&contractScalerPurchaser{
		buyer:    &autosizerPurchaser{med: med, shipRepo: shipRepo},
		med:      med,
		shipRepo: shipRepo,
	})

	// Ceiling: the live Pattern-C snapshot of the container's own config column (contract_fleet_max_hulls),
	// so a `tune --operation contractscaler` lands on the next tick with no restart.
	h.SetCeilingReader(NewContainerConfigReader(server.containerRepo))
	return h
}

// --- price reader: adapt the autosizer yard-price walk to the scaler's NextHullPrice port ---

// contractScalerPriceReader wraps the autosizer's YardPriceReader, asking it for the contract-delivery
// class's cheapest priced yard and projecting the (price, yard, readable) triple the scaler needs.
// readable=false ⇒ the ramp holds (fail-closed — no price, no cushion check, no buy).
type contractScalerPriceReader struct{ reader fleetCmd.YardPriceReader }

func (p *contractScalerPriceReader) NextHullPrice(ctx context.Context, playerID int, shipType string) (int64, string, bool, error) {
	price, _, yard, readable, err := p.reader.PriceFor(ctx, playerID, fleetCmd.HullClassContractDelivery, shipType, false)
	return price, yard, readable, err
}

// --- fleet counter: the exclusive "contract"-dedicated pool (the ramp's Current) ---

type contractScalerFleetCounter struct{ shipRepo navigation.ShipRepository }

func (c *contractScalerFleetCounter) ContractHullCount(ctx context.Context, playerID int) (int, error) {
	return countShips(ctx, c.shipRepo, playerID, func(sh *navigation.Ship) bool {
		return sh.DedicatedFleet() == contractFleetTag
	})
}

// --- purchaser: buy+dedicate (kept autosizer primitive) then home demand-ranked ---

// contractScalerBuyer is the narrow buy+dedicate primitive the purchaser composes — the kept
// autosizerPurchaser.BuyAndDedicate (the money-integrity batch path + dedicate-at-purchase).
type contractScalerBuyer interface {
	BuyAndDedicate(ctx context.Context, order fleetCmd.BuyOrder) (fleetCmd.BuyResult, error)
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
	res, err := p.buyer.BuyAndDedicate(ctx, fleetCmd.BuyOrder{
		PlayerID:      order.PlayerID,
		Class:         fleetCmd.HullClassContractDelivery, // → dedicates to the exclusive "contract" fleet
		ShipType:      order.Unit.ShipType,
		Yard:          order.Yard,
		ExpectedPrice: order.ExpectedPrice,
	})
	if err != nil {
		return contractScalerCmd.BuyResult{}, err
	}
	p.home(ctx, res.ShipSymbol, order)
	return contractScalerCmd.BuyResult{ShipSymbol: res.ShipSymbol, Price: res.Price}, nil
}

// home dispatches the demand-ranked homing for the freshly-bought hull, carrying the spread standby set
// + the per-park demand weights the HomeShipCommand consumes. Best-effort: it logs and swallows a
// homing error so a completed purchase is never rolled back. FleetShips (the contract-fleet peers whose
// positions determine the spread occupancy) is populated best-effort from the ship repo — an empty list
// degrades to plain nearest-station homing, never an error.
func (p *contractScalerPurchaser) home(ctx context.Context, shipSymbol string, order contractScalerCmd.BuyOrder) {
	if len(order.StandbyStations) == 0 {
		return // no spread set → nothing to home to (homing is opt-in on the standby set)
	}
	logger := common.LoggerFromContext(ctx)
	pid, err := shared.NewPlayerID(order.PlayerID)
	if err != nil {
		return
	}
	if _, err := p.med.Send(ctx, &contractCmd.HomeShipCommand{
		ShipSymbol:      shipSymbol,
		PlayerID:        pid,
		StandbyStations: order.StandbyStations,
		StandbyDemand:   order.StandbyDemand,
		FleetShips:      p.contractFleetPeers(ctx, pid),
	}); err != nil {
		logger.Log("WARN", fmt.Sprintf("Contract scaler bought %s but homing failed (best-effort; between-legs homing will retry): %v", shipSymbol, err), map[string]interface{}{
			"action": "contract_scaler_home_failed", "ship_symbol": shipSymbol,
		})
	}
}

// contractFleetPeers lists the exclusive "contract"-dedicated hull symbols — the homing peers whose
// positions seed the demand-ranked spread occupancy. A read failure yields nil (homing degrades to
// nearest-station), never an error (best-effort positioning must not block a buy).
func (p *contractScalerPurchaser) contractFleetPeers(ctx context.Context, pid shared.PlayerID) []string {
	if p.shipRepo == nil {
		return nil
	}
	ships, err := p.shipRepo.FindAllByPlayer(ctx, pid)
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
