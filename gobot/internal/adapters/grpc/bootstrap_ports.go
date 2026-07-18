package grpc

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	ledgerQuery "github.com/andrescamacho/spacetraders-go/internal/application/ledger/queries"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipyardCmd "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// This file wires the captain bootstrap coordinator's concrete ports (sp-3nbe) to the daemon's live
// collaborators, mirroring fleet_autosizer_ports.go. The application-layer reconciler
// (bootstrapCmd.RunBootstrapCoordinatorHandler) owns the sequencing/gating/staging/recovery logic
// and is unit-tested against fakes; these adapters are the thin, real-service edges — reusing
// SyncAllFromAPI (the phantom-cache guard), GetAgent (treasury), shipyard list + BatchPurchaseShips
// (price-check + buy), and AssignScoutingFleet (scout-all-markets). It BUILDS nothing new.

const (
	// commandRole is the flagship's registration role — its system is the cold-start home system.
	commandRole = "COMMAND"
	// marketplaceTrait / shipyardTrait are the waypoint traits the coverage + price reads filter on.
	marketplaceTrait = "MARKETPLACE"
	shipyardTrait    = "SHIPYARD"
	// bootstrapMarketFreshnessMin bounds how old a market's data may be and still count as
	// "covered" for the DATA coverage bar. Generous (24h) because markets are actively scouted
	// during bootstrap, so coverage measures BREADTH (how many marketplaces have data), not
	// staleness. A tighter freshness window is a later refinement.
	bootstrapMarketFreshnessMin = 24 * 60
	// contractFleetTag is the dedicated-fleet tag the contract coordinator's dedicated pool selects on
	// (matches the contract package's dedicatedFleetContract). A hauler carrying it is adopted as a
	// contract worker (and puts the pool in exclusive mode, dropping the untagged frigate); the frigate
	// retire clears it. The INCOME window (1h) is the trailing span the realized-$/hr read averages.
	contractFleetTag      = "contract"
	bootstrapIncomeWindow = time.Hour
)

// NewBootstrapCoordinatorHandler assembles the bootstrap reconciler (sp-3nbe M4), wiring every
// concrete port to the daemon's live collaborators. LIVE BY DEFAULT once first-launched; recovery
// -adopted on restart. server drives the scout-all-markets assignment; apiClient reads treasury;
// shipRepo backs the phantom-cache refresh + fleet observation; med runs the price-check + buy;
// waypointRepo + marketRepo back the market-coverage read.
func NewBootstrapCoordinatorHandler(
	server *DaemonServer,
	apiClient *api.SpaceTradersClient,
	shipRepo navigation.ShipRepository,
	med common.Mediator,
	waypointRepo *persistence.GormWaypointRepository,
	marketRepo *persistence.MarketRepositoryAdapter,
	contractRepo contract.ContractRepository,
) *bootstrapCmd.RunBootstrapCoordinatorHandler {
	h := bootstrapCmd.NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&bootstrapRefresher{shipRepo: shipRepo})
	h.SetWorldObserver(&bootstrapObserver{
		api: apiClient, shipRepo: shipRepo, waypointRepo: waypointRepo, marketRepo: marketRepo,
		med: med, contractRepo: contractRepo, containerRepo: server.containerRepo, server: server,
	})
	// One acquirer instance drives both the DATA probe buy and (embedded) the INCOME hauler price-check
	// + buy — the yard price-scan + batch-purchase plumbing is asset-agnostic (parameterised by shipType).
	acq := &bootstrapAcquirer{med: med, shipRepo: shipRepo, waypointRepo: waypointRepo}
	h.SetProbeAcquirer(acq)
	h.SetHaulerAcquirer(&bootstrapHaulerAcquirer{bootstrapAcquirer: acq})
	h.SetScoutAssigner(&bootstrapScouter{server: server})
	// sp-hh0h: the cold-start shipyard-readability positioner. On a fresh universe nothing has visited
	// the home shipyard, so its live (presence-gated) price is unreadable and the probe buy fails closed
	// forever; this flies an idle hull to the yard so the next tick's live PriceCheck reads. Same deps as
	// the acquirer (mediator navigate + ship/waypoint repos) — builds nothing new.
	h.SetShipyardScanner(&bootstrapShipyardScanner{med: med, shipRepo: shipRepo, waypointRepo: waypointRepo})
	h.SetFrigateRetirer(&bootstrapFrigateRetirer{shipRepo: shipRepo})
	h.SetContractRunner(&bootstrapContractRunner{server: server})
	h.SetMetricsSink(&bootstrapMetricsSink{})
	// sp-r6yq: the per-tick live-config reader makes every bootstrap knob honor
	// `spacetraders tune --operation bootstrap` on the next reconcile with no restart. Reads the
	// same persisted config column the tune verb writes (ContainerConfigReader).
	h.SetLiveConfigReader(NewContainerConfigReader(server.containerRepo))

	// GATE-phase collaborators (Slice 3): construction start, the manufacturing-executor ensure/bounce,
	// the repurpose-to-manufacturing re-tag, the gate-worker buy, and the COMPLETE hand-off — each a thin
	// wrapper over an existing daemon capability (build nothing new).
	h.SetConstructionManager(&bootstrapConstructionManager{server: server})
	h.SetManufacturingController(&bootstrapManufacturingController{server: server})
	h.SetWorkerRepurposer(&bootstrapWorkerRepurposer{shipRepo: shipRepo})
	h.SetGateWorkerAcquirer(&bootstrapGateWorkerAcquirer{bootstrapAcquirer: acq, shipRepo: shipRepo})
	h.SetHandoffLauncher(&bootstrapHandoffLauncher{server: server})
	return h
}

// --- ship refresher (phantom-cache guard, captain L47) ---

type bootstrapRefresher struct{ shipRepo navigation.ShipRepository }

func (r *bootstrapRefresher) RefreshFleet(ctx context.Context, playerID int) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}
	_, err = r.shipRepo.SyncAllFromAPI(ctx, pid)
	return err
}

// --- world observer (fleet counts + coverage + treasury + home system) ---

type bootstrapObserver struct {
	api          agentReader
	shipRepo     navigation.ShipRepository
	waypointRepo *persistence.GormWaypointRepository
	marketRepo   *persistence.MarketRepositoryAdapter
	// INCOME-phase reads (Slice 2). med runs the realized-$/hr ledger query; contractRepo lists the
	// active contracts' demanded goods (hub bias); containerRepo answers "is batch-contract running?".
	med           common.Mediator
	contractRepo  contract.ContractRepository
	containerRepo *persistence.ContainerRepositoryGORM
	// GATE-phase reads (Slice 3). server runs the construction-site discovery + status snapshot and the
	// executor/autosizer container-running checks. All best-effort (a miss leaves the field zero-valued).
	server *DaemonServer
}

func (o *bootstrapObserver) Observe(ctx context.Context, playerID int) (bootstrapCmd.Observation, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return bootstrapCmd.Observation{Readable: false, Reason: fmt.Sprintf("bad player id: %v", err)}, nil
	}
	ships, err := o.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return bootstrapCmd.Observation{}, err // infra fault → tick skip (logged by the reconciler)
	}

	obs := bootstrapCmd.Observation{}
	commandHome, anyHome := "", ""
	for _, s := range ships {
		if s.IsScoutType() {
			obs.ProbeCount++
			// A dispatched (non-idle) probe is scouting; a fresh probe idle at the yard is not yet.
			if !s.IsIdle() {
				obs.ProbesScouting++
			}
		}
		if s.IsIdle() {
			obs.HasIdlePurchaser = true
		}
		wp := ""
		if loc := s.CurrentLocation(); loc != nil {
			wp = loc.Symbol
			sys := shared.ExtractSystemSymbol(loc.Symbol)
			if anyHome == "" {
				anyHome = sys
			}
			if s.Role() == commandRole {
				commandHome = sys
			}
		}
		// INCOME fleet signals (Slice 2): the command frigate (retire target) and the contract-dedicated
		// haulers (the staged-buy count + placement guard). A hull tagged "contract" that is the command
		// frigate is NOT a hauler — it is the retire target, tracked separately.
		if s.Role() == commandRole {
			obs.CommandFrigateID = s.ShipSymbol()
			obs.CommandFrigateOnContract = s.DedicatedFleet() == contractFleetTag
		} else if s.DedicatedFleet() == contractFleetTag {
			obs.Haulers = append(obs.Haulers, bootstrapCmd.HaulerSnapshot{Symbol: s.ShipSymbol(), Waypoint: wp})
		} else if s.DedicatedFleet() == manufacturingFleetTag {
			// A hull dedicated to the manufacturing fleet is a gate-construction worker (Slice 3) — the
			// worker-sizing "have" count, so the staged top-up buy never overshoots the pipeline's shape.
			obs.GateWorkers++
		}
	}
	obs.HomeSystem = commandHome
	if obs.HomeSystem == "" {
		obs.HomeSystem = anyHome
	}

	// Treasury — the capital-gate input. No token / unreadable agent ⇒ fail closed (no action).
	token, terr := common.PlayerTokenFromContext(ctx)
	if terr != nil {
		obs.Reason = "no player token in context"
		return obs, nil
	}
	agent, aerr := o.api.GetAgent(ctx, token)
	if aerr != nil || agent == nil {
		obs.Reason = fmt.Sprintf("agent credits unreadable: %v", aerr)
		return obs, nil
	}
	obs.Treasury = int64(agent.Credits)

	// Coverage — home-system marketplaces total vs those with (fresh) market data. A read miss on
	// either leaves that count 0, which reads as uncovered (stay in DATA) rather than falsely exiting.
	// The single ListMarketsInSystem read serves BOTH the DATA coverage count AND the INCOME hub
	// selector's market snapshots (sourceable goods + prices).
	if obs.HomeSystem != "" {
		if wps, werr := o.waypointRepo.ListBySystemWithTrait(ctx, obs.HomeSystem, marketplaceTrait); werr == nil {
			obs.MarketsTotal = len(wps)
		}
		if mkts, merr := o.marketRepo.ListMarketsInSystem(ctx, uint(playerID), obs.HomeSystem, bootstrapMarketFreshnessMin); merr == nil {
			obs.MarketsCovered = len(mkts)
			obs.Markets = toMarketSnapshots(mkts)
		}
	}

	// INCOME-phase reads (Slice 2). Each is BEST-EFFORT: a miss leaves the field at its zero value,
	// which the reconciler reads fail-safe — 0 $/hr keeps the arc in INCOME (never premature GATE),
	// empty Markets means no hubs (no hauler buys), empty ContractGoods falls back to density+cheapness.
	obs.IncomePerHour = o.readIncomePerHour(ctx, playerID)
	if o.contractRepo != nil {
		if contracts, cerr := o.contractRepo.FindActiveContracts(ctx, playerID); cerr == nil {
			obs.ContractGoods = contractDemandGoods(contracts)
		}
	}
	if o.containerRepo != nil {
		if running, rerr := contractFleetCoordinatorRunning(ctx, o.containerRepo, playerID); rerr == nil {
			obs.BatchContractRunning = running
		}
		// sp-tsn2 probe-buyer arbitration input: is a market-freshness-sizer coordinator running to take
		// over probe acquisition? Best-effort — a read miss leaves it false, so bootstrap keeps buying
		// (never defers into a vacuum). Inert unless defer_probe_to_freshsizer is armed.
		if running, rerr := containerTypeRunning(ctx, o.containerRepo, playerID, container.ContainerTypeMarketFreshnessSizer); rerr == nil {
			obs.FreshsizerActive = running
		}
	}

	// GATE-phase reads (Slice 3). All best-effort: a miss leaves the field zero-valued, which the
	// reconciler reads fail-safe — an unknown gate site holds GATE (no_gate_site), 0% never completes,
	// and an executor/autosizer read miss defers to the guarded action.
	if o.server != nil {
		snap := o.server.readBootstrapGateSnapshot(ctx, obs.HomeSystem, playerID)
		obs.GateSite = snap.Site
		obs.ConstructionStarted = snap.Started
		obs.ConstructionComplete = snap.Complete
		obs.ConstructionPercent = snap.Percent
		obs.GateMaterialChains = snap.MaterialChain
		obs.ManufacturingAdopted = snap.Adopted
	}
	if o.containerRepo != nil {
		if running, rerr := containerTypeRunning(ctx, o.containerRepo, playerID, executorContainerTypes...); rerr == nil {
			obs.ManufacturingRunning = running
		}
		if running, rerr := containerTypeRunning(ctx, o.containerRepo, playerID, container.ContainerTypeFleetAutosizer); rerr == nil {
			obs.AutosizerRunning = running
		}
	}

	obs.Readable = true
	return obs, nil
}

// readIncomePerHour reads the player's realized NET credits over the trailing income window (reusing
// the ledger GetProfitLoss query) — the INCOME→GATE exit input. Realized (booked ledger rows), not
// projected. A read miss returns 0, which keeps the arc in INCOME (never a premature GATE).
func (o *bootstrapObserver) readIncomePerHour(ctx context.Context, playerID int) float64 {
	if o.med == nil {
		return 0
	}
	now := time.Now()
	resp, err := o.med.Send(ctx, &ledgerQuery.GetProfitLossQuery{
		PlayerID:  playerID,
		StartDate: now.Add(-bootstrapIncomeWindow),
		EndDate:   now,
	})
	if err != nil {
		return 0
	}
	pl, ok := resp.(*ledgerQuery.GetProfitLossResponse)
	if !ok || pl == nil {
		return 0
	}
	// The window is exactly bootstrapIncomeWindow (1h), so NetProfit over it IS the net $/hr.
	return float64(pl.NetProfit)
}

// toMarketSnapshots projects the persisted markets into the hub selector's input: per waypoint, the
// goods a hauler can SOURCE (buy). Only EXPORT/EXCHANGE goods are sourceable (an IMPORT-only good is
// one the market CONSUMES). The price is SellPrice() — the market ASK, i.e. what a ship PAYS to buy —
// because this codebase's TradeGood swaps the API's purchase/sell prices (market_scanner.go). A
// non-positive ask is dropped (fail-closed). Markets with no sourceable good are omitted.
func toMarketSnapshots(mkts []market.Market) []bootstrapCmd.MarketSnapshot {
	out := make([]bootstrapCmd.MarketSnapshot, 0, len(mkts))
	for i := range mkts {
		m := mkts[i]
		snap := bootstrapCmd.MarketSnapshot{
			Waypoint: m.WaypointSymbol(),
			System:   shared.ExtractSystemSymbol(m.WaypointSymbol()),
		}
		for _, g := range m.TradeGoods() {
			if tt := g.TradeType(); tt != market.TradeTypeExport && tt != market.TradeTypeExchange {
				continue // IMPORT-only: the market consumes it — a hauler cannot source it here
			}
			ask := g.SellPrice() // domain-swapped: SellPrice() is what a ship PAYS to buy (the ask)
			if ask <= 0 {
				continue
			}
			snap.Goods = append(snap.Goods, bootstrapCmd.MarketGood{Symbol: g.Symbol(), PurchasePrice: int64(ask)})
		}
		if len(snap.Goods) > 0 {
			out = append(out, snap)
		}
	}
	return out
}

// contractDemandGoods collects the distinct trade symbols the player's active contracts require
// delivered — the hub selector's contract-good bias. Empty when no accepted contract exists yet (the
// selector then falls back to market density + cheapness), which is the normal state at INCOME start.
func contractDemandGoods(contracts []*contract.Contract) []string {
	seen := map[string]struct{}{}
	var goods []string
	for _, c := range contracts {
		if c == nil {
			continue
		}
		for _, d := range c.Terms().Deliveries {
			if d.TradeSymbol == "" {
				continue
			}
			if _, ok := seen[d.TradeSymbol]; ok {
				continue
			}
			seen[d.TradeSymbol] = struct{}{}
			goods = append(goods, d.TradeSymbol)
		}
	}
	return goods
}

// contractFleetCoordinatorRunning reports whether a contract fleet coordinator container is already
// PENDING or RUNNING for the player — the batch-contract idempotency read, used by the observer
// (BatchContractRunning) and the runner (defense-in-depth launch guard). Mirrors the autosizer's
// container-list guard (fleet_autosizer_ports.go).
func contractFleetCoordinatorRunning(ctx context.Context, repo *persistence.ContainerRepositoryGORM, playerID int) (bool, error) {
	for _, st := range []container.ContainerStatus{container.ContainerStatusRunning, container.ContainerStatusPending} {
		models, err := repo.ListByStatus(ctx, st, &playerID)
		if err != nil {
			return false, err
		}
		for _, m := range models {
			if m.ContainerType == string(container.ContainerTypeContractFleetCoordinator) {
				return true, nil
			}
		}
	}
	return false, nil
}

// --- probe acquirer (shipyard list price-check + BatchPurchaseShips) ---

type bootstrapAcquirer struct {
	med          common.Mediator
	shipRepo     navigation.ShipRepository
	waypointRepo *persistence.GormWaypointRepository
}

// PriceCheck finds the cheapest priced listing for shipType at a SHIPYARD-trait waypoint in a system
// where the player operates. readable=false (capital gate fails closed) when no priced listing is
// found.
func (a *bootstrapAcquirer) PriceCheck(ctx context.Context, playerID int, shipType string) (int64, string, bool, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return 0, "", false, nil
	}
	ships, err := a.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return 0, "", false, nil
	}
	systems := map[string]struct{}{}
	for _, s := range ships {
		if loc := s.CurrentLocation(); loc != nil {
			systems[shared.ExtractSystemSymbol(loc.Symbol)] = struct{}{}
		}
	}
	var cheapest int64
	var cheapestYard string
	for system := range systems {
		waypoints, werr := a.waypointRepo.ListBySystemWithTrait(ctx, system, shipyardTrait)
		if werr != nil {
			continue
		}
		for _, wp := range waypoints {
			if wp == nil {
				continue
			}
			price, ok := a.priceAtShipyard(ctx, system, wp.Symbol, shipType, pid)
			if !ok {
				continue
			}
			if cheapestYard == "" || price < cheapest {
				cheapest, cheapestYard = price, wp.Symbol
			}
		}
	}
	if cheapestYard == "" {
		return 0, "", false, nil
	}
	return cheapest, cheapestYard, true, nil
}

func (a *bootstrapAcquirer) priceAtShipyard(ctx context.Context, system, waypoint, shipType string, pid shared.PlayerID) (int64, bool) {
	q := &shipyardQueries.GetShipyardListingsQuery{SystemSymbol: system, WaypointSymbol: waypoint, PlayerID: pid}
	resp, err := a.med.Send(ctx, q)
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

// Buy purchases ONE shipType at yard through the money-integrity batch path (which navigates an idle
// hull to the yard and enforces the sp-e7je type guard). Probes are scouts — no dedicated-fleet tag.
func (a *bootstrapAcquirer) Buy(ctx context.Context, playerID int, shipType, yard string) (bootstrapCmd.BuyResult, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return bootstrapCmd.BuyResult{}, err
	}
	ships, err := a.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return bootstrapCmd.BuyResult{}, err
	}
	purchaser := ""
	for _, s := range ships {
		if s.IsIdle() {
			purchaser = s.ShipSymbol()
			break
		}
	}
	if purchaser == "" {
		return bootstrapCmd.BuyResult{}, fmt.Errorf("no idle hull available to execute the purchase")
	}

	resp, err := a.med.Send(ctx, &shipyardCmd.BatchPurchaseShipsCommand{
		PurchasingShipSymbol: purchaser,
		ShipType:             shipType,
		Quantity:             1,
		MaxBudget:            0,
		PlayerID:             pid,
		ShipyardWaypoint:     yard,
	})
	if err != nil {
		return bootstrapCmd.BuyResult{}, err
	}
	batch, ok := resp.(*shipyardCmd.BatchPurchaseShipsResponse)
	if !ok || batch.ShipsPurchasedCount == 0 || len(batch.PurchasedShips) == 0 {
		return bootstrapCmd.BuyResult{}, fmt.Errorf("purchase returned no ship")
	}
	bought := batch.PurchasedShips[0]
	return bootstrapCmd.BuyResult{ShipSymbol: bought.ShipSymbol(), Price: int64(batch.TotalCost)}, nil
}

// --- hauler acquirer (INCOME: reuse the probe price-check + buy, then dedicate + place on the hub) ---

// bootstrapHaulerAcquirer embeds the DATA-phase acquirer to reuse its cheapest-yard PriceCheck and the
// money-integrity BatchPurchaseShips buy (both asset-agnostic, parameterised by shipType); it only adds
// the contract-fleet dedication + hub placement that distinguish a positioned contract hauler from a
// free scout. Building nothing new (spec §Reuse).
type bootstrapHaulerAcquirer struct {
	*bootstrapAcquirer
}

// BuyAndPlace buys ONE hauler at yard (reused batch path), dedicates it to the contract fleet so the
// contract coordinator's dedicated pool adopts it (and, being the first tagged hull, seals the pool in
// exclusive mode — dropping the untagged frigate), then navigates it to its hub. The dedication uses
// the single fleet-assign write path (shipRepo.AssignFleet); placement reuses the high-level
// NavigateRouteCommand (route/refuel/flight-mode handled, idempotent if already there).
func (a *bootstrapHaulerAcquirer) BuyAndPlace(ctx context.Context, playerID int, shipType, yard, hubWaypoint string) (bootstrapCmd.BuyResult, error) {
	bought, err := a.Buy(ctx, playerID, shipType, yard)
	if err != nil {
		return bootstrapCmd.BuyResult{}, err
	}
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return bought, err
	}
	// Dedicate to the contract fleet — the tag is what makes batch-contract's dedicated pool adopt it.
	if derr := a.shipRepo.AssignFleet(ctx, bought.ShipSymbol, contractFleetTag, pid); derr != nil {
		return bought, fmt.Errorf("dedicate hauler %s to contract fleet: %w", bought.ShipSymbol, derr)
	}
	// Place it on its hub. A nav miss is surfaced (the hull is bought + dedicated; a later tick's
	// batch-contract still adopts it wherever it is — placement is an optimisation, not a correctness bar).
	if _, nerr := a.med.Send(ctx, &navCmd.NavigateRouteCommand{ShipSymbol: bought.ShipSymbol, Destination: hubWaypoint, PlayerID: pid}); nerr != nil {
		return bought, fmt.Errorf("navigate hauler %s to hub %s: %w", bought.ShipSymbol, hubWaypoint, nerr)
	}
	return bought, nil
}

// --- frigate retirer (INCOME: clear the command frigate's contract dedication — fleet unassign) ---

type bootstrapFrigateRetirer struct{ shipRepo navigation.ShipRepository }

// RetireFromContract clears the frigate's dedicated-fleet tag (fleet unassign = AssignFleet with ""),
// removing it from the contract coordinator's dedicated pool. Idempotent at the repo (a clear on an
// untagged hull is a no-op); the reconciler already guards on the observation.
func (r *bootstrapFrigateRetirer) RetireFromContract(ctx context.Context, playerID int, shipSymbol string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}
	return r.shipRepo.AssignFleet(ctx, shipSymbol, "", pid)
}

// --- contract runner (INCOME: launch the contract fleet coordinator — workflow batch-contract) ---

type bootstrapContractRunner struct{ server *DaemonServer }

// StartBatchContract launches the contract fleet coordinator (dynamic ship discovery — empty slices).
// It re-checks the container repo first (defense in depth beyond the observation's BatchContractRunning
// guard) because ContractFleetCoordinator is not itself idempotent — so a stale observation can never
// spawn a duplicate coordinator.
func (r *bootstrapContractRunner) StartBatchContract(ctx context.Context, playerID int) error {
	running, err := contractFleetCoordinatorRunning(ctx, r.server.containerRepo, playerID)
	if err != nil {
		return err
	}
	if running {
		return nil
	}
	_, err = r.server.ContractFleetCoordinator(ctx, nil, playerID, nil, nil)
	return err
}

// --- scout assigner (reuse the VRP scout-all-markets fleet assignment) ---

type bootstrapScouter struct{ server *DaemonServer }

func (s *bootstrapScouter) AssignAllMarkets(ctx context.Context, playerID int, system string) error {
	_, err := s.server.AssignScoutingFleet(ctx, system, playerID)
	return err
}

// --- shipyard scanner (sp-hh0h: position a hull at the home yard so the cold price reads) ---

type bootstrapShipyardScanner struct {
	med          common.Mediator
	shipRepo     navigation.ShipRepository
	waypointRepo *persistence.GormWaypointRepository
}

// EnsureHomeShipyardReadable positions an idle hull at a home-system SHIPYARD waypoint so the NEXT
// tick's live GetShipyard (bootstrapAcquirer.PriceCheck) returns priced listings. The SpaceTraders
// shipyard ship listing is PRESENCE-GATED — empty unless a hull is at the waypoint — so on a fresh
// universe the probe price is unreadable until something visits the yard. This navigates the command
// frigate / an idle hull there (reusing NavigateRouteCommand, the same high-level route+refuel path
// BuyAndPlace uses); presence (in orbit) is enough for the listing to read — the buy path docks.
//
// Idempotent + best-effort (returns dispatched=false, nil rather than churn):
//   - a hull is already present (not in transit) at a shipyard ⇒ the price reads next tick, no dispatch;
//   - the only free hull is already IN_TRANSIT (heading to the yard from a prior dispatch) ⇒ it is not an
//     idle-non-transit candidate, so no purchaser is chosen and no second nav is issued — just wait;
//   - no idle hull is free, or no home-system shipyard is known yet ⇒ retry a later tick.
//
// It NEVER buys and NEVER weakens the price guard — the reconciler still spends nothing while unreadable.
func (s *bootstrapShipyardScanner) EnsureHomeShipyardReadable(ctx context.Context, playerID int, homeSystem string) (bool, error) {
	if homeSystem == "" {
		return false, nil
	}
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return false, nil
	}
	yardWps, werr := s.waypointRepo.ListBySystemWithTrait(ctx, homeSystem, shipyardTrait)
	if werr != nil {
		return false, nil
	}
	isYard := map[string]struct{}{}
	dest := ""
	for _, wp := range yardWps {
		if wp == nil {
			continue
		}
		isYard[wp.Symbol] = struct{}{}
		if dest == "" {
			dest = wp.Symbol
		}
	}
	if dest == "" {
		return false, nil // no known home-system shipyard yet — retry once waypoint data arrives
	}

	ships, serr := s.shipRepo.FindAllByPlayer(ctx, pid)
	if serr != nil {
		return false, nil
	}
	var purchaser *navigation.Ship
	for _, sh := range ships {
		if loc := sh.CurrentLocation(); loc != nil {
			if _, ok := isYard[loc.Symbol]; ok && !sh.IsInTransit() {
				return false, nil // a hull is already present at a shipyard — the live price reads next tick
			}
		}
		// The purchaser must be free NOW (idle, not mid-flight); prefer the command frigate — the natural
		// cold-start buyer — over any other idle hull. A hull already en route to the yard is IN_TRANSIT,
		// so it is never re-selected here (that is the idempotency that prevents re-navigating each tick).
		if sh.IsIdle() && !sh.IsInTransit() && (purchaser == nil || sh.Role() == commandRole) {
			purchaser = sh
		}
	}
	if purchaser == nil {
		return false, nil // no free hull to send this tick (e.g. the last dispatch is still under way)
	}

	if _, nerr := s.med.Send(ctx, &navCmd.NavigateRouteCommand{ShipSymbol: purchaser.ShipSymbol(), Destination: dest, PlayerID: pid}); nerr != nil {
		return false, fmt.Errorf("navigate %s to home shipyard %s: %w", purchaser.ShipSymbol(), dest, nerr)
	}
	return true, nil
}

// --- metrics sink (adapts to the global bootstrap collector; pure observation, nil-safe) ---

type bootstrapMetricsSink struct{}

func (m *bootstrapMetricsSink) RecordPhase(phase string) {
	if c := metrics.GetGlobalBootstrapCollector(); c != nil {
		c.RecordPhase(phase)
	}
}

func (m *bootstrapMetricsSink) RecordProbePurchased() {
	if c := metrics.GetGlobalBootstrapCollector(); c != nil {
		c.RecordProbePurchased()
	}
}

func (m *bootstrapMetricsSink) RecordHaulerPurchased() {
	if c := metrics.GetGlobalBootstrapCollector(); c != nil {
		c.RecordHaulerPurchased()
	}
}

func (m *bootstrapMetricsSink) RecordConstructionPct(pct float64) {
	if c := metrics.GetGlobalBootstrapCollector(); c != nil {
		c.RecordConstructionPct(pct)
	}
}
