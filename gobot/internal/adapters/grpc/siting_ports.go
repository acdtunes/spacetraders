package grpc

import (
	"context"
	"encoding/json"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	goodsCmd "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/commands"
	goodsServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// NewSitingCoordinatorHandler assembles the factory-siting coordinator handler (sp-vdld M7),
// wiring every concrete port in this file to the daemon's live collaborators. It reuses the SAME
// SupplyChainResolver + MarketLocator the goods-factory path holds, so a candidate is priced
// exactly as the launch path prices it (through the sp-2dv4 ChainMarginGuard). The tour-alignment
// provider is deliberately left unset: the C1 stock-draw signal has no persisted read path and
// there is no tour_leg_telemetry throughput reader yet, so scoring ranks on branchPL alone — the
// documented monotonic proxy — until that seam lands.
func NewSitingCoordinatorHandler(
	server *DaemonServer,
	resolver *goodsServices.SupplyChainResolver,
	locator *goodsServices.MarketLocator,
	marketAges *persistence.MarketRepositoryGORM,
	marketReader market.MarketRepository,
	shipRepo navigation.ShipRepository,
	eventStore captain.EventStore,
) *goodsCmd.RunSitingCoordinatorHandler {
	scanner := goodsCmd.NewSitingScannerService(
		newSitingMarketSource(marketAges, marketReader, nil),
		&sitingRecipeFeedResolver{resolver: resolver},
		&sitingEligibleInputSource{locator: locator},
	)
	guard := goodsServices.NewChainMarginGuard(locator, marketReader)
	h := goodsCmd.NewRunSitingCoordinatorHandler(
		scanner,
		&sitingChainProjector{resolver: resolver, guard: guard},
		&sitingChainController{server: server},
		nil, // nil = use RealClock
	)
	h.SetWorkerCounter(&sitingWorkerCounter{shipRepo: shipRepo})
	h.SetScoutDemandEmitter(newSitingScoutDemandEmitter(eventStore, nil))
	return h
}

// This file wires the factory-siting coordinator's application ports (sp-vdld M7) to the
// concrete daemon collaborators. The M2-M6 logic depends only on narrow interfaces
// (goodsCmd.SitingScanner / ChainProjector / ChainController / WorkerCounter /
// ScoutDemandEmitter and the scanner's three sub-ports), tested against fakes; these are the
// thin bridges the daemon injects at boot. No business logic lives here — every method
// forwards to an existing service/repo and adapts its shape.

// --- SCAN: market source (siting_market_source) ---

// sitingSystemAgeSource is the systems-with-market-data enumerator (MarketRepositoryGORM's
// MaxAgeSecondsBySystem — the same freshness roll-up the scout-post coordinator uses). Its keys
// are every system the player has market data for; SCAN ranges over exactly those.
type sitingSystemAgeSource interface {
	MaxAgeSecondsBySystem(ctx context.Context, playerID int) (map[string]float64, error)
}

// sitingMarketReader reads a system's markets and their goods (the domain market.MarketRepository,
// satisfied by MarketRepositoryAdapter). SCAN filters the goods to EXPORT listings (the hard gate).
type sitingMarketReader interface {
	FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error)
	GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error)
}

// sitingMarketSource is the concrete goodsCmd.SitingMarketSource: it enumerates the player's
// market-bearing systems and, per system, every (good, trade-type) with its market-data age. Age
// is now − Market.LastUpdated (the scanner applies the freshness gate). A single unreadable
// waypoint is skipped, never fatal (the scanner is already fail-closed per candidate).
type sitingMarketSource struct {
	ages    sitingSystemAgeSource
	markets sitingMarketReader
	clock   shared.Clock
}

func newSitingMarketSource(ages sitingSystemAgeSource, markets sitingMarketReader, clock shared.Clock) *sitingMarketSource {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &sitingMarketSource{ages: ages, markets: markets, clock: clock}
}

func (s *sitingMarketSource) Systems(ctx context.Context, playerID int) ([]string, error) {
	ages, err := s.ages.MaxAgeSecondsBySystem(ctx, playerID)
	if err != nil {
		return nil, err
	}
	systems := make([]string, 0, len(ages))
	for system := range ages {
		systems = append(systems, system)
	}
	return systems, nil
}

func (s *sitingMarketSource) GoodsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]goodsCmd.MarketGood, error) {
	waypoints, err := s.markets.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	var out []goodsCmd.MarketGood
	for _, waypoint := range waypoints {
		m, err := s.markets.GetMarketData(ctx, waypoint, playerID)
		if err != nil || m == nil {
			continue // an unreadable market must not abort the system scan
		}
		ageSecs := now.Sub(m.LastUpdated()).Seconds()
		for _, tg := range m.TradeGoods() {
			out = append(out, goodsCmd.MarketGood{
				Good:      tg.Symbol(),
				TradeType: string(tg.TradeType()),
				AgeSecs:   ageSecs,
			})
		}
	}
	return out, nil
}

// --- SCAN: recipe feed resolver (in-system fabrication gate) ---

// sitingRecipeFeedResolver is the concrete goodsCmd.RecipeFeedResolver over
// SupplyChainResolver.BuildDependencyTree. BuildDependencyTree errors precisely when the recipe
// does not resolve entirely in-system (a fabricated sub-node has no in-system EXPORT factory —
// the XX56/a5j7 gate), which maps to inSystem=false. A resolved BUY/raw root (not a fabrication
// chain) is likewise not a factory site. The feeds are the tree's BUY leaves (market-sourced
// inputs).
type sitingRecipeFeedResolver struct {
	resolver *goodsServices.SupplyChainResolver
}

func (r *sitingRecipeFeedResolver) Feeds(ctx context.Context, targetGood, systemSymbol string, playerID int) ([]string, bool, error) {
	root, err := r.resolver.BuildDependencyTree(ctx, targetGood, systemSymbol, playerID)
	if err != nil || root == nil {
		// Does not resolve in-system (no in-system factory for a sub-input, unknown good, or a
		// cycle): a clean exclusion, not an infra error to surface — the scanner drops it.
		return nil, false, nil
	}
	if root.IsLeaf() || root.AcquisitionMethod != goods.AcquisitionFabricate {
		// Directly buyable / raw source — arbitrage, not a fabrication site.
		return nil, false, nil
	}
	feeds := collectBuyLeafGoods(root)
	return feeds, true, nil
}

// collectBuyLeafGoods walks a resolved dependency tree and returns the distinct goods of its
// BUY nodes — the market-sourced feed inputs the chain draws at market (first-seen order).
func collectBuyLeafGoods(root *goods.SupplyChainNode) []string {
	seen := make(map[string]struct{})
	var feeds []string
	var walk func(n *goods.SupplyChainNode)
	walk = func(n *goods.SupplyChainNode) {
		if n == nil {
			return
		}
		if n.AcquisitionMethod == goods.AcquisitionBuy {
			if _, ok := seen[n.Good]; !ok {
				seen[n.Good] = struct{}{}
				feeds = append(feeds, n.Good)
			}
			return
		}
		for _, child := range n.Children {
			walk(child)
		}
	}
	walk(root)
	return feeds
}

// --- SCAN: eligible input source (a5j7 supply-first) ---

// sitingEligibleInputSource is the concrete goodsCmd.EligibleInputSource over
// MarketLocator.FindExportMarketBySupplyPriority: it resolves a feed's supply-first eligible
// in-system source (a MODERATE+ EXPORT market — UQ16: import listings do not count). A nil result
// means no eligible source exists, which excludes the candidate.
type sitingEligibleInputSource struct {
	locator *goodsServices.MarketLocator
}

func (s *sitingEligibleInputSource) Source(ctx context.Context, good, systemSymbol string, playerID int) (string, bool, error) {
	res, err := s.locator.FindExportMarketBySupplyPriority(ctx, good, systemSymbol, playerID)
	if err != nil {
		return "", false, err
	}
	if res == nil {
		return "", false, nil // no MODERATE+ EXPORT source in-system (a5j7 supply-first)
	}
	return res.WaypointSymbol, true, nil
}

// --- SCORE: chain projector (branchPL via the launch guard) ---

// sitingChainProjector is the concrete goodsCmd.ChainProjector: it builds the candidate's
// dependency tree and prices it through the sp-2dv4 ChainMarginGuard. Proceed=false is the veto
// (negative margin, sink-absorption breach, or unpriceable) — the scorer drops a vetoed candidate
// at zero cost. A tree that will not build in-system is itself a fail-closed veto.
type sitingChainProjector struct {
	resolver *goodsServices.SupplyChainResolver
	guard    *goodsServices.ChainMarginGuard
}

func (p *sitingChainProjector) Project(ctx context.Context, good, system string, playerID int) (goodsCmd.ChainProjection, error) {
	root, err := p.resolver.BuildDependencyTree(ctx, good, system, playerID)
	if err != nil || root == nil {
		// Unbuildable in-system → fail-closed veto (never launch what cannot be priced/resolved).
		return goodsCmd.ChainProjection{Proceed: false, Reason: "chain does not resolve in-system"}, nil
	}
	proj := p.guard.Evaluate(ctx, root, system, playerID)
	return goodsCmd.ChainProjection{
		ProjectedPL: proj.ProjectedPL,
		Proceed:     proj.Proceed,
		Reason:      string(proj.Reason),
	}, nil
}

// --- ACT: chain controller (portfolio launch / retire / observe) ---

// sitingChainController is the concrete goodsCmd.ChainController. It launches standing goods_factory
// chains through the full guard stack (StartGoodsFactory with iterations=-1 — the child coordinator
// runs 2dv4 + a5j7 + C2 + r5a6 on its own passes), retires one via a clean container stop, and
// enumerates the running STANDING portfolio (iterations=-1 goods_factory containers) so a one-shot
// `goods-factory` run is never mistaken for a managed chain and torn down.
type sitingChainController struct {
	server *DaemonServer
}

func (c *sitingChainController) RunningChains(ctx context.Context, playerID int) ([]goodsCmd.RunningChain, error) {
	models, err := c.server.containerRepo.ListByStatus(ctx, container.ContainerStatusRunning, &playerID)
	if err != nil {
		return nil, err
	}
	var chains []goodsCmd.RunningChain
	for _, m := range models {
		if m.ContainerType != "goods_factory_coordinator" {
			continue
		}
		var cfg map[string]interface{}
		if m.Config != "" {
			if err := json.Unmarshal([]byte(m.Config), &cfg); err != nil {
				continue
			}
		}
		// Standing chains only: a one-shot factory run (iterations >= 1) is not a portfolio
		// member — never retire it, never diff against it.
		iter, ok := cfg["max_iterations"].(float64)
		if !ok || iter != -1 {
			continue
		}
		good, _ := cfg["target_good"].(string)
		system, _ := cfg["system_symbol"].(string)
		if good == "" || system == "" {
			continue
		}
		chains = append(chains, goodsCmd.RunningChain{FactoryID: m.ID, Good: good, System: system})
	}
	return chains, nil
}

func (c *sitingChainController) Launch(ctx context.Context, good, system string, playerID int) (string, error) {
	res, err := c.server.StartGoodsFactory(ctx, good, system, playerID, -1, false)
	if err != nil {
		return "", err
	}
	return res.FactoryID, nil
}

func (c *sitingChainController) Retire(ctx context.Context, factoryID string, playerID int) error {
	return c.server.StopGoodsFactory(ctx, factoryID, playerID)
}

// --- MAINTAIN: worker counter (C3 K-sizing) ---

// sitingHaulerRole is the Ship.Role() value that marks a manufacturing worker hull — the pool the
// C3 rotation math sizes the portfolio against (K = floor(haulers / workers_per_chain)). Mirrors
// the contract ship-pool manager's roleHauler.
const sitingHaulerRole = "HAULER"

// sitingWorkerCounter is the concrete goodsCmd.WorkerCounter: it counts the player's HAULER hulls
// (the manufacturing worker pool) for K-derivation.
type sitingWorkerCounter struct {
	shipRepo navigation.ShipRepository
}

func (w *sitingWorkerCounter) CountWorkers(ctx context.Context, playerID int) (int, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return 0, err
	}
	ships, err := w.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, s := range ships {
		if s.Role() == sitingHaulerRole {
			n++
		}
	}
	return n, nil
}

// --- EMIT: scout-demand emitter (captain scout-post-proposal channel) ---

// sitingScoutEventStore is the captain event port EMIT needs: HasSince for the per-system cooldown
// dedup and Record to post the demand (GormCaptainEventRepository satisfies it).
type sitingScoutEventStore interface {
	HasSince(ctx context.Context, playerID int, t captain.EventType, ship string, since time.Time) (bool, error)
	Record(ctx context.Context, e *captain.Event) error
}

// sitingScoutDemandEmitter is the concrete goodsCmd.ScoutDemandEmitter: it posts a
// scout.post_proposal for a stale-but-promising system, deduped over the cooldown window via
// HasSince — the same channel and idiom the discovery detector's emitPostProposals uses. Returns
// true only when a NEW demand was recorded.
type sitingScoutDemandEmitter struct {
	store sitingScoutEventStore
	clock shared.Clock
}

// newSitingScoutDemandEmitter wires the emitter; a nil clock defaults to the real clock.
func newSitingScoutDemandEmitter(store sitingScoutEventStore, clock shared.Clock) *sitingScoutDemandEmitter {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &sitingScoutDemandEmitter{store: store, clock: clock}
}

func (e *sitingScoutDemandEmitter) EmitScoutDemand(ctx context.Context, playerID int, system string, cooldown time.Duration, payload string) (bool, error) {
	recent, err := e.store.HasSince(ctx, playerID, captain.EventScoutPostProposal, system, e.clock.Now().Add(-cooldown))
	if err != nil {
		return false, err
	}
	if recent {
		return false, nil // already demanded within the cooldown window
	}
	if err := e.store.Record(ctx, &captain.Event{
		Type:     captain.EventScoutPostProposal,
		Ship:     system,
		PlayerID: playerID,
		Payload:  payload,
	}); err != nil {
		return false, err
	}
	return true, nil
}
