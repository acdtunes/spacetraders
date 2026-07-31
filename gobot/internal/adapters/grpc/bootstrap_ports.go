package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	ledgerQuery "github.com/andrescamacho/spacetraders-go/internal/application/ledger/queries"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipyardCmd "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	domainShipyard "github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

// This file wires the captain bootstrap coordinator's concrete ports (sp-3nbe) to the daemon's live
// collaborators, mirroring fleet_autosizer_ports.go. The application-layer reconciler
// (bootstrapCmd.RunBootstrapCoordinatorHandler) owns the sequencing/gating/staging/recovery logic
// and is unit-tested against fakes; these adapters are the thin, real-service edges — reusing
// SyncAllFromAPI (the phantom-cache guard), GetAgent (treasury), shipyard list + BatchPurchaseShips
// (price-check + buy), and AddScoutPost (declare the home coverage post — sp-pt7d). It BUILDS nothing new.

const (
	// commandRole is the flagship's registration role — its system is the cold-start home system.
	commandRole = "COMMAND"
	// marketplaceTrait / shipyardTrait are the waypoint traits the coverage + price reads filter on.
	marketplaceTrait = "MARKETPLACE"
	shipyardTrait    = "SHIPYARD"
	// bootstrapMarketFreshnessMin bounds how old a market's data may be and still count as
	// "covered" for the heartbeat coverage reading. Generous (24h) because markets are actively scouted
	// during bootstrap, so coverage measures BREADTH (how many marketplaces have data), not
	// staleness. A tighter freshness window is a later refinement.
	bootstrapMarketFreshnessMin = 24 * 60
	// contractFleetTag is the dedicated-fleet tag the contract coordinator's dedicated pool selects on
	// (matches the contract package's dedicatedFleetContract). A hauler carrying it is adopted as a
	// contract worker (and puts the pool in exclusive mode, dropping the untagged frigate); the frigate
	// retire clears it. The income window (1h) is the trailing span the realized-$/hr read averages.
	contractFleetTag = "contract"
	// tradeFleetTag is the dedicated-fleet tag the standing trade-fleet coordinator selects on (matches the
	// trading package's tradeFleet and the autosizer's trade count). obs.TradeHullCount counts hulls carrying
	// it — the observable "trade-seeded" signal that drives the sp-192k4 trade-seed + the scaler
	// delay-launch. The bootstrap trade-seed (BuyAndDedicate) is what stamps a bought hull with this tag.
	tradeFleetTag = "trade"
	// warehouseFleetTag / stockerFleetTag are the dedicated-fleet tags the contract auto-scaler stamps on the
	// DEPOT half of the contract fleet — the central far-source warehouse hulls and the stocker
	// (container_ops_depot_launch.go). obs.ContractDepotHullCount counts hulls carrying either, mirroring how
	// obs.Haulers/TradeHullCount count by DedicatedFleet() tag; the delivery Haulers + this depot count are
	// the FULL contract fleet the sp-gm7r GATE-entry bar measures against the scaler's target.
	warehouseFleetTag     = "warehouse"
	stockerFleetTag       = "stocker"
	bootstrapIncomeWindow = time.Hour
	// bootstrapHomeScoutPostFreshness is the SEED freshness SLA stamped on the cold-start home scout
	// post (sp-pt7d). Transitional: the market-freshness sizer RESIZES the post's SLA + hull budget
	// once the home system enters the scanned census, so this only paces the FIRST scans. 1h mirrors
	// the sizer's baseline cadence.
	bootstrapHomeScoutPostFreshness = time.Hour
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
) *bootstrapCmd.RunBootstrapCoordinatorHandler {
	h := bootstrapCmd.NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&bootstrapRefresher{shipRepo: shipRepo})
	h.SetWorldObserver(&bootstrapObserver{
		api: apiClient, shipRepo: shipRepo, waypointRepo: waypointRepo, marketRepo: marketRepo,
		med: med, containerRepo: server.containerRepo, server: server,
		eraRepo: persistence.NewEraRepository(server.db),
		// The ramp places hulls on the SAME fixed delivery slots the contract coordinator homes them to —
		// one slot set for every positioning consumer, resolved from stationary home-system geometry.
		placement: NewContractStandbyPlacementProvider(shipRepo, waypointRepo, marketRepo),
	})
	// One acquirer instance drives both the probe buy and (embedded) the hauler price-check
	// + buy — the yard price-scan + batch-purchase plumbing is asset-agnostic (parameterised by shipType).
	// savedYards is where a yard's ask outlives the process that read it: the same era-scoped shipyard
	// inventory every scout scan writes, so a cold yard is still weighed against real evidence after a
	// daemon restart (RULINGS #2).
	acq := &bootstrapAcquirer{
		med: med, shipRepo: shipRepo, waypointRepo: waypointRepo,
		savedYards: persistence.NewShipyardInventoryRepository(server.db),
	}
	h.SetProbeAcquirer(acq)
	h.SetHaulerAcquirer(&bootstrapHaulerAcquirer{bootstrapAcquirer: acq})
	h.SetScoutPostDeclarer(&bootstrapScoutPostDeclarer{server: server})
	// The cold-start shipyard-readability scanner. On a fresh universe nothing has visited the home
	// shipyard, so its live (presence-gated) price is unreadable and the buy fails closed forever; this
	// flies a hull to the yard so the next tick's live PriceCheck reads. Same deps as the acquirer
	// (mediator navigate + ship/waypoint repos) — builds nothing new.
	h.SetShipyardScanner(&bootstrapShipyardScanner{med: med, shipRepo: shipRepo, waypointRepo: waypointRepo})
	h.SetFrigateRetirer(&bootstrapFrigateRetirer{shipRepo: shipRepo})
	h.SetContractRunner(&bootstrapContractRunner{server: server})
	// sp-rype: the pre-hauler frigate sole-earner contract loop (sp-ehg9 batch-contract --loop). After the
	// frigate finishes its hour-0 shipyard run + probe buy it runs contracts as the sole earner instead of
	// parking idle at the yard — the fix for the cold-start income stall.
	h.SetFrigateContractLoopStarter(&bootstrapFrigateContractLoop{server: server})
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
	// sp-mxflh: un-dedicate surplus idle gate workers → idle pool so the contract scaler adopts them (zero buys).
	h.SetGateSurplusReleaser(&bootstrapGateSurplusReleaser{shipRepo: shipRepo})
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
	// Contract-workstream reads (Slice 2). med runs the realized-$/hr ledger query; placement resolves the
	// era's fixed delivery slots; containerRepo answers "is batch-contract running?".
	med           common.Mediator
	placement     appContract.StandbyPlacementProvider
	containerRepo *persistence.ContainerRepositoryGORM
	// GATE-phase reads (Slice 3). server runs the construction-site discovery + status snapshot and the
	// executor/autosizer container-running checks. All best-effort (a miss leaves the field zero-valued).
	server *DaemonServer
	// eraRepo reads the durable per-player era-scoped contract-graduation flag (sp-difa.1) — the SAME
	// read the capacity reconciler consults. Best-effort: a nil repo or read error leaves ContractGraduated
	// false (fail-OPEN — contracts run as today), so a mis-wire never silently kills the funding floor.
	eraRepo *persistence.EraRepository
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
		// Contract fleet signals (Slice 2): the command frigate (retire target) and the contract-dedicated
		// haulers (the staged-buy count + placement guard). A hull tagged "contract" that is the command
		// frigate is NOT a hauler — it is the retire target, tracked separately.
		if s.Role() == commandRole {
			obs.CommandFrigateID = s.ShipSymbol()
			obs.CommandFrigateOnContract = s.DedicatedFleet() == contractFleetTag
			// sp-7r7w: the first-hauler-pivot safe point (empty ⇒ no in-flight contract cargo to lose on a
			// loop stop) and the exclusive-purchasing-ship signal (the pre-hauler loop must never restart on
			// a purchasing-dedicated frigate; the buy paths resolve the purchaser to it).
			obs.FrigateCargoEmpty = s.CargoUnits() == 0
			obs.CommandFrigatePurchasing = s.DedicatedFleet() == navigation.PurchasingFleet
		} else if s.DedicatedFleet() == contractFleetTag {
			obs.Haulers = append(obs.Haulers, bootstrapCmd.HaulerSnapshot{Symbol: s.ShipSymbol(), Waypoint: wp})
		} else if s.DedicatedFleet() == tradeFleetTag {
			// sp-192k4: a hull dedicated to the trade fleet is the observable "trade-seeded" signal. Counted
			// here mirroring obs.Haulers (same ship set, same DedicatedFleet() source), filtering the "trade"
			// tag instead of "contract" — drives the trade-seed routing + the contract-scaler
			// delay-launch. Restart-safe by construction: the hull EXISTING is the marker (no stored flag).
			obs.TradeHullCount++
		} else if s.DedicatedFleet() == manufacturingFleetTag {
			// A hull dedicated to the manufacturing fleet is a gate-construction worker (Slice 3) — the
			// worker-sizing "have" count, so the staged top-up buy never overshoots the pipeline's shape.
			// GateWorkerHulls carries the per-hull detail + idle status (sp-mxflh) the surplus-release
			// selection reads — appended in lock-step with the count so len(GateWorkerHulls)==GateWorkers.
			obs.GateWorkers++
			obs.GateWorkerHulls = append(obs.GateWorkerHulls, bootstrapCmd.GateWorkerSnapshot{
				Symbol: s.ShipSymbol(),
				Idle:   s.IsIdle() && !s.IsInTransit(),
			})
		} else if s.DedicatedFleet() == warehouseFleetTag || s.DedicatedFleet() == stockerFleetTag {
			// sp-gm7r: a hull dedicated to the contract auto-scaler's DEPOT half (warehouse or stocker). Counted
			// here mirroring obs.Haulers/TradeHullCount (same ship set, same DedicatedFleet() source) — the
			// delivery Haulers + this depot count are the FULL contract fleet the GATE-entry bar measures
			// against ContractScalerTarget.
			obs.ContractDepotHullCount++
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
	// either leaves that count 0, which simply reads as uncovered on the heartbeat.
	if obs.HomeSystem != "" {
		if wps, werr := o.waypointRepo.ListBySystemWithTrait(ctx, obs.HomeSystem, marketplaceTrait); werr == nil {
			obs.MarketsTotal = len(wps)
		}
		if mkts, merr := o.marketRepo.ListMarketsInSystem(ctx, uint(playerID), obs.HomeSystem, bootstrapMarketFreshnessMin); merr == nil {
			obs.MarketsCovered = len(mkts)
		}
	}

	// Contract graduation (sp-difa.1): the durable per-player era-scoped flag that gates the whole
	// contract-income workstream. Best-effort + fail-OPEN — a nil repo or read error leaves it false, so
	// contracts run as today (a mis-wire never silently kills the funding floor). This is the SAME read
	// the capacity reconciler consults, so both coordinators honor one operator decision.
	if o.eraRepo != nil {
		if graduated, gerr := o.eraRepo.IsContractGraduated(ctx, playerID); gerr == nil {
			obs.ContractGraduated = graduated
		}
	}

	// Contract-workstream reads (Slice 2). Each is BEST-EFFORT: a miss leaves the field at its zero value,
	// which the reconciler reads fail-safe — 0 $/hr never advances the arc (never premature GATE), and an
	// empty slot set means no placement target (no hauler buys).
	obs.IncomePerHour = o.readIncomePerHour(ctx, playerID)
	// The era's FIXED delivery placement slots — the SAME ≤6 set the contract auto-scaler buys against and
	// the contract coordinator's homing zips hulls onto, so the ramp drops each hull where the standing op
	// will keep it. Resolved from stationary home-system geometry + market roles, never from the live
	// contract's goods, so the ramp's placements do not churn as contracts turn over.
	if o.placement != nil {
		if slots, perr := o.placement.StandbyPlacement(ctx, playerID); perr == nil {
			obs.ContractPlacementSlots = slots
		}
	}
	if o.containerRepo != nil {
		if running, rerr := contractFleetCoordinatorRunning(ctx, o.containerRepo, playerID); rerr == nil {
			obs.BatchContractRunning = running
		}
		// sp-rype earner-signal: is the command frigate's OWN continuous contract loop running? SEPARATE
		// from BatchContractRunning (which detects the contract_fleet_coordinator TYPE, not this per-hull
		// CONTRACT_WORKFLOW loop — sp-ehg9 note), so the contract action starts the loop exactly once and
		// never double-claims. Best-effort + only when the frigate is resolved: a read miss leaves it
		// false, and the daemon's per-player single-CONTRACT_WORKFLOW guard rejects any redundant start.
		if obs.CommandFrigateID != "" {
			if running, rerr := frigateContractLoopRunning(ctx, o.containerRepo, playerID, obs.CommandFrigateID); rerr == nil {
				obs.FrigateContractLoopRunning = running
			}
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
		// sp-gm7r GATE-entry bar input: the contract auto-scaler's live achievable fleet target
		// (min(scaler plan slots, the scaler's live contract_fleet_max_hulls ceiling)). 0 when no scaler is
		// running or the target is unread — fail-closed, so gateFunded never enters GATE on an unknown target.
		obs.ContractScalerTarget = contractScalerTargetFor(ctx, o.containerRepo, playerID)
	}

	obs.Readable = true
	return obs, nil
}

// readIncomePerHour reads the player's realized NET credits over the trailing income window (reusing
// the ledger GetProfitLoss query) — the heartbeat's realized-earnings reading. Realized (booked ledger
// rows), not projected. A read miss returns 0, which drives no decision (it gates nothing).
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

// frigateContractLoopRunning reports whether the command frigate's OWN continuous single-hull contract
// loop is RUNNING or PENDING for the player — the sp-rype earner-signal the bootstrap contract action
// guards on (so it starts the loop exactly once and never double-claims). It is the loop container
// sp-ehg9 creates: a CONTRACT_WORKFLOW container with ship_symbol==frigate AND iterations==-1. Matching
// BOTH is what distinguishes it from a coordinator-spawned single-shot worker (iterations 1, on a
// hauler); obs.BatchContractRunning cannot see it because that detects the coordinator TYPE, not this
// per-hull loop (sp-ehg9 note). Mirrors contractFleetCoordinatorRunning's PENDING+RUNNING scan.
// findFrigateContractLoopID returns the container ID of the command frigate's continuous single-hull
// contract loop (a CONTRACT_WORKFLOW with iterations=-1, the sp-ehg9 batch-contract --loop) if one is
// running or pending, else "". The earner-signal reader (frigateContractLoopRunning) and the pivot
// stopper (StopLoop) both resolve the loop the same way, so they can never disagree.
func findFrigateContractLoopID(ctx context.Context, repo *persistence.ContainerRepositoryGORM, playerID int, frigateSymbol string) (string, error) {
	for _, st := range []container.ContainerStatus{container.ContainerStatusRunning, container.ContainerStatusPending} {
		models, err := repo.ListByStatus(ctx, st, &playerID)
		if err != nil {
			return "", err
		}
		for _, m := range models {
			if m.ContainerType != string(container.ContainerTypeContractWorkflow) {
				continue
			}
			cfg := map[string]interface{}{}
			if m.Config != "" {
				if json.Unmarshal([]byte(m.Config), &cfg) != nil {
					continue
				}
			}
			ship, _ := cfg["ship_symbol"].(string)
			iters, _ := intValue(cfg["iterations"])
			if ship == frigateSymbol && iters == -1 {
				return m.ID, nil
			}
		}
	}
	return "", nil
}

func frigateContractLoopRunning(ctx context.Context, repo *persistence.ContainerRepositoryGORM, playerID int, frigateSymbol string) (bool, error) {
	id, err := findFrigateContractLoopID(ctx, repo, playerID, frigateSymbol)
	return id != "", err
}

// --- probe acquirer (shipyard list price-check + BatchPurchaseShips) ---

type bootstrapAcquirer struct {
	med          common.Mediator
	shipRepo     navigation.ShipRepository
	waypointRepo waypointTraitLister
	savedYards   savedYardReader

	// lastAsks caches the most recent priced reading per player+ship type so a cold yard is answered
	// without a store read. It is an accelerator over savedYards, never the record itself — it starts
	// empty in every process. One instance backs the probe, hauler and gate-worker acquirers and serves
	// every player, hence the mutex.
	lastAskMu sync.Mutex
	lastAsks  map[askKey]int64
}

// waypointTraitLister lists a system's waypoints carrying a trait — the shipyard search the price-check
// walks.
type waypointTraitLister interface {
	ListBySystemWithTrait(ctx context.Context, systemSymbol, trait string) ([]*shared.Waypoint, error)
}

// savedYardReader is the persisted, era-scoped shipyard inventory every scanned yard writes: the durable
// record of what a yard charged, cheapest first. Era scoping is the correct forgetting — a universe reset
// retires the old asks along with the old universe.
type savedYardReader interface {
	ListSavedYards(ctx context.Context, playerID int, shipTypes []string) ([]domainShipyard.ShipTypeAvailability, error)
}

type askKey struct {
	playerID int
	shipType string
}

func (a *bootstrapAcquirer) rememberAsk(playerID int, shipType string, price int64) {
	a.lastAskMu.Lock()
	defer a.lastAskMu.Unlock()
	if a.lastAsks == nil {
		a.lastAsks = map[askKey]int64{}
	}
	a.lastAsks[askKey{playerID, shipType}] = price
}

// lastAsk reports what a yard last charged for shipType, 0 only when none ever has. The cache answers
// first; the persisted inventory answers whenever it cannot, so a reading outlives the process that took
// it (RULINGS #2). That distinction is load-bearing: callers read 0 as "no yard has ever priced this, so
// there is no evidence to act on", and a reading that died with a process would read as an absence of
// yards rather than an absence of memory.
func (a *bootstrapAcquirer) lastAsk(ctx context.Context, playerID int, shipType string) int64 {
	if cached := a.cachedAsk(playerID, shipType); cached > 0 {
		return cached
	}
	return a.savedAsk(ctx, playerID, shipType)
}

func (a *bootstrapAcquirer) cachedAsk(playerID int, shipType string) int64 {
	a.lastAskMu.Lock()
	defer a.lastAskMu.Unlock()
	return a.lastAsks[askKey{playerID, shipType}]
}

// savedAsk returns the cheapest ask on record for shipType — the same cheapest-reachable-yard reading
// PriceCheck computes live. Rows arrive price-ascending, and a 0 price marks a type that was listed but
// carried no priced listing at scan time, so the first POSITIVE price is the cheapest real ask.
func (a *bootstrapAcquirer) savedAsk(ctx context.Context, playerID int, shipType string) int64 {
	if a.savedYards == nil {
		return 0
	}
	rows, err := a.savedYards.ListSavedYards(ctx, playerID, []string{shipType})
	if err != nil {
		common.LoggerFromContext(ctx).Log("ERROR", fmt.Sprintf("Bootstrap could not read the %s asks on record — this tick weighs no yard evidence: %v", shipType, err), map[string]interface{}{
			"action":    "bootstrap_saved_ask_read_error",
			"player_id": playerID,
			"ship_type": shipType,
		})
		return 0
	}
	for _, row := range rows {
		if row.PurchasePrice > 0 {
			return int64(row.PurchasePrice)
		}
	}
	return 0
}

// PriceCheck finds the cheapest priced listing for shipType at a SHIPYARD-trait waypoint in a system
// where the player operates. readable=false (capital gate fails closed) when no priced listing is
// found — a yard prices its hulls only while a ship is standing at it, so an unvisited one reads cold.
// An unreadable read returns the LAST ask this yard gave for shipType (0 when it never has): the only
// evidence available while the yard is cold, and evidence for policy alone — every buy path gates on
// readable before it spends.
func (a *bootstrapAcquirer) PriceCheck(ctx context.Context, playerID int, shipType string) (int64, string, bool, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return a.lastAsk(ctx, playerID, shipType), "", false, nil
	}
	ships, err := a.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return a.lastAsk(ctx, playerID, shipType), "", false, nil
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
		return a.lastAsk(ctx, playerID, shipType), "", false, nil
	}
	a.rememberAsk(playerID, shipType, cheapest)
	return cheapest, cheapestYard, true, nil
}

func (a *bootstrapAcquirer) priceAtShipyard(ctx context.Context, system, waypoint, shipType string, pid shared.PlayerID) (int64, bool) {
	// The bootstrap capital gate spends against the winning price this search
	// returns, with no later re-verification, so the read stays Earning: metered,
	// never denied (RULINGS #4).
	//
	// NOTE FOR THE NEXT READER: this is called in a LOOP over every SHIPYARD-trait
	// waypoint in every system the fleet occupies (see PriceCheck), so one call
	// costs one undeniable live read PER YARD, and two of PriceCheck's own callers
	// discard the result entirely — they are documented read-only. That residual
	// is the largest remaining Earning-class consumer of the shipyard allowance;
	// it is left alone here because fixing it means changing WHERE the capital
	// gate gets its price (rank on the store, then take ONE live read at the
	// winner), which needs its own money-guard analysis. It is now MEASURABLE:
	// scan_budget_decisions_total{budget="shipyard",class="earning"}.
	q := &shipyardQueries.GetShipyardListingsQuery{SystemSymbol: system, WaypointSymbol: waypoint, PlayerID: pid, Class: marketscan.Earning}
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
// It picks ANY idle hull as the purchaser (the probe-buy default, where the frigate/probes are idle).
func (a *bootstrapAcquirer) Buy(ctx context.Context, playerID int, shipType, yard string) (bootstrapCmd.BuyResult, error) {
	return a.buyWith(ctx, playerID, shipType, yard, "")
}

// buyWith purchases ONE shipType at yard using `purchaser` as the purchasing hull. purchaser=="" keeps
// the legacy behavior (scan for any idle hull); a set value PINS the purchaser — the sp-7r7w first-hauler
// pivot and every subsequent cold-start buy pass the exclusive purchasing frigate, so the buy is
// deterministic rather than dependent on an incidentally-idle hull. The batch path still enforces the
// sp-e7je money-integrity type guard and navigates the purchaser to the yard.
func (a *bootstrapAcquirer) buyWith(ctx context.Context, playerID int, shipType, yard, purchaser string) (bootstrapCmd.BuyResult, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return bootstrapCmd.BuyResult{}, err
	}
	// One roster read serves BOTH the search and the ownership guard below. It was
	// previously taken only on the search path, which is exactly why a NAMED purchaser
	// was never checked against anything.
	ships, err := a.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return bootstrapCmd.BuyResult{}, err
	}
	if purchaser == "" {
		// sp-7r7w: PREFER the exclusive purchasing ship (the pivoted command frigate) when it is idle, so
		// every cold-start + scaling buy runs through the deterministic, protected buy ship rather than an
		// incidentally-idle hull. Fall back to any idle hull before the pivot exists (e.g. the probe
		// buy) or if the purchasing ship is momentarily busy.
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
			return bootstrapCmd.BuyResult{}, fmt.Errorf("no idle hull available to execute the purchase")
		}
	}

	// OWNERSHIP GATE (RULINGS #3 single-writer, #7 "do not code around ... the claim tx").
	// The buy below runs IN-PROCESS through the mediator — BatchPurchaseShipsCommand ->
	// PurchaseShipCommand -> NavigateRouteCommand/DockShipCommand — and NOT ONE of those
	// handlers consults the ship claim. Without this gate a buy issued against a hull
	// another container is actively running flies it to a shipyard and docks it out from
	// under the live worker, and nothing anywhere reports it: the loud "already assigned"
	// collision the CONTAINER claim path raises has a silent twin on this path, and a
	// silent one never appears in any failure count.
	//
	// The search path above already refuses a held hull (it only takes s.IsIdle(), which is
	// false for anything ClaimShip'd — AssignToContainer makes the hull IsAssigned). The gap
	// was the NAMED purchaser, which skipped the search and every check in it. Applied to
	// BOTH paths so a future caller cannot reopen the hole, and it is a pure REFUSAL: it can
	// only prevent a spend, never enable one, so no money guard is loosened (RULINGS #4).
	//
	// Fails CLOSED on a purchaser absent from the roster: ownership that cannot be read
	// cannot be cleared, and a hull we cannot see is one we must not fly.
	if err := ownedByAnotherContainer(ships, purchaser); err != nil {
		return bootstrapCmd.BuyResult{}, err
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

// ownedByAnotherContainer reports the buy-blocking reason when `purchaser` is not the
// bootstrap's to fly: a hull carrying a live container claim (a running workflow, or a
// captain reservation — both are ACTIVE assignments) has another writer, and a hull the
// roster does not carry cannot be shown to be free. nil means the hull is unowned and the
// buy may proceed. Read-only: it never releases, never clobbers, and never retries — a
// contested hull simply does not get bought with this tick, and the next tick re-derives
// the answer from durable state (RULINGS #2).
func ownedByAnotherContainer(ships []*navigation.Ship, purchaser string) error {
	for _, s := range ships {
		if s == nil || s.ShipSymbol() != purchaser {
			continue
		}
		if s.IsAssigned() {
			return fmt.Errorf("purchaser %s is assigned to container %q — refusing to fly a hull another writer owns", purchaser, s.ContainerID())
		}
		return nil
	}
	return fmt.Errorf("purchaser %s is not in the fleet roster — refusing to fly a hull whose ownership cannot be read", purchaser)
}

// --- hauler acquirer (reuse the probe price-check + buy, then dedicate + place on the hub) ---

// bootstrapHaulerAcquirer embeds the probe acquirer to reuse its cheapest-yard PriceCheck and the
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
func (a *bootstrapHaulerAcquirer) BuyAndPlace(ctx context.Context, playerID int, shipType, yard, hubWaypoint, purchaserSymbol string) (bootstrapCmd.BuyResult, error) {
	bought, err := a.buyWith(ctx, playerID, shipType, yard, purchaserSymbol)
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

// BuyAndDedicate buys ONE hull at yard (reused batch path) and dedicates it to the arbitrary fleet tag
// `fleet` — the fleet-parameterized sibling of BuyAndPlace, minus the hub placement (sp-192k4). The
// hull-routing trade-seed calls it with fleet="trade" to make acquisition #2 a trade hull; the dedication
// uses the SAME single fleet-assign write path (shipRepo.AssignFleet) BuyAndPlace uses over the SAME
// money-integrity BatchPurchaseShips buy, so no money guard is touched (RULINGS #4). NO navigate: a trade
// hull runs the continuous tours the trade-fleet coordinator assigns, not a fixed contract hub.
func (a *bootstrapHaulerAcquirer) BuyAndDedicate(ctx context.Context, playerID int, shipType, yard, fleet, purchaserSymbol string) (bootstrapCmd.BuyResult, error) {
	bought, err := a.buyWith(ctx, playerID, shipType, yard, purchaserSymbol)
	if err != nil {
		return bootstrapCmd.BuyResult{}, err
	}
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return bought, err
	}
	if derr := a.shipRepo.AssignFleet(ctx, bought.ShipSymbol, fleet, pid); derr != nil {
		return bought, fmt.Errorf("dedicate hull %s to %q fleet: %w", bought.ShipSymbol, fleet, derr)
	}
	return bought, nil
}

// --- frigate retirer (clear the command frigate's contract dedication — fleet unassign) ---

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

// DedicateAsPurchaser tags the frigate dedicated_fleet="purchasing" at the first-hauler pivot (sp-7r7w),
// reserving it as the EXCLUSIVE, protected buy ship. Reuses the single fleet-assign write path
// (shipRepo.AssignFleet); the contract-op selection paths skip a purchasing-dedicated hull like any
// foreign dedication (RULINGS #7), so it is never re-drafted while it stands by between buys.
func (r *bootstrapFrigateRetirer) DedicateAsPurchaser(ctx context.Context, playerID int, shipSymbol string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}
	return r.shipRepo.AssignFleet(ctx, shipSymbol, navigation.PurchasingFleet, pid)
}

// --- contract runner (launch the contract fleet coordinator — workflow batch-contract) ---

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

// --- frigate contract-loop starter (sp-rype: the pre-hauler sole-earner loop, sp-ehg9 primitive) ---

type bootstrapFrigateContractLoop struct{ server *DaemonServer }

// StartLoop puts the command frigate on a continuous single-hull contract loop
// (server.BatchContractWorkflow with iterations=-1 — the sp-ehg9 batch-contract --loop). The daemon's
// per-player single-CONTRACT_WORKFLOW guard (CreateIfNoActiveWorker) makes a duplicate start a benign
// no-op, so an "already running" error is swallowed as success — the reconciler already gates on the
// obs.FrigateContractLoopRunning earner-signal, and this only trips on the rare observation-lag race.
// The loop CLAIMS the frigate via the container runner (IsAssigned), NOT the "contract" fleet tag, so it
// never collides with the frigate-retire (which clears that tag) and the coordinator can never
// double-claim the frigate while the loop holds it (RULINGS #7).
func (r *bootstrapFrigateContractLoop) StartLoop(ctx context.Context, playerID int, frigateSymbol string) error {
	if _, err := r.server.BatchContractWorkflow(ctx, frigateSymbol, playerID, -1); err != nil {
		if strings.Contains(err.Error(), "already running") {
			return nil
		}
		return err
	}
	return nil
}

// StopLoop stops the command frigate's continuous contract-loop container (sp-7r7w first-hauler pivot):
// it finds the frigate's CONTRACT_WORKFLOW (iterations=-1) container and StopContainer's it, which
// gracefully cancels the loop goroutine and RELEASES the frigate's work-claim so it goes idle to serve
// as the purchaser. Idempotent: no loop found ⇒ nil (the frigate is already free). The reconciler gates
// the pivot on obs.FrigateContractLoopRunning, so StopLoop is invoked only when a loop is observed.
func (r *bootstrapFrigateContractLoop) StopLoop(ctx context.Context, playerID int, frigateSymbol string) error {
	id, err := findFrigateContractLoopID(ctx, r.server.containerRepo, playerID, frigateSymbol)
	if err != nil {
		return err
	}
	if id == "" {
		return nil
	}
	return r.server.StopContainer(id)
}

// --- scout-post declarer (declare the home COVERAGE target; the boot-standing scout-post
// coordinator sp-9ujl mans it by claiming an idle probe — sp-pt7d) ---

type bootstrapScoutPostDeclarer struct{ server *DaemonServer }

// DeclareHomeScoutPost ensures a STANDING scout post exists for the home system so the boot-standing
// scout-post coordinator (sp-9ujl) has a coverage target to man, and stamps its permanent manning
// FLOOR = minHulls (probeTarget, sp-2ci9y). It assigns/dedicates NO probe; the coordinator claims an
// idle one. hulls=1: one probe slot initially; the freshsizer resizes the budget once the system
// enters the scanned census (but never below the floor). Replaces the old AssignScoutingFleet sweep
// that HELD the probes.
//
// The post ROW is declared ONCE (re-Upsert every tick would churn the coordinator's manning and
// the freshsizer's Hulls resize). The floor is stamped through the NARROW min_hulls-only seam
// (UpdateScoutPostMinHulls) — a DISJOINT column from hulls, so it disturbs neither writer — and is
// idempotent: it fires once on a fresh or pre-floor post, then the equality guard makes it a no-op,
// so a mid-era deploy also gets the floor without per-tick writes.
func (s *bootstrapScoutPostDeclarer) DeclareHomeScoutPost(ctx context.Context, playerID int, system string, minHulls int) error {
	existing, err := s.server.ListScoutPosts(ctx, playerID)
	if err != nil {
		return fmt.Errorf("list scout posts: %w", err)
	}
	var home *domainScouting.ScoutPost
	for _, p := range existing {
		if p.SystemSymbol == system {
			home = p
			break
		}
	}
	if home == nil {
		if _, err := s.server.AddScoutPost(ctx, playerID, system, bootstrapHomeScoutPostFreshness, domainScouting.PostKindStanding, 1); err != nil {
			return err
		}
	}
	// Stamp the permanent home floor iff it is not already the desired value — leaving the
	// coordinator's/freshsizer's live state untouched in the steady state.
	if minHulls > 0 && (home == nil || home.MinHulls != minHulls) {
		return s.server.UpdateScoutPostMinHulls(ctx, playerID, system, minHulls)
	}
	return nil
}

// --- shipyard scanner (send a hull to the home yard so the cold price reads) ---

type bootstrapShipyardScanner struct {
	med          common.Mediator
	shipRepo     navigation.ShipRepository
	waypointRepo *persistence.GormWaypointRepository
}

// EnsureShipyardReadable sends a hull to a home-system SHIPYARD waypoint so the next tick's live
// GetShipyard (bootstrapAcquirer.PriceCheck) returns priced listings. The SpaceTraders shipyard ship
// listing is PRESENCE-GATED — empty unless a hull is at the waypoint — so on a fresh universe the price
// is unreadable until something visits the yard. The trip reuses NavigateRouteCommand, the same
// high-level route+refuel path BuyAndPlace uses; presence (in orbit) is enough for the listing to read,
// and the buy path docks.
//
// Idempotent + best-effort (returns dispatched=false, nil rather than churn): a hull already standing at
// a yard means the price reads next tick; a hull already IN_TRANSIT is an earlier dispatch still under
// way; and no free hull or no known home shipyard just retries a later tick.
//
// It NEVER buys and NEVER weakens the price guard — the reconciler still spends nothing while unreadable.
func (s *bootstrapShipyardScanner) EnsureShipyardReadable(ctx context.Context, playerID int, homeSystem, purchaser string) (bool, error) {
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
	send, ok := hullToSend(ships, isYard, purchaser)
	if !ok {
		return false, nil
	}

	if _, nerr := s.med.Send(ctx, &navCmd.NavigateRouteCommand{ShipSymbol: send, Destination: dest, PlayerID: pid}); nerr != nil {
		return false, fmt.Errorf("navigate %s to home shipyard %s: %w", send, dest, nerr)
	}
	return true, nil
}

// hullToSend picks the hull to send to the yard, or reports that nothing should move this tick.
//
// A NAMED purchaser is already committed to the buy, so it goes on its own account: it is sent even
// though its purchasing dedication puts it outside the free-hull search, and it goes without waiting to
// read idle — the pivot released its loop-claim moments ago and that may not have propagated yet. Only
// its own position excuses the trip: standing at a yard means the price reads next tick, and being
// mid-flight means an earlier dispatch is still under way.
//
// With no purchaser named, the search takes a genuinely FREE hull that no other controller owns
// (RULINGS #7 — the seed→sustain handoff must never double-claim): idle (IsIdle is false for a hull
// ClaimShip'd by the contract engine — AssignToContainer makes it IsAssigned), not mid-flight, and
// dedicated to no fleet, so a contract hauler or mfg worker that is momentarily idle is never poached.
// It prefers the command frigate, the natural cold-start buyer, and stands down entirely once any hull
// is already at a yard.
func hullToSend(ships []*navigation.Ship, isYard map[string]struct{}, purchaser string) (string, bool) {
	atYard := func(sh *navigation.Ship) bool {
		loc := sh.CurrentLocation()
		if loc == nil {
			return false
		}
		_, ok := isYard[loc.Symbol]
		return ok && !sh.IsInTransit()
	}

	if purchaser != "" {
		for _, sh := range ships {
			if sh.ShipSymbol() != purchaser {
				continue
			}
			if atYard(sh) || sh.IsInTransit() {
				return "", false
			}
			// OWNERSHIP GATE (RULINGS #3 single-writer, #7 claim tx). Being committed to a
			// buy excuses this hull from the FREE-hull search above; it does not make it
			// ours to fly. A live container claim means another writer is running it, and
			// this navigate leg — like the buy it precedes — consults no claim of its own,
			// so without this the daemon flies a hull out from under a running workflow and
			// reports nothing. Declining costs nothing: EnsureShipyardReadable is
			// idempotent and best-effort, so the trip simply happens on a later tick once
			// the claim clears (RULINGS #2 — re-derived, never remembered).
			if sh.IsAssigned() {
				return "", false
			}
			return purchaser, true
		}
		// Fail CLOSED on a purchaser the roster does not carry: ownership that cannot be
		// read cannot be cleared.
		return "", false
	}

	var free *navigation.Ship
	for _, sh := range ships {
		if atYard(sh) {
			return "", false
		}
		if sh.IsIdle() && !sh.IsInTransit() && sh.DedicatedFleet() == "" && (free == nil || sh.Role() == commandRole) {
			free = sh
		}
	}
	if free == nil {
		return "", false
	}
	return free.ShipSymbol(), true
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
