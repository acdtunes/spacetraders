package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipyardCmd "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/application/system/gategraph"
	tradingQueries "github.com/andrescamacho/spacetraders-go/internal/application/trading/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/apibudget"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	shipyardDomain "github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

// This file wires the fleet capacity autosizer's application ports (sp-1txd M6) to the concrete
// daemon collaborators — the buy-side twin of siting_ports.go. The M1–M5 coordinator logic depends
// only on narrow interfaces (fleetCmd.TreasuryReader / EraClockReader / YardPriceReader / Purchaser
// / the demand sources, etc.), tested against fakes; these are the thin bridges the daemon injects
// at boot. No business logic lives here — every method forwards to an existing client/repo.
//
// LIGHTS and HEAVIES are both FULLY LIVE. Lights: real worker count (HAULER hulls), running-chain
// count, chain-P&L realized worker rate. Heavies: the unserved-lane count reads the
// profitable-lane surface off the persisted market cache (the read-only ProfitableLaneReader — the
// same pure trading.RankSpreads ranking the trade circuit flies, no coordinator perturbation), and
// the realized tour-rate reads persisted tour telemetry (trading.ComputeFleetTourRate). Both fail
// CLOSED on a genuine read failure (RULINGS #4 — an unreadable signal never spends); the seam only
// makes readable demand READABLE, it relaxes no guard. Shared: treasury / era-clock / yard-price /
// fleet-size reads and the buy+dedicate path. API utilization is LIVE too: it reads the
// rolling-5m window of the budget tracker (the daemon-startup singleton), so its guard fails
// CLOSED rather than open — holding concurrency growth on real saturation or an absent surface.
// Vacancies are 0 for now (the rebalancer hub-vacancy query is a later enrichment;
// a 0 leaves the chain-derived base demand intact).

// agentReader is the narrow slice of *api.SpaceTradersClient the money guards need (treasury).
// Declared here so the ports depend on behaviour, not the whole client.
type agentReader interface {
	GetAgent(ctx context.Context, token string) (*player.AgentData, error)
}

// NewFleetAutosizerCoordinatorHandler assembles the autosizer handler (sp-1txd M6), wiring every
// concrete port to the daemon's live collaborators and registering the light + heavy demand
// providers (and the opt-in explorer class).
func NewFleetAutosizerCoordinatorHandler(
	server *DaemonServer,
	apiClient *api.SpaceTradersClient,
	ledgerTreasury *persistence.LedgerTreasury,
	shipRepo navigation.ShipRepository,
	med common.Mediator,
	waypointRepo *persistence.GormWaypointRepository,
	eventStore captain.EventStore,
	marketRepo market.MarketRepository,
	scannedYards scannedYardRanker,
	offGateDemand fleetCmd.OffGateDemandSource,
	heavyYards heavyYardInventory,
) *fleetCmd.RunFleetAutosizerCoordinatorHandler {
	h := fleetCmd.NewRunFleetAutosizerCoordinatorHandler(nil)

	// Demand providers.
	h.AddDemandProvider(fleetCmd.NewLightDemandProvider(&autosizerLightSources{
		shipRepo: shipRepo, server: server,
	}))
	// HEAVIES ARE NOW LIVE: the unserved-lane signal reads the profitable-lane surface
	// off the persisted market cache (tradingQueries.ProfitableLaneReader, read-only — the same pure
	// trading.RankSpreads ranking the trade circuit uses, no coordinator perturbation), and the
	// realized tour-rate reads persisted tour telemetry. Both fail closed on a genuine read failure,
	// so the guard stack still gates every heavy buy; the seam only makes the demand READABLE.
	h.AddDemandProvider(fleetCmd.NewHeavyDemandProvider(&autosizerHeavySources{
		shipRepo:   shipRepo,
		laneReader: tradingQueries.NewProfitableLaneReader(marketRepo),
	}))

	// Explorer class (slice C): reads slice-B off-gate demand through the cross-coordinator
	// bridge (offGateDemand) and the live explorer-pool count (dedicate-at-purchase "explorer" fleet).
	// DORMANT until BOTH armed (explorer_hulls_enabled, default off — classDisabled skips it otherwise)
	// AND the frontier raises off-gate demand into the bridge, so registering it here changes no live
	// behaviour and nothing auto-buys. The frontier warps the bought hull (SetExplorerDispatchPort).
	h.AddDemandProvider(fleetCmd.NewExplorerDemandProvider(offGateDemand, &autosizerExplorerFleetSource{shipRepo: shipRepo}))

	// Buy-path readers + writers.
	h.SetTreasuryReader(&autosizerTreasuryReader{api: apiClient, ledger: ledgerTreasury})
	h.SetAPIUtilizationReader(&autosizerAPIUtilReader{reporter: metrics.GetGlobalAPIBudgetTracker()})
	// The concrete waypoint repo is assigned only when non-nil: a typed-nil
	// pointer inside the interface field would defeat the reader's nil guard
	// (fail-closed on an unwired waypoint surface) with a runtime panic instead.
	yardPriceReader := &autosizerYardPriceReader{med: med, shipRepo: shipRepo, scannedYards: scannedYards}
	if waypointRepo != nil {
		yardPriceReader.waypointRepo = waypointRepo
	}
	h.SetYardPriceReader(yardPriceReader)

	// The heavy-hull census and the cheapest-heavy-price read: together they derive the heavy
	// reservation each tick and feed the heavy_cap guard. BOTH fail closed when unwired, so an
	// unwired census STOPS heavy buying — hence the loud WARN below rather than a silent skip.
	if counter, ok := shipRepo.(heavyHullCounter); ok {
		h.SetHeavyCensusReader(&autosizerHeavyCensus{counter: counter})
	} else {
		log.Printf("WARNING: fleet autosizer heavy census UNWIRED — the ship repository does not implement CountHeavyHulls, so the heavy_cap guard fails closed and NO heavy will be bought")
	}
	if heavyYards != nil {
		h.SetHeavyYardReader(&autosizerHeavyYardReader{yards: heavyYards})
	} else {
		log.Printf("WARNING: fleet autosizer heavy-yard reader UNWIRED — the heavy reservation will always be 0, so expansion spending is never held back for a heavy")
	}
	// The heavy-yard PRICING ERRAND. A shipyard prices its hulls only while a ship stands there,
	// so a heavy yard discovered without presence carries an availability-only row and can never
	// feed the reservation. Unwired ⇒ no errand and no reservation ever forms from such a yard,
	// which is exactly the state this warns about.
	if scannedYards != nil {
		if ranker, ok := scannedYards.(heavyYardRanker); ok {
			h.SetHeavyYardCatalogReader(&autosizerHeavyYardCatalog{ranker: ranker, shipRepo: shipRepo})
			h.SetHeavyPricingErrandPort(&autosizerPricingErrand{med: med, shipRepo: shipRepo})
		} else {
			log.Printf("WARNING: fleet autosizer heavy-yard pricing errand UNWIRED — the yard ranker cannot list availability-only rows, so a known-but-unpriced heavy yard is never priced and no heavy reservation can form")
		}
	}
	// heavy_cap is the autosizer's ONE live-tunable knob (Pattern-C hot reload).
	h.SetHeavyCapReader(NewContainerConfigReader(server.containerRepo))
	h.SetPurchaser(&autosizerPurchaser{med: med, shipRepo: shipRepo})
	h.SetPurchaseNotifier(&autosizerNotifier{store: eventStore})
	// The metrics sink is wrapped in the blocked-guard tap: it forwards every recording
	// unchanged AND lets the reconcile loop name the FIRST FAILING GUARD in the tick's stall
	// verdict. The guard is read off the seam sizeClass already publishes it on, so nothing is
	// re-evaluated and no port is read twice.
	h.SetMetricsSink(fleetCmd.NewBlockedGuardTap(&autosizerMetricsSink{}))
	// Stall escalation: a class BLOCKED on the SAME guard for health.StallEscalationTicks
	// consecutive ticks raises a coordinator.stalled captain event and a Prometheus escalation
	// counter rather than an INFO log line, which nothing watches. Write-only by type — the
	// streak it accumulates is unreadable by any sizing decision (RULINGS #2).
	h.SetStallObserver(health.NewStallEscalator(metrics.NewStallMetricsPort(), eventStore))
	// sp-y2ptq: the contract_delivery graduation gate was removed with the autosizer's contract class
	// (the dedicated scaler owns contract capacity + its own graduation handling).
	return h
}

// --- treasury ---

// autosizerTreasuryReader answers the autosizer's (and the contract scaler's) treasury
// cushion guard. It prefers the LEDGER (sp-muq66) — the same balance, with no API call —
// and only falls back to a live read when the ledger is too old to trust; the fallback
// lives inside the ledger reader, so this type states the guard's contract and nothing
// more. An unwired ledger keeps the direct live read, which is what the daemon did before
// and what every test that constructs this type bare still exercises.
//
// Unreadable is reported as readable=false, never as a zero balance: a guard that cannot
// read treasury must refuse to buy, not size a purchase against 0 (RULINGS #4).
type autosizerTreasuryReader struct {
	api    agentReader
	ledger *persistence.LedgerTreasury
}

func (r *autosizerTreasuryReader) Treasury(ctx context.Context, playerID int) (int64, bool, error) {
	if r.ledger != nil {
		credits, err := r.ledger.Credits(ctx, playerID)
		if err != nil {
			return 0, false, nil // unreadable → the treasury guards fail closed
		}
		return credits, true, nil
	}
	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return 0, false, nil // no token in ctx → unreadable → the treasury guards fail closed
	}
	agent, err := r.api.GetAgent(ctx, token)
	if err != nil || agent == nil {
		return 0, false, nil
	}
	return int64(agent.Credits), true, nil
}

// --- API utilization (sp-a5dq: live read off the sp-51ti budget tracker; fail CLOSED) ---

// apiBudgetReporter is the narrow read the API-util guard needs — the rolling utilization snapshot.
// Satisfied by *metrics.APIBudgetTracker (the daemon-startup singleton, fed one event per API attempt
// on the request path). Declared as an interface so the reader depends on behaviour, not the tracker.
type apiBudgetReporter interface {
	Report() apibudget.DualReport
}

// autosizerAPIUtilReader surfaces the fleet-wide API request-utilization percent to the autosizer's
// api_util guard. It reads the rolling-5m window of the shared budget tracker — the SAME
// throughput/ceiling basis as the Prometheus ApproachCeiling alert (sum(rate(api_requests_total[5m]))
// / RateLimitPerSecond) — so the guard gates concurrency GROWTH against genuine API saturation.
// Fails CLOSED (readable=false) when no live surface exists (nil
// tracker, or an unconfigured/zero ceiling): a guard that cannot read its bound never permits growth
// (RULINGS #4). In the daemon the tracker is wired unconditionally at startup, so the normal case is
// readable; blocking only occurs on real saturation or a genuinely-absent metrics subsystem.
type autosizerAPIUtilReader struct{ reporter apiBudgetReporter }

func (r *autosizerAPIUtilReader) UtilizationPct(ctx context.Context) (float64, bool, error) {
	if r == nil || r.reporter == nil {
		return 0, false, nil // no utilization surface wired → unreadable → guard fails CLOSED
	}
	rolling := r.reporter.Report().Rolling5m
	if rolling.CeilingReqPerSec <= 0 {
		// A typed-nil tracker's nil-safe Report() (or a tracker built with no ceiling) yields a
		// zero-value report; without a ceiling there is no meaningful utilization → fail CLOSED
		// rather than let a spurious readable 0% permit unbounded growth.
		return 0, false, nil
	}
	return rolling.UtilizationPct, true, nil
}

// --- yard price (cheapest known shipyard ask for the type across the player's systems) ---

// shipyardWaypointLister is the narrow waypoint-repo slice the yard-price walk
// needs (SHIPYARD-trait waypoints per system). Satisfied by
// *persistence.GormWaypointRepository; an interface so the reader is testable
// against fakes at the port boundary.
type shipyardWaypointLister interface {
	ListBySystemWithTrait(ctx context.Context, systemSymbol, trait string) ([]*shared.Waypoint, error)
}

// scannedYardRanker is the nearest-reachable-yard signal off the persisted
// shipyard-inventory scans, ranked hops-then-price. Satisfied by
// *shipyardQueries.ReachableYardFinder.
type scannedYardRanker interface {
	NearestYardsSelling(ctx context.Context, playerID int, shipTypes []string, fromSystems []string) ([]shipyardQueries.YardCandidate, error)
}

type autosizerYardPriceReader struct {
	med          common.Mediator
	shipRepo     navigation.ShipRepository
	waypointRepo shipyardWaypointLister
	// scannedYards is the heavy-yard fallback: when the live in-system
	// walk finds no priced listing (the branch that has ALWAYS failed closed),
	// the HEAVY class may open on a scout-scanned, gate-reachable yard. Nil-safe;
	// nil or an empty scan store keeps the historical fail-closed behavior.
	scannedYards scannedYardRanker
}

// PriceFor finds the cheapest priced listing for the ship type at a SHIPYARD-trait waypoint in a
// system where the player operates. When that live in-system walk finds nothing, the HEAVY class
// falls back to the scout-scanned shipyard inventory: the nearest gate-reachable scanned
// yard, ranked hops-then-price — the availability signal the fail-closed heavy branch was designed
// to consume. Returns readable=false (price guard fails closed) when neither surface knows a priced
// yard. The demand-proximal preference is a later refinement (banked) — cheapest is returned now.
func (r *autosizerYardPriceReader) PriceFor(ctx context.Context, playerID int, class fleetCmd.HullClass, shipType string, preferProximal bool) (int64, int64, string, bool, error) {
	if r.waypointRepo == nil {
		return 0, 0, "", false, nil
	}
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return 0, 0, "", false, nil
	}
	ships, err := r.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return 0, 0, "", false, nil
	}
	var cheapest int64
	var cheapestYard string
	for _, system := range distinctShipSystems(ships) {
		waypoints, werr := r.waypointRepo.ListBySystemWithTrait(ctx, system, "SHIPYARD")
		if werr != nil {
			continue
		}
		for _, wp := range waypoints {
			if wp == nil {
				continue
			}
			price, ok := r.priceAtShipyard(ctx, system, wp.Symbol, shipType, pid)
			if !ok {
				continue
			}
			if cheapestYard == "" || price < cheapest {
				cheapest, cheapestYard = price, wp.Symbol
			}
		}
	}
	if cheapestYard == "" {
		return r.scannedYardFallback(ctx, playerID, class, shipType, ships)
	}
	return cheapest, cheapest, cheapestYard, true, nil
}

// PriceForSystem finds the cheapest priced listing for the ship type at a SHIPYARD-trait waypoint
// scoped to a SINGLE named system (sp-fihvy: the depot stocker hull viability fix). Unlike PriceFor
// (which walks every system the player already operates in), the depot stocker buy-fallback must never
// price a hull outside the warehouse's home system — RULINGS #14 (the stocker is intra-system) — so this
// reader takes the home system as an explicit argument instead of discovering it from ship locations.
// No remote-yard/scanned fallback: a depot stocker hull is either bought at home or not bought at all.
func (r *autosizerYardPriceReader) PriceForSystem(ctx context.Context, playerID int, shipType, system string) (int64, string, bool, error) {
	if r.waypointRepo == nil {
		return 0, "", false, nil
	}
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return 0, "", false, nil
	}
	waypoints, werr := r.waypointRepo.ListBySystemWithTrait(ctx, system, "SHIPYARD")
	if werr != nil {
		return 0, "", false, nil
	}
	var cheapest int64
	var cheapestYard string
	for _, wp := range waypoints {
		if wp == nil {
			continue
		}
		price, ok := r.priceAtShipyard(ctx, system, wp.Symbol, shipType, pid)
		if !ok {
			continue
		}
		if cheapestYard == "" || price < cheapest {
			cheapest, cheapestYard = price, wp.Symbol
		}
	}
	if cheapestYard == "" {
		return 0, "", false, nil
	}
	return cheapest, cheapestYard, true, nil
}

// scannedYardFallback opens the HEAVY price signal from the persisted shipyard
// scans when the live in-system walk found no priced listing. Heavy
// ONLY: heavy hulls are the class whose yards are routinely out-of-system (the
// branch that has always failed closed for lack of this signal); lights keep
// buying in-system, so widening them to remote yards would be a buy-policy
// change this seam deliberately does not make. price = the NEAREST candidate's
// ask (hops first, then price — the finder's rank), cheapest = the true minimum
// across reachable candidates so the premium guard judges the proximity premium
// honestly. No candidates / no ranker wired / rank read failure ⇒ readable=false:
// exactly the historical fail-closed behavior.
func (r *autosizerYardPriceReader) scannedYardFallback(ctx context.Context, playerID int, class fleetCmd.HullClass, shipType string, ships []*navigation.Ship) (int64, int64, string, bool, error) {
	if class != fleetCmd.HullClassHeavy || r.scannedYards == nil {
		return 0, 0, "", false, nil
	}
	candidates, err := r.scannedYards.NearestYardsSelling(ctx, playerID, []string{shipType}, distinctShipSystems(ships))
	if err != nil || len(candidates) == 0 {
		return 0, 0, "", false, nil // unreadable or empty scan surface → the price guard stays closed
	}
	nearest := candidates[0]
	cheapest := int64(nearest.PurchasePrice)
	for _, c := range candidates[1:] {
		if int64(c.PurchasePrice) < cheapest {
			cheapest = int64(c.PurchasePrice)
		}
	}
	return int64(nearest.PurchasePrice), cheapest, nearest.WaypointSymbol, true, nil
}

func (r *autosizerYardPriceReader) priceAtShipyard(ctx context.Context, system, waypoint, shipType string, pid shared.PlayerID) (int64, bool) {
	// The autosizer's fail-closed price guard reads this before it authorises a
	// hull buy, so it is Earning: metered, never denied (RULINGS #4).
	q := &shipyardQueries.GetShipyardListingsQuery{SystemSymbol: system, WaypointSymbol: waypoint, PlayerID: pid, Class: marketscan.Earning}
	resp, err := r.med.Send(ctx, q)
	if err != nil {
		return 0, false
	}
	out, ok := resp.(*shipyardQueries.GetShipyardListingsResponse)
	if !ok || out == nil {
		return 0, false
	}
	if listing, found := out.Shipyard.FindListingByType(shipType); found {
		return int64(listing.PurchasePrice), true
	}
	return 0, false
}

// --- purchaser (buy 1 through the money-integrity batch path, then dedicate) ---

type autosizerPurchaser struct {
	med      common.Mediator
	shipRepo navigation.ShipRepository
}

// autosizerDedicatedFleet maps a hull class to its permanent dedicated-fleet tag. Lights get NO tag
// (a SHIP_LIGHT_HAULER is a HAULER worker the moment it is bought — being adopted by a factory chain
// is the intended outcome, not the absorption hazard); heavies (and explorer/contract hulls) MUST be
// tagged at purchase so no coordinator poaches them before they reach their role (the 3-of-5-absorbed lesson).
func autosizerDedicatedFleet(class fleetCmd.HullClass) string {
	switch class {
	case fleetCmd.HullClassHeavy:
		return "trade"
	case fleetCmd.HullClassExplorer:
		// Dedicate-at-purchase: tag the bought explorer to the "explorer" fleet in the same
		// breath so no coordinator poaches it before the frontier dispatch loop warps it off-gate.
		return "explorer"
	case fleetCmd.HullClassContractDelivery:
		// The dedicated contract auto-scaler drives this class through BuyAndDedicate, so a
		// contract-delivery hull is stamped EXCLUSIVE to the "contract" fleet at purchase — killing the
		// churn class the shared reuse pool created. Safe to tag: the class is opt-in default-off.
		return "contract"
	default:
		return ""
	}
}

func (p *autosizerPurchaser) BuyAndDedicate(ctx context.Context, order fleetCmd.BuyOrder) (fleetCmd.BuyResult, error) {
	pid, err := shared.NewPlayerID(order.PlayerID)
	if err != nil {
		return fleetCmd.BuyResult{}, err
	}
	// The purchase needs a hull to travel to and buy at the shipyard. sp-7r7w: PREFER the exclusive
	// purchasing ship (the pivoted command frigate) when idle — the deterministic, protected buy ship for
	// every scaling buy — and fall back to any idle hull if it is momentarily busy or not yet established.
	// The battle-tested batch path navigates it and enforces the money-integrity type guard.
	ships, err := p.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return fleetCmd.BuyResult{}, err
	}
	purchaser := ""
	for _, s := range ships {
		if s.IsIdle() && s.DedicatedFleet() == navigation.PurchasingFleet {
			purchaser = s.ShipSymbol()
			break
		}
	}
	if purchaser == "" {
		for _, s := range ships {
			if s.IsIdle() {
				purchaser = s.ShipSymbol()
				break
			}
		}
	}
	if purchaser == "" {
		return fleetCmd.BuyResult{}, fmt.Errorf("no idle hull available to execute the purchase")
	}

	resp, err := p.med.Send(ctx, &shipyardCmd.BatchPurchaseShipsCommand{
		PurchasingShipSymbol: purchaser,
		ShipType:             order.ShipType,
		Quantity:             1,
		MaxBudget:            0,
		PlayerID:             pid,
		ShipyardWaypoint:     order.Yard,
	})
	if err != nil {
		return fleetCmd.BuyResult{}, err
	}
	batch, ok := resp.(*shipyardCmd.BatchPurchaseShipsResponse)
	if !ok || batch.ShipsPurchasedCount == 0 || len(batch.PurchasedShips) == 0 {
		return fleetCmd.BuyResult{}, fmt.Errorf("purchase returned no ship")
	}
	bought := batch.PurchasedShips[0]

	// Dedicate-at-purchase: tag heavy/explorer/contract hulls to their fleet in the same breath so no
	// coordinator tick can adopt them first. Idempotent; lights get no tag (they ARE workers).
	dedicated := false
	if fleet := autosizerDedicatedFleet(order.Class); fleet != "" {
		if aerr := p.shipRepo.AssignFleet(ctx, bought.ShipSymbol(), fleet, pid); aerr != nil {
			return fleetCmd.BuyResult{}, fmt.Errorf("bought %s but failed to dedicate to %q: %w", bought.ShipSymbol(), fleet, aerr)
		}
		dedicated = true
	}
	return fleetCmd.BuyResult{ShipSymbol: bought.ShipSymbol(), Price: int64(batch.TotalCost), Dedicated: dedicated}, nil
}

// --- purchase notifier (captain event: a buy is real news) ---

type autosizerNotifier struct{ store captain.EventStore }

func (n *autosizerNotifier) NotifyPurchase(ctx context.Context, playerID int, class fleetCmd.HullClass, shipType string, price int64, note string) error {
	if n.store == nil {
		return nil
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"class": string(class), "ship_type": shipType, "price": price, "note": note,
	})
	return n.store.Record(ctx, &captain.Event{
		Type:     captain.EventFleetAutosizerPurchase,
		Ship:     string(class),
		PlayerID: playerID,
		Payload:  string(payload),
	})
}

// --- metrics sink (adapts the fleet MetricsSink to the global collector's Record funcs) ---

type autosizerMetricsSink struct{}

func (m *autosizerMetricsSink) RecordDemand(class fleetCmd.HullClass, demand, current int) {
	metrics.RecordAutosizerDemand(string(class), demand, current)
}
func (m *autosizerMetricsSink) RecordPurchase(class fleetCmd.HullClass) {
	metrics.RecordAutosizerPurchase(string(class))
}
func (m *autosizerMetricsSink) RecordBlocked(class fleetCmd.HullClass, guard fleetCmd.GuardName) {
	metrics.RecordAutosizerBlocked(string(class), string(guard))
}
func (m *autosizerMetricsSink) RecordZeroEffectAlarm() {
	metrics.RecordAutosizerZeroEffectAlarm()
}
func (m *autosizerMetricsSink) RecordHeavyReserve(playerID string, reserve int64, owned, cap int) {
	metrics.RecordAutosizerHeavyReserve(playerID, reserve, owned, cap)
}
func (m *autosizerMetricsSink) ObserveHeavyPricePremium(playerID string, paid, cheapestKnown int64) {
	metrics.ObserveAutosizerHeavyPricePremium(playerID, paid, cheapestKnown)
}
func (m *autosizerMetricsSink) RecordSizingEnabled(playerID string, enabled bool) {
	metrics.RecordAutosizerSizingEnabled(playerID, enabled)
}

// --- LIGHT demand sources ---

type autosizerLightSources struct {
	shipRepo navigation.ShipRepository
	server   *DaemonServer
}

const autosizerHaulerRole = "HAULER"

func (s *autosizerLightSources) WorkerCount(ctx context.Context, playerID int) (int, error) {
	return countShips(ctx, s.shipRepo, playerID, func(sh *navigation.Ship) bool { return sh.Role() == autosizerHaulerRole })
}

func (s *autosizerLightSources) DesiredChains(ctx context.Context, playerID int) (int, error) {
	// Running standing goods_factory chains (iterations=-1) — the same portfolio the siting
	// controller enumerates. When siting is worker-limited these are the chains that need workers.
	models, err := s.server.containerRepo.ListByStatus(ctx, container.ContainerStatusRunning, &playerID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range models {
		if m.ContainerType != "goods_factory_coordinator" {
			continue
		}
		var cfg map[string]interface{}
		if m.Config != "" {
			if json.Unmarshal([]byte(m.Config), &cfg) != nil {
				continue
			}
		}
		if iter, ok := cfg["max_iterations"].(float64); ok && iter == -1 {
			n++
		}
	}
	return n, nil
}

func (s *autosizerLightSources) Vacancies(ctx context.Context, playerID int) (int, error) {
	// The rebalancer hub-vacancy query is a later enrichment (banked). 0 leaves the chain-derived
	// base demand intact (vacancies are additive).
	return 0, nil
}

// --- HEAVY demand sources (sp-4ewi: the wired seam) ---

// profitableLaneCounter counts the profitable, feasible trade lanes ranked across the given systems,
// read-only, off the persisted market cache. Satisfied by tradingQueries.ProfitableLaneReader.
type profitableLaneCounter interface {
	CountProfitableLanes(ctx context.Context, playerID int, systems []string) (count int, readable bool, err error)
}

type autosizerHeavySources struct {
	shipRepo   navigation.ShipRepository
	laneReader profitableLaneCounter
}

func (s *autosizerHeavySources) HeavyCount(ctx context.Context, playerID int) (int, error) {
	return countShips(ctx, s.shipRepo, playerID, func(sh *navigation.Ship) bool { return sh.DedicatedFleet() == "trade" })
}

// UnservedLaneCount surfaces the trade solver's profitable-but-unflown lane count as the heavy
// capacity-short signal: the number of profitable, feasible lanes the player's trading
// grounds rank BEYOND the current trade-hull pool. It discovers those grounds from the player's hull
// locations (the yard-price reader's system-discovery idiom), asks the read-only lane reader how many
// profitable lanes they hold, and subtracts the current heavies. READ-ONLY: it never perturbs the
// trade coordinator (it consumes the same pure trading.RankSpreads ranking off the market cache).
// Fails CLOSED (readable=false) on a genuine ship/market read failure; a readable zero (empty cache,
// no floor-clearing lane) yields 0 unserved (no demand, no buy) — not a fail-closed.
func (s *autosizerHeavySources) UnservedLaneCount(ctx context.Context, playerID int) (int, bool, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return 0, false, nil // invalid player → unreadable → fail closed
	}
	ships, err := s.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return 0, false, err // a genuine ship read failure fails closed
	}
	profitable, readable, err := s.laneReader.CountProfitableLanes(ctx, playerID, distinctShipSystems(ships))
	if err != nil || !readable {
		return 0, false, err // unreadable lane surface → fail closed
	}
	heavies := 0
	for _, sh := range ships {
		if sh.DedicatedFleet() == "trade" {
			heavies++
		}
	}
	unserved := profitable - heavies
	if unserved < 0 {
		unserved = 0 // never a negative demand (the pool already covers the lanes)
	}
	return unserved, true, nil
}

// distinctShipSystems returns the distinct systems the player's hulls are located in — the trading
// grounds the unserved-lane count scans. Mirrors autosizerYardPriceReader's system discovery.
func distinctShipSystems(ships []*navigation.Ship) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(ships))
	for _, sh := range ships {
		loc := sh.CurrentLocation()
		if loc == nil {
			continue
		}
		system := shared.ExtractSystemSymbol(loc.Symbol)
		if _, ok := seen[system]; ok {
			continue
		}
		seen[system] = struct{}{}
		out = append(out, system)
	}
	return out
}

// --- explorer pool count (sp-a3yn: the hard-cap basis + shortfall Current) ---

// autosizerExplorerFleetSource counts the player's explorer-dedicated hulls (DedicatedFleet
// "explorer", stamped by dedicate-at-purchase). It is the hard-cap basis and the shortfall's Current
// the ExplorerDemandProvider reads; a read failure fails the class CLOSED (an unknowable pool must
// never buy, lest it breach the hard cap of 1).
type autosizerExplorerFleetSource struct{ shipRepo navigation.ShipRepository }

func (s *autosizerExplorerFleetSource) ExplorerCount(ctx context.Context, playerID int) (int, error) {
	return countShips(ctx, s.shipRepo, playerID, func(sh *navigation.Ship) bool {
		return sh.DedicatedFleet() == "explorer"
	})
}

// --- shared helper ---

func countShips(ctx context.Context, shipRepo navigation.ShipRepository, playerID int, pred func(*navigation.Ship) bool) (int, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return 0, err
	}
	ships, err := shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, s := range ships {
		if pred(s) {
			n++
		}
	}
	return n, nil
}

// --- heavy census + heavy yard (the reservation's two inputs) ---

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

// --- heavy-yard pricing errand (send a hull so a known-but-unpriced heavy yard prices) ---

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
