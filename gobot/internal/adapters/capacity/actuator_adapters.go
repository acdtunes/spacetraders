package capacity

// Production adapters that wrap the Actuator's primitive ports around the
// EXISTING primitives (bead st-5ig). Each is a thin translation — the actuation
// logic stays in the primitive it drives:
//   - reassign   -> the fleet-assign mediator command (single dedication write path)
//   - reposition -> the navigate-route mediator command
//   - workers    -> ensure the standing sp-f5pr worker-rebalancer runs (it owns the moves)
//   - buffer     -> the depot warehouse supported-goods whitelist writer (sp-94du)
//   - player     -> the reconciling player resolved from the ambient auth token
//
// These are ADAPTERS (hexagonal driven side): they are exercised by integration
// tests against the real primitives, never unit-tested with mocks of the
// infrastructure they wrap (the Actuator's own dispatch is unit-tested against
// these ports in actuator_test.go).

import (
	"context"
	"fmt"
	"sync"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipAssignment "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/assignment"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// capacityReconcilerAssigner is the assigner-audit tag every reconciler
// dedication write carries (sp-r6f1) so a mispin names its culprit in one grep.
const capacityReconcilerAssigner = "capacity-reconciler"

// ---- tier 1: reassign via the single fleet-assign write path ----------------

type mediatorReassigner struct{ mediator common.Mediator }

// NewMediatorReassigner drives tier-1 reassigns through the AssignShipFleet
// mediator command — the single dedication write path (with its cargo-eligibility
// gate + assigner audit line intact).
func NewMediatorReassigner(mediator common.Mediator) HullReassigner {
	return &mediatorReassigner{mediator: mediator}
}

func (r *mediatorReassigner) ReassignHull(ctx context.Context, playerID shared.PlayerID, shipSymbol, fleet string) error {
	pid := playerID.Value()
	_, err := r.mediator.Send(ctx, &shipAssignment.AssignShipFleetCommand{
		ShipSymbol: shipSymbol,
		Fleet:      fleet,
		PlayerID:   &pid,
		Assigner:   capacityReconcilerAssigner,
		Manual:     false, // automated path: the eligibility gate fails closed
	})
	return err
}

// ---- tier 2 reposition: the navigate-route mediator command -----------------

type mediatorRepositioner struct{ mediator common.Mediator }

// NewMediatorRepositioner drives tier-2 repositions through the NavigateRoute
// mediator command (RouteExecutor-backed travel).
func NewMediatorRepositioner(mediator common.Mediator) HullRepositioner {
	return &mediatorRepositioner{mediator: mediator}
}

func (r *mediatorRepositioner) RepositionHull(ctx context.Context, playerID shared.PlayerID, shipSymbol, destinationWaypoint string) error {
	_, err := r.mediator.Send(ctx, &navCmd.NavigateRouteCommand{
		ShipSymbol:  shipSymbol,
		Destination: destinationWaypoint,
		PlayerID:    playerID,
	})
	return err
}

// ---- tier 2 workers: ensure the standing worker-rebalancer runs -------------
//
// The differ leaves rebalance_workers without a source hull on purpose — "the
// worker-rebalancer primitive owns the actual moves" (ladder.go). So the
// reconciler's action ENSURES the standing sp-f5pr worker-rebalancer is running
// for the player (the exact ensure-running the bootstrap gate uses); the
// rebalancer then autonomously ferries workers toward its vacancies. Idempotent:
// an already-running rebalancer is a no-op.

// runningContainerLister lists a player's live containers (satisfied by
// *persistence.ContainerRepositoryGORM) — the running-check without importing grpc.
type runningContainerLister interface {
	ListByStatusSimple(ctx context.Context, status string, playerID *int) ([]persistence.ContainerSummary, error)
}

// workerRebalancerLauncher launches the standing worker-rebalancer (satisfied by
// *grpc.DaemonServer structurally — no grpc import needed).
type workerRebalancerLauncher interface {
	WorkerRebalancerCoordinator(ctx context.Context, playerID int, agentSymbol string, dryRun bool) (string, error)
}

type workerRebalanceEnsurer struct {
	containers runningContainerLister
	launcher   workerRebalancerLauncher
	players    player.PlayerRepository
}

// NewWorkerRebalanceEnsurer wires the tier-2 worker-rebalance actuation to the
// standing worker-rebalancer's ensure-running seam.
func NewWorkerRebalanceEnsurer(containers runningContainerLister, launcher workerRebalancerLauncher, players player.PlayerRepository) WorkerRebalancer {
	return &workerRebalanceEnsurer{containers: containers, launcher: launcher, players: players}
}

func (e *workerRebalanceEnsurer) RebalanceWorkers(ctx context.Context, playerID shared.PlayerID, hubSymbol, workerWaypoint string, count int) error {
	running, err := e.rebalancerRunning(ctx, playerID.Value())
	if err != nil {
		return fmt.Errorf("worker rebalance toward %s: check rebalancer running: %w", hubSymbol, err)
	}
	if running {
		return nil // the standing rebalancer already owns the vacancy moves
	}
	reconcilingPlayer, err := e.players.FindByID(ctx, playerID)
	if err != nil {
		return fmt.Errorf("worker rebalance toward %s: resolve agent: %w", hubSymbol, err)
	}
	if reconcilingPlayer == nil {
		return fmt.Errorf("worker rebalance toward %s: player %d not found", hubSymbol, playerID.Value())
	}
	if _, err := e.launcher.WorkerRebalancerCoordinator(ctx, playerID.Value(), reconcilingPlayer.AgentSymbol, false); err != nil {
		return fmt.Errorf("worker rebalance toward %s: launch worker-rebalancer: %w", hubSymbol, err)
	}
	return nil
}

func (e *workerRebalanceEnsurer) rebalancerRunning(ctx context.Context, playerID int) (bool, error) {
	pid := playerID
	for _, status := range []string{string(container.ContainerStatusRunning), string(container.ContainerStatusPending)} {
		summaries, err := e.containers.ListByStatusSimple(ctx, status, &pid)
		if err != nil {
			return false, err
		}
		for _, summary := range summaries {
			if summary.ContainerType == string(container.ContainerTypeWorkerRebalancerCoordinator) {
				return true, nil
			}
		}
	}
	return false, nil
}

// ---- tier 3: the depot warehouse buffer whitelist writer --------------------
//
// Drives the EXISTING supported-goods writer (sp-94du, StorageOperationRepository.
// UpdateSupportedGoods — the same primitive refreshRunningDepotWarehouseCaps
// uses): the differ already decided the good and whether it is buffered
// (UnitsCap > 0) or shed (UnitsCap == 0). The per-good target_units MAGNITUDE has
// no post-launch writer primitive to drive — building one would be reinventing,
// which this thin-wrapper lane must not do — so the whitelist membership is the
// dimension actuated here; the port carries UnitsCap so a follow-on wires the cap
// magnitude without touching the actuator.

type warehouseBufferConfigurator struct {
	db    *gorm.DB
	clock shared.Clock
}

// NewWarehouseBufferConfigurator drives tier-3 buffer adjusts through the
// existing depot warehouse supported-goods whitelist writer.
func NewWarehouseBufferConfigurator(db *gorm.DB, clock shared.Clock) BufferConfigurator {
	return &warehouseBufferConfigurator{db: db, clock: clock}
}

func (c *warehouseBufferConfigurator) AdjustBufferGood(ctx context.Context, playerID shared.PlayerID, hubSymbol, good string, unitsCap int) error {
	repo := persistence.NewStorageOperationRepository(c.db, c.clock)
	ops, err := repo.FindAllRunningByWaypoint(ctx, playerID.Value(), hubSymbol)
	if err != nil {
		return fmt.Errorf("adjust buffer %s@%s: load warehouse: %w", good, hubSymbol, err)
	}
	for _, op := range ops {
		if op.OperationType() != storage.OperationTypeWarehouse {
			continue
		}
		newGoods := applyWhitelistChange(op.SupportedGoods(), good, unitsCap > 0)
		if len(newGoods) == 0 {
			// UpdateSupportedGoods rejects an empty whitelist (a warehouse with no
			// goods strands every deposit — fail closed). A de-whitelist that would
			// empty the set is a teardown, out of the reconciler's v1 add/adjust
			// scope — skip it rather than strand the warehouse.
			continue
		}
		if err := repo.UpdateSupportedGoods(ctx, op.ID(), newGoods); err != nil {
			return fmt.Errorf("adjust buffer %s@%s: persist whitelist: %w", good, hubSymbol, err)
		}
	}
	return nil
}

// applyWhitelistChange returns the good-set after adding or removing good,
// preserving order and de-duplicating.
func applyWhitelistChange(current []string, good string, add bool) []string {
	out := make([]string, 0, len(current)+1)
	present := false
	for _, existing := range current {
		if existing == good {
			present = true
			if !add {
				continue // de-whitelist: drop it
			}
		}
		out = append(out, existing)
	}
	if add && !present {
		out = append(out, good)
	}
	return out
}

// ---- player resolver: numeric player from the ambient auth token ------------
//
// The frozen Actuator interface carries no player, so the reconciling player is
// resolved from the auth token the loop's mediator pass injected into ctx (the
// same token the SENSE lane's treasury read rides). Cached per token — a token is
// stable per player, so the agent lookup runs at most once per player lifetime.

type tokenPlayerResolver struct {
	api     domainPorts.APIClient
	players player.PlayerRepository

	mu    sync.Mutex
	cache map[string]shared.PlayerID
}

// NewTokenPlayerResolver resolves the reconciling player from the ambient token.
func NewTokenPlayerResolver(api domainPorts.APIClient, players player.PlayerRepository) PlayerResolver {
	return &tokenPlayerResolver{api: api, players: players, cache: map[string]shared.PlayerID{}}
}

func (r *tokenPlayerResolver) ResolvePlayer(ctx context.Context) (shared.PlayerID, error) {
	token, err := auth.PlayerTokenFromContext(ctx)
	if err != nil {
		return shared.PlayerID{}, fmt.Errorf("capacity actuator: %w", err)
	}
	if pid, ok := r.cached(token); ok {
		return pid, nil
	}
	agent, err := r.api.GetAgent(ctx, token)
	if err != nil {
		return shared.PlayerID{}, fmt.Errorf("capacity actuator: resolve agent from token: %w", err)
	}
	reconcilingPlayer, err := r.players.FindByAgentSymbol(ctx, agent.Symbol)
	if err != nil {
		return shared.PlayerID{}, fmt.Errorf("capacity actuator: find player %q: %w", agent.Symbol, err)
	}
	if reconcilingPlayer == nil {
		return shared.PlayerID{}, fmt.Errorf("capacity actuator: no player for agent %q", agent.Symbol)
	}
	r.store(token, reconcilingPlayer.ID)
	return reconcilingPlayer.ID, nil
}

func (r *tokenPlayerResolver) cached(token string) (shared.PlayerID, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pid, ok := r.cache[token]
	return pid, ok
}

func (r *tokenPlayerResolver) store(token string, pid shared.PlayerID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache[token] = pid
}
