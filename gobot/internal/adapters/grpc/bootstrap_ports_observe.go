package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	ledgerQuery "github.com/andrescamacho/spacetraders-go/internal/application/ledger/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// bootstrapRefresher force-syncs the fleet from the API — the phantom-cache guard.
type bootstrapRefresher struct{ shipRepo navigation.ShipRepository }

func (r *bootstrapRefresher) RefreshFleet(ctx context.Context, playerID int) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}
	_, err = r.shipRepo.SyncAllFromAPI(ctx, pid)
	return err
}

// bootstrapObserver reads the reconciler's world snapshot: fleet shape, home coverage,
// treasury, and the contract workstream.
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
	observeFleetShape(ships, &obs)

	if !o.readTreasury(ctx, &obs) {
		return obs, nil
	}
	o.readHomeCoverage(ctx, playerID, &obs)
	o.readUntouredProbes(ctx, playerID, ships, &obs)
	o.readContractWorkstream(ctx, playerID, &obs)
	o.readGatePhase(ctx, playerID, &obs)

	obs.Readable = true
	return obs, nil
}

// observeFleetShape folds the hulls into probe counts, the command frigate's role and
// dedication, and the per-fleet tallies. The DedicatedFleet() tag is the restart-safe marker.
func observeFleetShape(ships []*navigation.Ship, obs *bootstrapCmd.Observation) {
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
		// A hull tagged "contract" that is the command frigate is NOT a hauler — it is the
		// retire target, tracked separately.
		if s.Role() == commandRole {
			obs.CommandFrigateID = s.ShipSymbol()
			obs.CommandFrigateOnContract = s.DedicatedFleet() == contractFleetTag
			// Empty cargo is the first-hauler-pivot safe point (no in-flight contract cargo to
			// lose on a loop stop); the pre-hauler loop must never restart on a purchasing frigate.
			obs.FrigateCargoEmpty = s.CargoUnits() == 0
			obs.CommandFrigatePurchasing = s.DedicatedFleet() == navigation.PurchasingFleet
			// Idle-and-not-flying is the honest free tick the re-dedication and the first-hauler pivot
			// both wait for (same expression as the gate-worker release below).
			obs.CommandFrigateOnTrade = s.DedicatedFleet() == tradeFleetTag
			obs.CommandFrigateIdle = s.IsIdle() && !s.IsInTransit()
			// Last run off the persisted assignment — the two fields the trade coordinator scores "unproductive" on.
			if a := s.Assignment(); a != nil && a.ReleasedAt() != nil {
				obs.CommandFrigateLastRunStart = a.AssignedAt()
				obs.CommandFrigateLastRunEnd = *a.ReleasedAt()
			}
		} else if s.DedicatedFleet() == contractFleetTag {
			obs.Haulers = append(obs.Haulers, bootstrapCmd.HaulerSnapshot{Symbol: s.ShipSymbol(), Waypoint: wp})
		} else if s.DedicatedFleet() == tradeFleetTag {
			obs.TradeHullCount++
		} else if gate.IsGateFleetTag(s.DedicatedFleet()) {
			// EVERY gate tag — the delivery role, the factory role, and the legacy one — is a
			// gate worker. This total is the worker-sizing "have" count, so a role-tagged hull
			// counted as nothing would under-report the workforce and let the staged top-up buy
			// past gateWorkerTarget. Appended in lock-step so len(GateWorkerHulls)==GateWorkers.
			obs.GateWorkers++
			obs.GateWorkerHulls = append(obs.GateWorkerHulls, bootstrapCmd.GateWorkerSnapshot{
				Symbol: s.ShipSymbol(),
				Idle:   s.IsIdle() && !s.IsInTransit(),
			})
		} else if s.DedicatedFleet() == warehouseFleetTag || s.DedicatedFleet() == stockerFleetTag {
			// The contract auto-scaler's DEPOT half. Delivery Haulers plus this depot count are the
			// FULL contract fleet the GATE-entry bar measures against ContractScalerTarget.
			obs.ContractDepotHullCount++
		}
	}
	obs.HomeSystem = commandHome
	if obs.HomeSystem == "" {
		obs.HomeSystem = anyHome
	}
}

// readTreasury reads the capital-gate input. No token or an unreadable agent fails closed:
// it stamps a Reason and reports false, and the caller must stop observing.
func (o *bootstrapObserver) readTreasury(ctx context.Context, obs *bootstrapCmd.Observation) bool {
	token, terr := common.PlayerTokenFromContext(ctx)
	if terr != nil {
		obs.Reason = "no player token in context"
		return false
	}
	agent, aerr := o.api.GetAgent(ctx, token)
	if aerr != nil || agent == nil {
		obs.Reason = fmt.Sprintf("agent credits unreadable: %v", aerr)
		return false
	}
	obs.Treasury = int64(agent.Credits)
	return true
}

// readHomeCoverage counts SCOUTABLE home-system marketplaces (FindAllMarketsInSystem — the same
// MARKETPLACE-minus-FUEL_STATION predicate scout-all-markets builds circuits from) against those
// with fresh data, so the ratio reaches 1.0 once scouting finishes the job it is designed to do. A
// FUEL_STATION stays out of both terms even when it holds data of its own (an incidental refuel-
// side-effect scan — still live via `market list`, just not what this signal measures). A read
// miss fails both counts closed to 0.
func (o *bootstrapObserver) readHomeCoverage(ctx context.Context, playerID int, obs *bootstrapCmd.Observation) {
	if obs.HomeSystem == "" {
		return
	}
	scoutable, serr := o.marketRepo.FindAllMarketsInSystem(ctx, obs.HomeSystem, playerID)
	if serr != nil {
		return
	}
	obs.MarketsTotal = len(scoutable)

	mkts, merr := o.marketRepo.ListMarketsInSystem(ctx, uint(playerID), obs.HomeSystem, bootstrapMarketFreshnessMin)
	if merr != nil {
		return
	}
	isScoutable := make(map[string]bool, len(scoutable))
	for _, wp := range scoutable {
		isScoutable[wp] = true
	}
	covered := 0
	for i := range mkts {
		if isScoutable[mkts[i].WaypointSymbol()] {
			covered++
		}
	}
	obs.MarketsCovered = covered
}

// readUntouredProbes counts the home probes that have never been given a circuit — the signal
// that separates a fleet that GREW from a tour that ENDED. BEST-EFFORT: an unreadable container
// table leaves the count 0, which reads as "the post is the right size" and touches nothing.
func (o *bootstrapObserver) readUntouredProbes(ctx context.Context, playerID int, ships []*navigation.Ship, obs *bootstrapCmd.Observation) {
	if o.containerRepo == nil || obs.HomeSystem == "" {
		return
	}
	toured, err := o.containerRepo.ListScoutTourShips(ctx, playerID)
	if err != nil {
		return
	}
	obs.ProbesUntoured = countUntouredHomeProbes(ships, obs.HomeSystem, toured)
}

// countUntouredHomeProbes counts the probes at home that are IDLE and carry no scout-tour
// history. Both qualifiers are load-bearing, because acting on this count re-partitions live
// tours and must terminate: a hull the tour will never be handed — someone else's claim
// (RULINGS #7), or one parked outside the in-system partition — would re-cut the post forever.
func countUntouredHomeProbes(ships []*navigation.Ship, homeSystem string, toured map[string]bool) int {
	untoured := 0
	for _, s := range ships {
		if s == nil || !s.IsScoutType() || !s.IsIdle() || toured[s.ShipSymbol()] {
			continue
		}
		if loc := s.CurrentLocation(); loc == nil || shared.ExtractSystemSymbol(loc.Symbol) != homeSystem {
			continue
		}
		untoured++
	}
	return untoured
}

// readContractWorkstream fills the contract half. Every read is BEST-EFFORT and the
// graduation flag fail-OPEN, so neither a miss nor a mis-wire kills the funding floor.
func (o *bootstrapObserver) readContractWorkstream(ctx context.Context, playerID int, obs *bootstrapCmd.Observation) {
	if o.eraRepo != nil {
		if graduated, gerr := o.eraRepo.IsContractGraduated(ctx, playerID); gerr == nil {
			obs.ContractGraduated = graduated
		}
	}

	obs.IncomePerHour = o.readIncomePerHour(ctx, playerID)

	// The era's FIXED delivery slots — the same set the auto-scaler buys against, resolved
	// from stationary home-system geometry, so placements do not churn as contracts turn over.
	if o.placement != nil {
		if slots, perr := o.placement.StandbyPlacement(ctx, playerID); perr == nil {
			obs.ContractPlacementSlots = slots
		}
	}

	if o.containerRepo == nil {
		return
	}
	if running, rerr := contractFleetCoordinatorRunning(ctx, o.containerRepo, playerID); rerr == nil {
		obs.BatchContractRunning = running
	}
	// SEPARATE from BatchContractRunning, which detects the coordinator TYPE rather than this
	// per-hull CONTRACT_WORKFLOW loop, so the contract action starts the loop exactly once.
	if obs.CommandFrigateID != "" {
		if running, rerr := frigateContractLoopRunning(ctx, o.containerRepo, playerID, obs.CommandFrigateID); rerr == nil {
			obs.FrigateContractLoopRunning = running
		}
	}
}

// readGatePhase fills the GATE half. Best-effort and fail-safe: an unknown gate site HOLDS
// gate, and an unread scaler target never enters it.
func (o *bootstrapObserver) readGatePhase(ctx context.Context, playerID int, obs *bootstrapCmd.Observation) {
	if o.server != nil {
		snap := o.server.readBootstrapGateSnapshot(ctx, obs.HomeSystem, playerID)
		obs.GateSite = snap.Site
		obs.ConstructionStarted = snap.Started
		obs.ConstructionComplete = snap.Complete
		obs.ConstructionPercent = snap.Percent
		obs.GateMaterialChains = snap.MaterialChain
		obs.ManufacturingAdopted = snap.Adopted
	}
	if o.containerRepo == nil {
		return
	}
	if running, rerr := containerTypeRunning(ctx, o.containerRepo, playerID, executorContainerTypes...); rerr == nil {
		obs.ManufacturingRunning = running
	}
	// The hand-off latch watches the standing fleet-GROWTH coordinator — what the hand-off actually
	// launches, and the fleet's only heavy buyer (sp-5pclx retired the autosizer this used to watch).
	if running, rerr := containerTypeRunning(ctx, o.containerRepo, playerID, container.ContainerTypeFleetGrowth); rerr == nil {
		obs.GrowthRunning = running
	}
	// min(scaler plan slots, the live contract_fleet_max_hulls ceiling). 0 when no scaler runs
	// or the target is unread — fail-closed, so gateFunded never enters GATE on an unknown target.
	obs.ContractScalerTarget = contractScalerTargetFor(ctx, o.containerRepo, playerID)
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
