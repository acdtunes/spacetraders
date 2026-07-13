// Package commands — the CONTRACT-HUB placement coordinator (sp-q2zq) is the standing "brain"
// that automates where contract haulers are HOMED, off LIVE market + contract data. It is a
// deliberate SIBLING of the factory SITING coordinator
// (internal/application/manufacturing/commands/run_siting_coordinator.go): it reuses siting's
// candidate → score → maintain(caps) shape verbatim; ONLY the candidate set and the score
// differ.
//
//	SCAN   the candidate set is the in-system waypoints that are the cheapest EXPORT/EXCHANGE
//	       source for >=1 contract good (single-system only — RULINGS #14). The concrete
//	       HubCandidateSource hides the market_data joins; the engine is geometry-only.
//	SCORE  a candidate hub C's value is the MARGINAL payment-weighted buy-leg it eliminates
//	       (greedy max-coverage / facility-location), NOT raw payment — so a central cluster
//	       self-limits and outliers score high with no special-casing. Demand per good is
//	       EWMA-smoothed (mandatory — the raw 46-contract signal is thin/noisy).
//	PLACE  each new / idle-unhomed hauler is assigned to argmax_C marginal(C | current homes),
//	       subject to the per-hub concentration cap. Homed haulers are LEFT ALONE (Phase 2
//	       re-homes them; the cost-gate for that is the pure ShouldRehome seam here).
//
// Phase 1 is PLACEMENT ONLY: it never re-homes an already-homed hauler (zero thrash risk) and
// it is idle-only (never strands a hauler mid-contract). Every decision is re-derived from the
// injected ports each tick — the coordinator holds NO in-memory decision state — so a daemon
// restart is transparent (RULINGS #2): homes persist through the HomeAssigner (the daemon is
// the single writer, RULINGS #3) and reload through the HaulerHomeSource on boot. Fail-safe:
// any transient scan / demand / fleet read error leaves every existing home untouched (the
// siting fail-open idiom). All knobs are launch-config keys resolving to documented defaults
// (RULINGS #5); the Analyst owns the weights.
package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	// Config defaults (RULINGS #5). Documented starting values the Analyst tunes via config.
	defaultContractHubTickSeconds = 300 // placement is strategic; reacts to fleet/demand shifts

	// defaultContractHubEWMAHalfLife is the demand-smoothing half-life in CONTRACTS (not
	// wall-clock): the Analyst's documented start is ~half the era's contract count (the era ran
	// ~46 contracts → ~23). Smoothing is mandatory so single-contract noise cannot move a home.
	defaultContractHubEWMAHalfLife = 23.0

	// defaultContractHubMaxHaulersPerHub reuses the siting per-site concentration cap so a hub
	// never accumulates the whole fleet (defense-in-depth on top of the self-limiting score).
	defaultContractHubMaxHaulersPerHub = 3

	// defaultContractHubBaselineCoverage is the "no hub yet" coverage constant — large so the
	// FIRST hub captures the highest-demand cluster. Any value that dominates real inter-waypoint
	// distances works; the Analyst tunes it.
	defaultContractHubBaselineCoverage = 1_000_000.0

	// Phase-2 cost-gate knobs (documented here, consumed only by the pure ShouldRehome seam).
	defaultContractHubRehomeHysteresis           = 50.0 // margin over break-even that a re-home must clear
	defaultContractHubExpectedRemainingContracts = 10.0 // horizon a durable saving is amortized over
)

// --- Value types the engine ranks (all geometry-only; the concrete ports carry the joins) ---

// HubCandidate is one in-system waypoint that is the cheapest EXPORT/EXCHANGE source for >=1
// contract good — the set the coordinator homes haulers onto. X/Y are its in-system position.
type HubCandidate struct {
	Waypoint string
	X, Y     float64
}

// GoodSource is a contract good's cheapest in-system source S_G, with its position — the point a
// hauler's contract buy-leg would incur distance to under closest-ship-wins.
type GoodSource struct {
	Good     string
	Waypoint string
	X, Y     float64
}

// HubScan is one LIVE snapshot the coordinator scores: the candidate hubs plus, per contract
// good, its cheapest source. Single-system only (RULINGS #14) — the concrete source restricts to
// the operating system so a cross-gate source the serial contract pipeline cannot afford is never
// selected.
type HubScan struct {
	Candidates []HubCandidate
	Sources    []GoodSource
}

// ContractDemandRecord is one recent contract's demand signal: the goods it delivered and the
// payment on fulfilment. The engine folds these (oldest→newest) into an EWMA per good so demand =
// payment × recurrence, smoothed.
type ContractDemandRecord struct {
	Goods              []string
	PaymentOnFulfilled int
}

// HaulerHome is one contract hauler's current home state. HomeWaypoint == "" marks an unhomed
// (e.g. newly-added) hauler; a set HomeWaypoint carries the home's position (HomeX/HomeY) that
// feeds the coverage baseline. Idle gates placement (idle-only — never strand mid-contract).
type HaulerHome struct {
	ShipSymbol   string
	HomeWaypoint string
	HomeX, HomeY float64
	Idle         bool
}

// --- Ports (interfaces the handler depends on; the daemon wires concrete impls, tests inject
// fakes — the siting optional-collaborator idiom) ---

// HubCandidateSource enumerates the candidate hubs + per-good cheapest sources off LIVE market
// data (player-scoped, current era, single-system). The concrete impl hides the market_data
// joins. A read error is fail-safe: the coordinator leaves every home untouched that tick.
type HubCandidateSource interface {
	ScanHubs(ctx context.Context, playerID int) (HubScan, error)
}

// ContractDemandSource returns the recent contracts (oldest→newest) the demand EWMA folds. The
// concrete impl reads contracts.deliveries_json. A read error is fail-safe.
type ContractDemandSource interface {
	RecentContracts(ctx context.Context, playerID int) ([]ContractDemandRecord, error)
}

// HaulerHomeSource reports the contract haulers and their current homes each tick (reloaded from
// persisted state, so restart is transparent — RULINGS #2). A read error is fail-safe.
type HaulerHomeSource interface {
	Haulers(ctx context.Context, playerID int) ([]HaulerHome, error)
}

// HomeAssigner persists a hauler's assigned home hub. The daemon is the single writer of ship
// state (RULINGS #3): the concrete impl is a daemon RPC / container operation, never a CLI-side
// mutation. The assignment MUST persist and reload on boot (RULINGS #2).
type HomeAssigner interface {
	AssignHome(ctx context.Context, playerID int, shipSymbol, hubWaypoint string) error
}

// RunContractHubCoordinatorCommand launches the standing contract-hub placement coordinator for a
// player. Like the siting / fleet coordinators it runs an infinite reconcile loop inside a single
// Handle() call; the daemon container wraps it. Every knob is a launch-config key (RULINGS #5);
// a zero value falls back to the documented default, so the launch passes only what it overrides.
type RunContractHubCoordinatorCommand struct {
	PlayerID    int
	ContainerID string
	AgentSymbol string

	// Disabled is the RULINGS #5 escape hatch. Absent/false = ACTIVE (LIVE BY DEFAULT).
	Disabled bool
	// DryRun computes + logs the placement it WOULD make but assigns no home (watch mode).
	DryRun bool

	TickIntervalSecs int

	// EWMAHalfLifeContracts is the demand-smoothing half-life in contracts (Analyst-owned).
	EWMAHalfLifeContracts float64
	// MaxHaulersPerHub is the per-hub concentration cap (Analyst-owned; reuses the siting cap).
	MaxHaulersPerHub int
	// BaselineCoverage is the "no hub yet" coverage constant (Analyst-owned).
	BaselineCoverage float64

	// Phase-2 knobs (Analyst-owned; consumed only by the pure ShouldRehome seam, not Phase 1).
	RehomeHysteresisMargin     float64
	ExpectedRemainingContracts float64
}

// RunContractHubCoordinatorResponse reports reconcile progress. Because the loop is infinite it is
// only observed on context cancellation (shutdown).
type RunContractHubCoordinatorResponse struct {
	Ticks  int
	Errors []string
}

// RunContractHubCoordinatorHandler reconciles the desired hauler-home geometry against the live
// fleet every tick. It is a registered singleton; ALL decision inputs are derived fresh from the
// injected ports each pass, so it holds no in-memory state and a daemon restart is transparent.
type RunContractHubCoordinatorHandler struct {
	candidates HubCandidateSource
	demand     ContractDemandSource
	homes      HaulerHomeSource
	assigner   HomeAssigner
	clock      shared.Clock
}

// NewRunContractHubCoordinatorHandler wires the coordinator. clock defaults to the real clock when
// nil (production).
func NewRunContractHubCoordinatorHandler(
	candidates HubCandidateSource,
	demand ContractDemandSource,
	homes HaulerHomeSource,
	assigner HomeAssigner,
	clock shared.Clock,
) *RunContractHubCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunContractHubCoordinatorHandler{
		candidates: candidates,
		demand:     demand,
		homes:      homes,
		assigner:   assigner,
		clock:      clock,
	}
}

// Handle runs the reconcile loop until the context is cancelled.
func (h *RunContractHubCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	logger := common.LoggerFromContext(ctx)

	cmd, ok := request.(*RunContractHubCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	cfg := resolveContractHubConfig(cmd)
	logger.Log("INFO", fmt.Sprintf("Contract-hub coordinator starting (tick %s, dry_run=%v, disabled=%v)", cfg.Tick, cfg.DryRun, cfg.Disabled), map[string]interface{}{
		"action":       "contract_hub_start",
		"container_id": cmd.ContainerID,
		"dry_run":      cfg.DryRun,
		"disabled":     cfg.Disabled,
	})

	result := &RunContractHubCoordinatorResponse{Errors: []string{}}

	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		if _, err := h.reconcileOnce(ctx, cmd); err != nil {
			result.Errors = append(result.Errors, err.Error())
			logger.Log("ERROR", fmt.Sprintf("Contract-hub reconcile failed: %v", err), nil)
		}
		result.Ticks++

		select {
		case <-time.After(cfg.Tick):
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}
}

// contractHubRunConfig is the launch command with every default resolved, so the reconcile logic
// never repeats the "<= 0 → default" fallback (RULINGS #5, the siting resolveConfig idiom).
type contractHubRunConfig struct {
	Disabled bool
	DryRun   bool
	Tick     time.Duration

	EWMAHalfLife     float64
	MaxHaulersPerHub int
	BaselineCoverage float64

	RehomeHysteresisMargin     float64
	ExpectedRemainingContracts float64
}

func resolveContractHubConfig(cmd *RunContractHubCoordinatorCommand) contractHubRunConfig {
	c := contractHubRunConfig{
		Disabled:                   cmd.Disabled,
		DryRun:                     cmd.DryRun,
		Tick:                       time.Duration(cmd.TickIntervalSecs) * time.Second,
		EWMAHalfLife:               cmd.EWMAHalfLifeContracts,
		MaxHaulersPerHub:           cmd.MaxHaulersPerHub,
		BaselineCoverage:           cmd.BaselineCoverage,
		RehomeHysteresisMargin:     cmd.RehomeHysteresisMargin,
		ExpectedRemainingContracts: cmd.ExpectedRemainingContracts,
	}
	if c.Tick <= 0 {
		c.Tick = defaultContractHubTickSeconds * time.Second
	}
	if c.EWMAHalfLife <= 0 {
		c.EWMAHalfLife = defaultContractHubEWMAHalfLife
	}
	if c.MaxHaulersPerHub <= 0 {
		c.MaxHaulersPerHub = defaultContractHubMaxHaulersPerHub
	}
	if c.BaselineCoverage <= 0 {
		c.BaselineCoverage = defaultContractHubBaselineCoverage
	}
	if c.RehomeHysteresisMargin <= 0 {
		c.RehomeHysteresisMargin = defaultContractHubRehomeHysteresis
	}
	if c.ExpectedRemainingContracts <= 0 {
		c.ExpectedRemainingContracts = defaultContractHubExpectedRemainingContracts
	}
	return c
}

// contractHubReconcileResult tallies one tick's effect for metrics/logging.
type contractHubReconcileResult struct {
	Candidates int
	Haulers    int
	Planned    int // placements the tick computed (== Placed unless DryRun)
	Placed     int // homes actually assigned this tick
}

// reconcileOnce runs one full SCAN → SCORE → PLACE pass. It is the unit the tests drive directly;
// Handle just calls it on the tick.
//
// FAIL-SAFE ORDER (acceptance #4): the three reads happen BEFORE any assignment, and any read
// error returns immediately — so a transient market/contract/fleet failure leaves every existing
// home untouched (no AssignHome is ever reached on the error path).
func (h *RunContractHubCoordinatorHandler) reconcileOnce(ctx context.Context, cmd *RunContractHubCoordinatorCommand) (contractHubReconcileResult, error) {
	cfg := resolveContractHubConfig(cmd)
	logger := common.LoggerFromContext(ctx)

	// Boot-gate (RULINGS #5): the container stays resident when disabled so a config flip +
	// restart re-arms it with no manual relaunch, but it takes no action while stood down.
	if cfg.Disabled {
		return contractHubReconcileResult{}, nil
	}

	scan, err := h.candidates.ScanHubs(ctx, cmd.PlayerID)
	if err != nil {
		return contractHubReconcileResult{}, fmt.Errorf("contract-hub scan: %w", err)
	}
	contracts, err := h.demand.RecentContracts(ctx, cmd.PlayerID)
	if err != nil {
		return contractHubReconcileResult{}, fmt.Errorf("contract-hub demand: %w", err)
	}
	haulers, err := h.homes.Haulers(ctx, cmd.PlayerID)
	if err != nil {
		return contractHubReconcileResult{}, fmt.Errorf("contract-hub fleet: %w", err)
	}

	weights := computeDemandWeights(contracts, cfg.EWMAHalfLife)

	// Partition the fleet:
	//   - already-homed haulers FEED the coverage baseline (their home positions) and the per-hub
	//     concentration counts, but are LEFT ALONE (Phase 1 never re-homes — that is Phase 2).
	//   - new / idle-unhomed haulers are the placement set (idle-only: a busy hull is skipped so a
	//     hauler is never stranded mid-contract).
	var homedPositions []hubPosition
	hubCounts := make(map[string]int)
	var toPlace []HaulerHome
	for _, hh := range haulers {
		if hh.HomeWaypoint != "" {
			homedPositions = append(homedPositions, hubPosition{X: hh.HomeX, Y: hh.HomeY})
			hubCounts[hh.HomeWaypoint]++
			continue
		}
		if !hh.Idle {
			continue
		}
		toPlace = append(toPlace, hh)
	}

	placements := h.planPlacements(cfg, scan, weights, homedPositions, hubCounts, toPlace)
	res := contractHubReconcileResult{
		Candidates: len(scan.Candidates),
		Haulers:    len(haulers),
		Planned:    len(placements),
	}

	if cfg.DryRun {
		logger.Log("INFO", fmt.Sprintf("Contract-hub DRY-RUN: would home %d hauler(s)", len(placements)), map[string]interface{}{
			"action":       "contract_hub_dryrun",
			"container_id": cmd.ContainerID,
			"planned":      len(placements),
		})
		return res, nil
	}

	// ACT — persist each computed home. A single assign failure is logged and skipped; the rest
	// still home (never abandon the whole tick over one hull).
	for _, p := range placements {
		if err := h.assigner.AssignHome(ctx, cmd.PlayerID, p.ShipSymbol, p.HubWaypoint); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to home hauler %s to %s: %v", p.ShipSymbol, p.HubWaypoint, err), nil)
			continue
		}
		res.Placed++
		logger.Log("INFO", fmt.Sprintf("Contract hauler %s homed to best-open hub %s", p.ShipSymbol, p.HubWaypoint), map[string]interface{}{
			"action":       "contract_hub_placed",
			"container_id": cmd.ContainerID,
			"ship_symbol":  p.ShipSymbol,
			"hub":          p.HubWaypoint,
		})
	}

	logger.Log("INFO", fmt.Sprintf("Contract-hub tick: %d candidates, %d haulers, %d homed", res.Candidates, res.Haulers, res.Placed), map[string]interface{}{
		"action":       "contract_hub_tick",
		"container_id": cmd.ContainerID,
		"candidates":   res.Candidates,
		"haulers":      res.Haulers,
		"placed":       res.Placed,
	})
	return res, nil
}
