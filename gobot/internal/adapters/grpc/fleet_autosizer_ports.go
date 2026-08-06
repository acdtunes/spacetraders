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
	shipyardCmd "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	tradingQueries "github.com/andrescamacho/spacetraders-go/internal/application/trading/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/apibudget"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/hullbuy"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// agentReader is the narrow slice of *api.SpaceTradersClient the money guards need (treasury).
// Declared here so the ports depend on behaviour, not the whole client.
type agentReader interface {
	GetAgent(ctx context.Context, token string) (*player.AgentData, error)
}

// NewFleetAutosizerCoordinatorHandler assembles the autosizer handler (sp-1txd M6), wiring every
// concrete port to the daemon's live collaborators and registering the light + heavy demand providers.
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

	// Buy-path readers + writers.
	h.SetTreasuryReader(&autosizerTreasuryReader{api: apiClient, ledger: ledgerTreasury})
	// The tracker is RESOLVED PER READ, not captured here (sp-a75fz). Passing
	// metrics.GetGlobalAPIBudgetTracker itself — the function, uncalled — is what makes the
	// wiring order stop mattering; see autosizerAPIUtilReader.
	h.SetAPIUtilizationReader(&autosizerAPIUtilReader{resolve: globalAPIBudgetReporter})
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
			// The errand now draws its carrier from the PARKED SENSING pool (sp-gmfvw), which it
			// SHARES with the scout coordinator — so it must also see which probes a live scout
			// post already owns. The constructor takes the server and reads the roster off its
			// connection, which is what keeps that read from being something this line can omit.
			h.SetHeavyPricingErrandPort(newAutosizerPricingErrand(server, med, shipRepo))
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
// THE TRACKER IS RESOLVED AT READ TIME, NOT CAPTURED AT WIRING TIME (sp-a75fz).
//
// It used to hold the pointer that metrics.GetGlobalAPIBudgetTracker() returned during wiring. That
// was correct ONLY because main.go happens to construct the tracker (:778) before it wires the
// autosizer (:1196), and nothing enforced, tested or documented that ordering.
//
// REVERSE IT AND THE FLEET STOPS GROWING, SILENTLY AND FOREVER. The captured pointer would be nil,
// the nil guard below would fail CLOSED — which is correct, and is exactly why nobody would find
// it — so GuardAPIUtil would refuse every hull purchase, everywhere, with no error and no metric.
// It would read as "the autosizer decided not to grow". That is the sp-ps2oc failure shape: a
// wiring assumption nothing pins, whose breach is invisible.
//
// A resolver function makes the ordering irrelevant rather than merely checked. The field can no
// longer HOLD a tracker, so there is no wiring-time capture left to get wrong — the bug is
// unexpressible in this type rather than absent from this instance. It also degrades better in the
// world where the order does reverse: a nil read is TRANSIENT and self-heals on the next tick once
// the global is set, where a captured nil was permanent for the life of the process.
//
// The per-read cost is one package-variable load on a reconcile tick that already does database and
// API work — not a hot path, and the guard is consulted once per sizing decision.
//
// Fails CLOSED (readable=false) when no live surface exists: no resolver, a resolver returning
// nothing, or an unconfigured/zero ceiling. A guard that cannot read its bound never permits growth
// (RULINGS #4). In the daemon the tracker is wired unconditionally at startup, so the normal case is
// readable and blocking only occurs on real saturation or a genuinely-absent metrics subsystem.
type autosizerAPIUtilReader struct{ resolve func() apiBudgetReporter }

// globalAPIBudgetReporter adapts the package-level accessor to the reader's narrow interface.
//
// THE TYPED-NIL CONVERSION IS DELIBERATE AND MUST STAY EXPLICIT. GetGlobalAPIBudgetTracker returns a
// *metrics.APIBudgetTracker; assigning a nil one into an interface yields a NON-nil interface
// holding a nil pointer, which sails past `== nil`. Returning the untyped nil instead keeps the
// reader's own nil check meaningful rather than leaving it to the nil-receiver Report() one layer
// down. Both paths fail closed — this one just fails closed legibly.
func globalAPIBudgetReporter() apiBudgetReporter {
	tracker := metrics.GetGlobalAPIBudgetTracker()
	if tracker == nil {
		return nil
	}
	return tracker
}

func (r *autosizerAPIUtilReader) UtilizationPct(ctx context.Context) (float64, bool, error) {
	if r == nil || r.resolve == nil {
		return 0, false, nil // no utilization surface wired → unreadable → guard fails CLOSED
	}
	reporter := r.resolve()
	if reporter == nil {
		return 0, false, nil // resolved to nothing this tick → unreadable → guard fails CLOSED
	}
	rolling := reporter.Report().Rolling5m
	if rolling.CeilingReqPerSec <= 0 {
		// A typed-nil tracker's nil-safe Report() (or a tracker built with no ceiling) yields a
		// zero-value report; without a ceiling there is no meaningful utilization → fail CLOSED
		// rather than let a spurious readable 0% permit unbounded growth.
		return 0, false, nil
	}
	return rolling.UtilizationPct, true, nil
}

// autosizerPurchaser buys one hull through the money-integrity batch path, then dedicates it.
// It is driven by BOTH the autosizer and the dedicated contract scaler, so it speaks the neutral
// hullbuy vocabulary rather than either coordinator's.
type autosizerPurchaser struct {
	med      common.Mediator
	shipRepo navigation.ShipRepository
}

func (p *autosizerPurchaser) BuyAndDedicate(ctx context.Context, order hullbuy.BuyOrder) (hullbuy.BuyResult, error) {
	pid, err := shared.NewPlayerID(order.PlayerID)
	if err != nil {
		return hullbuy.BuyResult{}, err
	}
	// The purchase needs a hull to travel to and buy at the shipyard. sp-7r7w: PREFER the exclusive
	// purchasing ship (the pivoted command frigate) when idle — the deterministic, protected buy ship for
	// every scaling buy — and fall back to any idle hull if it is momentarily busy or not yet established.
	// The battle-tested batch path navigates it and enforces the money-integrity type guard.
	ships, err := p.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return hullbuy.BuyResult{}, err
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
		return hullbuy.BuyResult{}, fmt.Errorf("no idle hull available to execute the purchase")
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
		return hullbuy.BuyResult{}, err
	}
	batch, ok := resp.(*shipyardCmd.BatchPurchaseShipsResponse)
	if !ok || batch.ShipsPurchasedCount == 0 || len(batch.PurchasedShips) == 0 {
		return hullbuy.BuyResult{}, fmt.Errorf("purchase returned no ship")
	}
	bought := batch.PurchasedShips[0]

	// Dedicate-at-purchase: tag heavy/explorer/contract hulls to their fleet in the same breath so no
	// coordinator tick can adopt them first. Idempotent; lights get no tag (they ARE workers) — the
	// asymmetry, and why it is load-bearing, is on hullbuy.DedicatedFleet.
	dedicated := false
	if fleet := hullbuy.DedicatedFleet(order.Class); fleet != "" {
		if aerr := p.shipRepo.AssignFleet(ctx, bought.ShipSymbol(), fleet, pid); aerr != nil {
			return hullbuy.BuyResult{}, fmt.Errorf("bought %s but failed to dedicate to %q: %w", bought.ShipSymbol(), fleet, aerr)
		}
		dedicated = true
	}
	return hullbuy.BuyResult{ShipSymbol: bought.ShipSymbol(), Price: int64(batch.TotalCost), Dedicated: dedicated}, nil
}

// autosizerNotifier records a purchase as a captain event — a buy is real news.
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

// autosizerMetricsSink adapts the fleet MetricsSink to the global collector's Record funcs.
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

func (m *autosizerMetricsSink) RecordHeavyReserve(playerID string, reserve, target int64, owned, capacity int) {
	metrics.RecordAutosizerHeavyReserve(playerID, reserve, target, owned, capacity)
}

func (m *autosizerMetricsSink) ObserveHeavyPricePremium(playerID string, paid, cheapestKnown int64) {
	metrics.ObserveAutosizerHeavyPricePremium(playerID, paid, cheapestKnown)
}

func (m *autosizerMetricsSink) RecordSizingEnabled(playerID string, enabled bool) {
	metrics.RecordAutosizerSizingEnabled(playerID, enabled)
}
