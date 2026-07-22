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
	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
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

// contractScalerShipHomeReader resolves the home system from the player's hull locations by ANCHOR
// PRIORITY (sp-cfvgj): (1) the contract fleet's own FOOTPRINT — the base where the "contract"-dedicated
// hulls sit — wins whenever any contract hull exists (degree 1+); (2) the command frigate's system
// anchors ONLY when no contract hull exists yet (degree-0 cold-start, where the frigate IS the sole
// contract hauler); (3) the lexicographically smallest ship system is the final determinism fallback.
// Priority 1 exists because post-degree-0 the command frigate is RETIRED from contracts and becomes the
// reserved PURCHASE ship that WANDERS to shipyards — anchoring on it flips home to the wrong system. The
// footprint is the MODAL contract system (the base where MOST contract hulls sit), so a single hull
// transiting away on a delivery never flips home. readable=false (no resolvable hull location) makes the
// scaler no-op (an empty era) rather than guess.
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
	contractSystems := map[string]int{} // contract-fleet footprint: system → count of "contract" hulls there
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
		if ship.DedicatedFleet() == contractFleetTag {
			contractSystems[system]++
		}
	}
	// Priority 1: the contract fleet's own footprint (degree 1+) — the base most contract hulls sit in.
	if footprint := mostCommonSystem(contractSystems); footprint != "" {
		return footprint, true, nil
	}
	// Priority 2: the command frigate (degree-0 cold-start — it is the sole contract hauler).
	if commandHome != "" {
		return commandHome, true, nil
	}
	// Priority 3: any-hull lexicographically-smallest (final determinism fallback).
	if anyHome != "" {
		return anyHome, true, nil
	}
	return "", false, nil
}

// mostCommonSystem returns the system with the highest count, ties broken lexicographically smallest, or
// "" when counts is empty. Choosing the MODAL system (not a first-seen or extremal one) is what makes the
// contract-footprint anchor robust to a single hull transiting away on a delivery: the base where the
// fleet actually sits wins over a transient far outlier. Deterministic regardless of map-iteration order —
// a strictly greater count always replaces, and among equal counts the smaller system always replaces.
func mostCommonSystem(counts map[string]int) string {
	best, bestCount := "", 0
	for system, count := range counts {
		if count > bestCount || (count == bestCount && system < best) {
			best, bestCount = system, count
		}
	}
	return best
}

// --- handler assembly: wire the coordinator's ports to the daemon collaborators ---

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

	// Reclaimer: the ZERO-SPEND reuse tier tried before every buy (RULINGS #7 — reclaim only an idle
	// UNDEDICATED cargo-capable hull, never poach). Reuses the SAME ship repo the FleetCounter reads +
	// the SAME mediator the purchaser homes through — no new daemon dependency. The SAME instance also
	// serves the depot STOCKER's home-scoped reuse tier (sp-fihvy, RULINGS #14): FindReclaimableForHome
	// additionally requires the candidate be in, or gate-reachable to, the depot's home system via
	// gateGraph — the identical Routable notion the daemon's stocker-viability guard consults.
	reclaimer := &contractScalerReclaimer{shipRepo: shipRepo, med: med, gateGraph: gateGraph}
	h.SetIdleHullReclaimer(reclaimer)
	h.SetDepotHullReclaimer(reclaimer)

	// Depot-hull buy price (sp-fihvy, RULINGS #14): the STOCKER role's buy-fallback yard search is
	// home-system-ONLY — a bought stocker hull is crewed at its purchase yard with no repositioning
	// step of its own, unlike a delivery buy (cross-gated home afterward) or the warehouse role
	// (unchanged, out of this fix's scope, still the shared fleet-wide-cheapest PriceReader below).
	// Wraps the SAME yardPriceReader constructed above — no new yard-price mechanism.
	h.SetDepotPriceReader(&contractScalerDepotPriceReader{yards: yardPriceReader})

	// Ceiling: the live Pattern-C snapshot of the container's own config column (contract_fleet_max_hulls),
	// so a `tune --operation contractscaler` lands on the next tick with no restart.
	h.SetCeilingReader(NewContainerConfigReader(server.containerRepo))

	// Depot actuation (sp-urpxy): arm the warehouse/stocker half of the fixed plan. The counter reads the
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

// --- price reader: adapt the autosizer yard-price walk to the scaler's NextHullPrice port ---

// contractScalerPriceReader wraps the autosizer's YardPriceReader, asking it for the contract-delivery
// class's cheapest priced yard and projecting the (price, yard, readable) triple the scaler needs.
// readable=false ⇒ the ramp holds (fail-closed — no price, no cushion check, no buy).
type contractScalerPriceReader struct{ reader fleetCmd.YardPriceReader }

func (p *contractScalerPriceReader) NextHullPrice(ctx context.Context, playerID int, shipType string) (int64, string, bool, error) {
	price, _, yard, readable, err := p.reader.PriceFor(ctx, playerID, fleetCmd.HullClassContractDelivery, shipType, false)
	return price, yard, readable, err
}

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

// --- fleet counter: the exclusive "contract"-dedicated pool (the ramp's Current) ---

type contractScalerFleetCounter struct{ shipRepo navigation.ShipRepository }

func (c *contractScalerFleetCounter) ContractHullCount(ctx context.Context, playerID int) (int, error) {
	return countShips(ctx, c.shipRepo, playerID, func(sh *navigation.Ship) bool {
		return sh.DedicatedFleet() == contractFleetTag
	})
}

// --- depot-aware per-role counter: the depot registry is the source of truth for actuated
// warehouse/stocker units (sp-urpxy) ---

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

// --- depot growth port: AddElement/AddDepot + the depot launch verbs grow the EXISTING depot
// (sp-urpxy) ---

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
	homeContractHull(ctx, p.med, p.shipRepo, res.ShipSymbol, order.PlayerID, order.StandbyStations, order.StandbyDemand)
	return contractScalerCmd.BuyResult{ShipSymbol: res.ShipSymbol, Price: res.Price}, nil
}

// BuyHull buys ONE UNDEDICATED light hull for a DEPOT role (warehouse/stocker) — the kept buy
// primitive driven with HullClassLight, whose dedicate-at-purchase tag is EMPTY
// (autosizerDedicatedFleet(light)=""). It is deliberately NOT contract-dedicated and NOT homed: the
// grower's launch (positionDepotElementHull) re-dedicates the idle hull to its "warehouse"/"stocker"
// fleet, so leaving it undedicated is what lets that adoption succeed (a "contract"-tagged hull would
// be refused by the depot never-poach guard). This mirrors the reclaim path, which likewise hands the
// grower an undedicated idle hull; the buy is the fallback when no reclaimable hull is free.
func (p *contractScalerPurchaser) BuyHull(ctx context.Context, order contractScalerCmd.BuyOrder) (contractScalerCmd.BuyResult, error) {
	res, err := p.buyer.BuyAndDedicate(ctx, fleetCmd.BuyOrder{
		PlayerID:      order.PlayerID,
		Class:         fleetCmd.HullClassLight, // undedicated (no tag) → the grower re-dedicates to the role fleet
		ShipType:      order.Unit.ShipType,
		Yard:          order.Yard,
		ExpectedPrice: order.ExpectedPrice,
	})
	if err != nil {
		return contractScalerCmd.BuyResult{}, err
	}
	return contractScalerCmd.BuyResult{ShipSymbol: res.ShipSymbol, Price: res.Price}, nil
}

// homeContractHull dispatches the demand-ranked C1 homing for a hull that JUST JOINED the contract
// fleet — bought OR reclaimed — carrying the spread standby set + the per-park demand weights the
// HomeShipCommand consumes. SHARED so a reclaimed hull homes byte-for-byte like a bought one. Best-
// effort: it logs and swallows a homing error so a completed buy/reclaim is never rolled back.
// FleetShips (the contract-fleet peers whose positions seed the spread occupancy) is populated best-
// effort from the ship repo — an empty list degrades to plain nearest-station homing, never an error.
func homeContractHull(ctx context.Context, med commandSender, shipRepo navigation.ShipRepository, shipSymbol string, playerID int, standbyStations []string, standbyDemand map[string]float64) {
	if len(standbyStations) == 0 {
		return // no spread set → nothing to home to (homing is opt-in on the standby set)
	}
	logger := common.LoggerFromContext(ctx)
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return
	}
	// sp-orooy: a hull reclaimed/bought while idle in a FOREIGN system cannot be homed by the
	// intra-system spread-home below — HomeShipHandler resolves the standby parks in the hull's
	// CURRENT system graph, so a foreign hull fails ("none of the configured standby stations found
	// in system X graph") and STRANDS (dedicated to "contract" but idle in the wrong system, never
	// re-picked). Cross-gate it back to a home-system park FIRST; a hull already in the home system
	// skips this entirely (byte-identical). SCALER-ONLY: homeContractHull is called only by the
	// scaler's buy/reclaim home-return — idle-arb's deliberately leashed hub-local re-home never runs here.
	repositionForeignHullHome(ctx, med, shipRepo, shipSymbol, pid, standbyStations, logger)

	if _, err := med.Send(ctx, &contractCmd.HomeShipCommand{
		ShipSymbol:      shipSymbol,
		PlayerID:        pid,
		StandbyStations: standbyStations,
		StandbyDemand:   standbyDemand,
		FleetShips:      contractFleetPeers(ctx, shipRepo, pid),
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
// pre-fix homing) or when its location is unreadable (defer to the intra-system home + between-legs self-
// heal). Best-effort: a reposition hiccup only logs — the completed reclaim/buy is never rolled back, and
// the contract coordinator's between-legs homing still retries. The move REUSES NavigateRouteCommand's
// wired cross-system delegation (sp-9l4p → RepositionToWaypoint → the SAME multi-jump travel() the
// scout/arb/trade circuits ride), never hand-rolled pathfinding.
func repositionForeignHullHome(ctx context.Context, med commandSender, shipRepo navigation.ShipRepository, shipSymbol string, pid shared.PlayerID, standbyStations []string, logger common.ContainerLogger) {
	if shipRepo == nil {
		return
	}
	homeSystem := shared.ExtractSystemSymbol(standbyStations[0])
	ship, err := shipRepo.FindBySymbol(ctx, shipSymbol, pid)
	if err != nil || ship == nil || ship.CurrentLocation() == nil {
		return // unreadable location → let the intra-system home try (and self-heal via between-legs homing)
	}
	currentSystem := shared.ExtractSystemSymbol(ship.CurrentLocation().Symbol)
	if currentSystem == "" || currentSystem == homeSystem {
		return // already home (or unknown) → the intra-system spread-home handles it (byte-identical)
	}
	if _, err := med.Send(ctx, &shipNav.NavigateRouteCommand{
		ShipSymbol:  shipSymbol,
		Destination: standbyStations[0],
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

// --- reclaimer: the ZERO-SPEND reuse tier tried before every buy (RULINGS #7 — never poach) ---

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

// FindReclaimableForHome is the depot STOCKER's home-scoped reuse tier (sp-fihvy, RULINGS #14): the
// FIRST reuse-eligible hull (the SAME isReclaimable guard — idle, not in transit, undedicated,
// cargo-capable) that is ALSO in, or gate-reachable to, homeSystem — the exact reachability notion
// depotStockerHullViable / foreignMarketReachable use (gateGraph.Routable), never a second one
// invented here. Skipping a foreign-but-otherwise-idle candidate here is what stops GrowStocker from
// ever being handed a hull it cannot place viably; the ramp falls through to the equally
// home-scoped buy tier instead. A read error fails closed (falls through to a buy) exactly like
// FindReclaimable.
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

// isReclaimable is the reuse-eligibility guard: IDLE (never mid-task → no stranding), NOT in transit,
// UNDEDICATED (RULINGS #7 — never poach a dedicated hull of ANY fleet), and CARGO-CAPABLE (a probe /
// 0-cargo hull re-dedicated to contract can't haul — mirrors the hauling-pin cargo guard).
func isReclaimable(ship *navigation.Ship) bool {
	return ship.IsIdle() && !ship.IsInTransit() && ship.DedicatedFleet() == "" && ship.CargoCapacity() > 0
}

// Reclaim re-dedicates the hull EXCLUSIVE to the contract fleet via the single write path (AssignFleet,
// RULINGS #3), then homes it demand-ranked like a bought hull (best-effort C1 homing). It returns an
// error ONLY when the re-dedicate itself fails — the hull is then NOT taken, so the coordinator safely
// buys without double-counting. NO SPEND: reclaim is free and is never gated by the buy cushion.
func (r *contractScalerReclaimer) Reclaim(ctx context.Context, order contractScalerCmd.ReclaimOrder) error {
	pid, err := shared.NewPlayerID(order.PlayerID)
	if err != nil {
		return err
	}
	if err := r.shipRepo.AssignFleet(ctx, order.ShipSymbol, contractFleetTag, pid); err != nil {
		return fmt.Errorf("reclaim re-dedicate %s → contract: %w", order.ShipSymbol, err)
	}
	homeContractHull(ctx, r.med, r.shipRepo, order.ShipSymbol, order.PlayerID, order.StandbyStations, order.StandbyDemand)
	return nil
}
