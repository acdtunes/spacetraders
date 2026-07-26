package commands

// The probe-sensing coordinator is the single budgeted sensing loop: it decides
// WHERE standing scout posts exist (systems whose markets deal in whitelisted
// goods above a depth floor), HOW MANY probes each post gets (1, or 2 past the
// hot-market threshold), WHEN scanning sheds under API pressure (dormancy
// rotation — probes park in place, they never move), and WHETHER to buy one
// probe this tick (aggregate demand clamped by the probe budget N, spent only
// through the fail-closed guarded buyer). Sensing cost is bounded by the chosen
// budget, never by the size of the charted map: a system charted tomorrow adds
// no obligation until it clears the whitelist and the floor.
//
// The loop is idempotent and restart-safe (RULINGS #2): every decision is
// re-derived from persisted state each tick (the market cache, the posts table,
// the ledger-backed buy guards). The only in-memory state is the rotation
// cursor, which a restart resets harmlessly (rotation re-starts from the top of
// the ring). All movement, manning, and market partitioning stay with the scout
// post reconciler; this coordinator's writes are the desired-state posts table
// and the guarded probe buy.

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/application/probebuy"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	// Config defaults (RULINGS #5: every operational number is container config,
	// filled here only when the launch config leaves it unset).
	defaultSensingTickSeconds          = 30
	defaultSensingDepthFloor           = 2_000_000 // whitelisted-goods depth below which a system earns no probe (~bottom quartile)
	defaultSensingProbeBudget          = 150       // N — the single budget dial: the total probe count the fleet may hold
	defaultSensingSecondProbeThreshold = 12        // hot markets above which a system earns a second probe
	defaultSensingPurchaseCooldownSecs = 10
	defaultSensingFreshnessTargetSecs  = 3600 // stamped on every standing post; the reconciler's manning watchdog reads it
	defaultSensingWaitLowMs            = 50   // limiter wait at or under this: full scanning, discovery allowed
	defaultSensingWaitHighMs           = 1000 // limiter wait at or past this: scanning sheds toward the 0.5 floor
	defaultSensingMaxSpend             = 500_000
	defaultSensingSpendWindowSecs      = 3600
)

// defaultSensingWhitelist is the era-invariant goods whitelist: a market is
// worth observing for what it DEALS IN, never what it is currently worth —
// prices are volatile and would drop a crushed market right before it recovers.
func defaultSensingWhitelist() []string {
	return []string{
		"CLOTHING", "LAB_INSTRUMENTS", "FABRICS", "FOOD", "ADVANCED_CIRCUITRY",
		"MEDICINE", "EQUIPMENT", "URANITE", "MICROPROCESSORS", "SHIP_PLATING",
		"MACHINERY", "ELECTRONICS",
	}
}

// MarketDepthReader is the narrow census read: one row per (waypoint, good)
// from the market cache. Satisfied by the GORM market repository.
type MarketDepthReader interface {
	MarketDepthRows(ctx context.Context, playerID int) ([]domainScouting.MarketDepthRow, error)
}

// guardedBuyer is the shared fail-closed buy engine (probebuy.GuardedProbeBuyer):
// it runs the whole money-guard stack per call, so this coordinator never
// re-derives a spend decision of its own.
type guardedBuyer interface {
	MaybeBuy(ctx context.Context, playerID shared.PlayerID, demand, supply int, dryRun bool, target probebuy.ProbeTarget) probebuy.Outcome
}

// RunProbeSensingCoordinatorCommand launches the standing coordinator for a
// player. All knobs are launch-config keys (RULINGS #5); the zero value falls
// back to the documented default.
type RunProbeSensingCoordinatorCommand struct {
	PlayerID    shared.PlayerID
	ContainerID string

	GoodsWhitelist       []string
	DepthFloor           int64
	ProbeBudget          int // N — the total probe count the fleet may hold
	SecondProbeThreshold int
	PurchaseCooldownSecs int
	TickSecs             int
	WaitLowMs            int
	WaitHighMs           int
	FreshnessTargetSecs  int
	MaxSpendPerCycle     int
	SpendWindowSecs      int
}

// RunProbeSensingCoordinatorResponse reports reconcile progress. Because the
// loop is infinite it is only observed on context cancellation (shutdown).
type RunProbeSensingCoordinatorResponse struct {
	Ticks  int
	Errors []string
}

// RunProbeSensingCoordinatorHandler reconciles the sensing scope, the dormancy
// rotation, and the budgeted probe buy every tick. It is a registered singleton
// (one instance serves every player's ticks); the per-player rotation cursor is
// the sole in-memory state.
type RunProbeSensingCoordinatorHandler struct {
	depthReader MarketDepthReader
	postRepo    domainScouting.ScoutPostRepository
	fleetRepo   FleetReader
	pressure    domainScouting.PressureReader
	ledgerRepo  ledger.TransactionRepository
	clock       shared.Clock

	// treasury and purchaser are optional collaborators wired via setters (the
	// codebase's optional-injection idiom). A nil treasury or purchaser fails
	// the PURCHASE path closed inside the guarded buyer; sensing-scope and
	// dormancy writes need neither.
	treasury  probebuy.TreasuryReader
	purchaser probebuy.ProbePurchaser

	// newBuyer builds the tick's guarded buyer from the resolved buy config.
	// The default builds the real probebuy.GuardedProbeBuyer (guard stack
	// reused unmodified — RULINGS #4); it is a seam only for tests.
	newBuyer func(cfg probebuy.Config) guardedBuyer

	// captainEvents emits the coordinator error-loop event when a reconcile
	// pass fails with the identical error for DefaultStreakThreshold
	// consecutive ticks — under the wake model the captain event IS the
	// standing failure sensor. Optional-injection.
	captainEvents captain.EventRecorder

	// cursorMu guards cursors against the singleton-handler concurrency (many
	// players' ticks share one handler). cursors holds each player's rotation
	// cursor; memory-only by design — a restart merely restarts the rotation.
	cursorMu sync.Mutex
	cursors  map[int]int
}

// NewRunProbeSensingCoordinatorHandler wires the coordinator. clock defaults to
// the real clock when nil (production). The treasury reader and probe purchaser
// are optional and injected separately.
func NewRunProbeSensingCoordinatorHandler(
	depthReader MarketDepthReader,
	postRepo domainScouting.ScoutPostRepository,
	fleetRepo FleetReader,
	pressure domainScouting.PressureReader,
	ledgerRepo ledger.TransactionRepository,
	clock shared.Clock,
) *RunProbeSensingCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	h := &RunProbeSensingCoordinatorHandler{
		depthReader: depthReader,
		postRepo:    postRepo,
		fleetRepo:   fleetRepo,
		pressure:    pressure,
		ledgerRepo:  ledgerRepo,
		clock:       clock,
		cursors:     make(map[int]int),
	}
	h.newBuyer = func(cfg probebuy.Config) guardedBuyer {
		return probebuy.NewGuardedProbeBuyer(h.treasury, h.purchaser, h.ledgerRepo, h.clock, cfg)
	}
	return h
}

// SetTreasuryReader wires the live-treasury source for the 25% guard. Leaving
// it unset keeps the PURCHASE path fail-closed.
func (h *RunProbeSensingCoordinatorHandler) SetTreasuryReader(t probebuy.TreasuryReader) {
	h.treasury = t
}

// SetProbePurchaser wires the price-and-buy port over the existing
// purchase_ship machinery. Leaving it unset keeps the PURCHASE path fail-closed.
func (h *RunProbeSensingCoordinatorHandler) SetProbePurchaser(p probebuy.ProbePurchaser) {
	h.purchaser = p
}

// SetEventRecorder wires the captain outbox for the reconcile error-loop event.
func (h *RunProbeSensingCoordinatorHandler) SetEventRecorder(rec captain.EventRecorder) {
	h.captainEvents = rec
}

// noteReconcile records one reconcile pass at the streak checkpoint: a nil err
// resets the streak; a non-nil err repeating identically for
// DefaultStreakThreshold passes emits the coordinator error-loop captain event.
// Edge-triggered and nil-safe on the recorder.
func (h *RunProbeSensingCoordinatorHandler) noteReconcile(ctx context.Context, cmd *RunProbeSensingCoordinatorCommand, errMon *health.Monitor, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if streak, crossed := errMon.Note("reconcile", msg); crossed {
		health.RecordErrorLoop(h.captainEvents, common.LoggerFromContext(ctx), cmd.ContainerID, cmd.PlayerID.Value(), "reconcile", err, streak)
	}
}

// sensingConfig is the launch command with every default resolved.
type sensingConfig struct {
	Whitelist            map[string]bool
	DepthFloor           int64
	ProbeBudget          int
	SecondProbeThreshold int
	FreshnessTarget      time.Duration
	Tick                 time.Duration
	WaitLow              time.Duration
	WaitHigh             time.Duration
	Buy                  probebuy.Config
}

// resolveSensingConfig resolves one launch's effective config: a zero/absent
// knob means the documented default.
func resolveSensingConfig(cmd *RunProbeSensingCoordinatorCommand) sensingConfig {
	goods := cmd.GoodsWhitelist
	if len(goods) == 0 {
		goods = defaultSensingWhitelist()
	}
	whitelist := make(map[string]bool, len(goods))
	for _, good := range goods {
		whitelist[good] = true
	}

	c := sensingConfig{
		Whitelist:            whitelist,
		DepthFloor:           cmd.DepthFloor,
		ProbeBudget:          cmd.ProbeBudget,
		SecondProbeThreshold: cmd.SecondProbeThreshold,
		FreshnessTarget:      time.Duration(cmd.FreshnessTargetSecs) * time.Second,
		Tick:                 time.Duration(cmd.TickSecs) * time.Second,
		WaitLow:              time.Duration(cmd.WaitLowMs) * time.Millisecond,
		WaitHigh:             time.Duration(cmd.WaitHighMs) * time.Millisecond,
		Buy: probebuy.Config{
			MaxProbeFleet:    cmd.ProbeBudget,
			MaxSpendPerCycle: cmd.MaxSpendPerCycle,
			PurchaseCooldown: time.Duration(cmd.PurchaseCooldownSecs) * time.Second,
			SpendWindow:      time.Duration(cmd.SpendWindowSecs) * time.Second,
			// Probe buys always leave the immutable working-capital reserve
			// spendable (RULINGS #4/#5 — a hard floor, not a knob).
			ReserveFloor: common.ImmutableReserveFloor,
		},
	}
	if c.DepthFloor <= 0 {
		c.DepthFloor = defaultSensingDepthFloor
	}
	if c.ProbeBudget <= 0 {
		c.ProbeBudget = defaultSensingProbeBudget
		c.Buy.MaxProbeFleet = defaultSensingProbeBudget
	}
	if c.SecondProbeThreshold <= 0 {
		c.SecondProbeThreshold = defaultSensingSecondProbeThreshold
	}
	if c.FreshnessTarget <= 0 {
		c.FreshnessTarget = defaultSensingFreshnessTargetSecs * time.Second
	}
	if c.Tick <= 0 {
		c.Tick = defaultSensingTickSeconds * time.Second
	}
	if c.WaitLow <= 0 {
		c.WaitLow = defaultSensingWaitLowMs * time.Millisecond
	}
	if c.WaitHigh <= 0 {
		c.WaitHigh = defaultSensingWaitHighMs * time.Millisecond
	}
	if c.Buy.MaxSpendPerCycle <= 0 {
		c.Buy.MaxSpendPerCycle = defaultSensingMaxSpend
	}
	if c.Buy.PurchaseCooldown <= 0 {
		c.Buy.PurchaseCooldown = defaultSensingPurchaseCooldownSecs * time.Second
	}
	if c.Buy.SpendWindow <= 0 {
		c.Buy.SpendWindow = defaultSensingSpendWindowSecs * time.Second
	}
	return c
}

// Handle runs the reconcile loop until the context is cancelled.
func (h *RunProbeSensingCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	logger := common.LoggerFromContext(ctx)

	cmd, ok := request.(*RunProbeSensingCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	cfg := resolveSensingConfig(cmd)
	result := &RunProbeSensingCoordinatorResponse{Errors: []string{}}
	logger.Log("INFO", fmt.Sprintf("Probe sensing coordinator starting (tick %s, budget %d probes)", cfg.Tick, cfg.ProbeBudget), map[string]interface{}{
		"action":       "probe_sensing_start",
		"container_id": cmd.ContainerID,
	})

	errMon := health.NewMonitor(health.DefaultStreakThreshold)

	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		err := h.ReconcileOnce(ctx, cmd)
		if err != nil {
			result.Errors = append(result.Errors, err.Error())
			logger.Log("ERROR", fmt.Sprintf("Probe sensing reconcile failed: %v", err), nil)
		}
		h.noteReconcile(ctx, cmd, errMon, err)
		result.Ticks++

		select {
		case <-time.After(cfg.Tick):
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}
}

// ReconcileOnce is one reconcile pass — the unit the tests drive directly.
// Pure inputs → diffs: census → plan → post diff → dormancy rotation → one
// guarded buy → one heartbeat.
func (h *RunProbeSensingCoordinatorHandler) ReconcileOnce(ctx context.Context, cmd *RunProbeSensingCoordinatorCommand) error {
	logger := common.LoggerFromContext(ctx)
	cfg := resolveSensingConfig(cmd)
	now := h.clock.Now()

	rows, err := h.depthReader.MarketDepthRows(ctx, cmd.PlayerID.Value())
	if err != nil {
		return fmt.Errorf("failed to read market depth census: %w", err)
	}
	profiles := domainScouting.BuildSensingProfiles(rows, cfg.Whitelist)

	// ERA-GAP FAIL-SAFE: an empty census (cold DB, fresh era, transient gap)
	// carries no signal. Acting on it would mass-remove every standing post in
	// one tick — a fleet-killer — so declare/remove NOTHING and wait for the
	// census to repopulate.
	if len(profiles) == 0 {
		logger.Log("INFO", "Probe sensing cycle: empty census — era-gap fail-safe, declaring/removing/buying nothing", map[string]interface{}{
			"action":   "probe_sensing_cycle",
			"in_scope": 0,
		})
		return nil
	}

	plan := domainScouting.PlanSensing(profiles, cfg.DepthFloor, cfg.SecondProbeThreshold)

	posts, err := h.postRepo.ListActive(ctx, cmd.PlayerID.Value())
	if err != nil {
		return fmt.Errorf("failed to list scout posts: %w", err)
	}
	// sweep_once posts are the frontier's, and Upsert is keyed by (player,
	// system) — a standing write against a sweep system would clobber the sweep
	// row, so those systems are untouchable this tick.
	standingBySystem := make(map[string]*domainScouting.ScoutPost)
	sweepSystems := make(map[string]bool)
	for _, post := range posts {
		if post.Kind == domainScouting.PostKindStanding {
			standingBySystem[post.SystemSymbol] = post
			continue
		}
		sweepSystems[post.SystemSymbol] = true
	}

	// Rotation: share is derived ONLY via ActiveShare — its 0.5 floor is what
	// bounds degradation, and a raw share of 0 would park the ring forever.
	inScope := make([]string, 0, len(plan.Hulls))
	for system := range plan.Hulls {
		inScope = append(inScope, system)
	}
	sort.Strings(inScope)
	wait := time.Duration(0)
	if h.pressure != nil {
		wait = h.pressure.Current(now)
	}
	share, discovery := domainScouting.ActiveShare(wait, cfg.WaitLow, cfg.WaitHigh)
	dormant, nextCursor := domainScouting.RotateDormant(inScope, share, h.cursor(cmd.PlayerID.Value()))
	h.setCursor(cmd.PlayerID.Value(), nextCursor)

	// Post diff. Writes are DELTAS only: a converged tick writes nothing, so
	// steady state costs zero rows (write-amplification guard).
	upserts := 0
	for _, system := range inScope {
		if sweepSystems[system] {
			continue
		}
		wantDormant := dormant[system]
		existing := standingBySystem[system]
		if existing == nil {
			post := &domainScouting.ScoutPost{
				PlayerID:        cmd.PlayerID.Value(),
				SystemSymbol:    system,
				FreshnessTarget: cfg.FreshnessTarget,
				Kind:            domainScouting.PostKindStanding,
				Hulls:           plan.Hulls[system],
				Dormant:         wantDormant,
				CreatedAt:       now,
			}
			if err := h.postRepo.Upsert(ctx, post); err != nil {
				logger.Log("WARNING", fmt.Sprintf("Failed to declare sensing post %s: %v", system, err), nil)
				continue
			}
			upserts++
			logger.Log("INFO", fmt.Sprintf("Declared standing sensing post %s sized to %d probe(s) — reconciler will man and partition", system, post.Hulls), map[string]interface{}{
				"action": "sensing_post_declared", "system_symbol": system, "hulls": post.Hulls,
			})
			continue
		}
		// Never sized below the post's manning floor: bootstrap floor-protects
		// home with MinHulls, and honouring it here is what keeps the home
		// probes manned through INCOME.
		hulls := existing.FloorHulls(plan.Hulls[system])
		if existing.HullBudget() == hulls && existing.Dormant == wantDormant {
			continue
		}
		updated := *existing
		updated.Hulls = hulls
		updated.Dormant = wantDormant
		if err := h.postRepo.Upsert(ctx, &updated); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to update sensing post %s: %v", system, err), nil)
			continue
		}
		upserts++
	}

	// Out-of-scope standing posts: removed — freeing their probes — EXCEPT a
	// MinHulls-floored post (home), which is kept as-is and woken if dormant so
	// it can never be stranded parked outside the rotation.
	removed := 0
	for _, post := range posts {
		if post.Kind != domainScouting.PostKindStanding {
			continue
		}
		if _, in := plan.Hulls[post.SystemSymbol]; in {
			continue
		}
		if post.MinHulls > 0 {
			if post.Dormant {
				woken := *post
				woken.Dormant = false
				if err := h.postRepo.Upsert(ctx, &woken); err != nil {
					logger.Log("WARNING", fmt.Sprintf("Failed to wake floored sensing post %s: %v", post.SystemSymbol, err), nil)
					continue
				}
				upserts++
			}
			continue
		}
		if err := h.postRepo.Remove(ctx, cmd.PlayerID.Value(), post.SystemSymbol); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to remove out-of-scope sensing post %s: %v", post.SystemSymbol, err), nil)
			continue
		}
		removed++
		logger.Log("INFO", fmt.Sprintf("Removed sensing post %s — out of sensing scope, probes freed to the pool", post.SystemSymbol), map[string]interface{}{
			"action": "sensing_post_removed", "system_symbol": post.SystemSymbol,
		})
	}

	// Budgeted buy: demand is the plan total plus funded discovery, clamped by
	// N — the single budget dial. Supply counts every scout-type hull available
	// to scouting (idle, in-flight, or manning), so an en-route probe is never
	// double-bought.
	supply, err := h.probeSupply(ctx, cmd)
	if err != nil {
		return err
	}
	discoveryDemand := 0 // discovery funds its own hulls from limiter headroom; unfunded until the discovery pass wires it
	demand := plan.TotalHulls
	if discovery {
		demand += discoveryDemand
	}
	if demand > cfg.ProbeBudget {
		demand = cfg.ProbeBudget
	}

	buyer := h.newBuyer(cfg.Buy)
	target := probebuy.ProbeTarget{
		System:                    neediestSensingSystem(plan, standingBySystem),
		HopPenaltyCredits:         probebuy.DefaultHopPenaltyCredits,
		SiblingPriceMarginCredits: probebuy.DefaultSiblingPriceMarginCredits,
		ClaimOwnerContainerID:     cmd.ContainerID,
	}
	outcome := buyer.MaybeBuy(ctx, cmd.PlayerID, demand, supply, false, target)

	logger.Log("INFO", fmt.Sprintf("Probe sensing cycle: %d in-scope systems, %d hulls desired, supply %d, share %.2f, %d dormant, discovery=%v — %s",
		len(inScope), plan.TotalHulls, supply, share, len(dormant), discovery, outcome.Reason), map[string]interface{}{
		"action":        "probe_sensing_cycle",
		"in_scope":      len(inScope),
		"hulls_desired": plan.TotalHulls,
		"supply":        supply,
		"share":         share,
		"dormant":       len(dormant),
		"discovery":     discovery,
		"bought":        outcome.Bought,
		"upserts":       upserts,
		"removed":       removed,
	})
	if outcome.Bought {
		logger.Log("INFO", fmt.Sprintf("Probe sensing bought probe %s for %d at %s (demand %d > supply %d) — landed undedicated, reconciler will relay", outcome.Symbol, outcome.Price, outcome.Yard, demand, supply), map[string]interface{}{
			"action":      "sensing_probe_purchased",
			"ship_symbol": outcome.Symbol,
			"price":       outcome.Price,
			"yard":        outcome.Yard,
		})
	}
	return nil
}

// probeSupply is the sensing fleet count: every scout-type hull that is
// undedicated or scout-tagged, in ANY nav state — the same reader and filter
// the freshness supply count uses, so the two coordinators can never disagree
// about what a probe is.
func (h *RunProbeSensingCoordinatorHandler) probeSupply(ctx context.Context, cmd *RunProbeSensingCoordinatorCommand) (int, error) {
	ships, err := h.fleetRepo.FindAllByPlayer(ctx, cmd.PlayerID)
	if err != nil {
		return 0, fmt.Errorf("failed to list fleet: %w", err)
	}
	supply := 0
	for _, ship := range ships {
		if !ship.IsScoutType() {
			continue
		}
		if fleet := ship.DedicatedFleet(); fleet != "" && fleet != freshnessScoutFleetTag {
			continue
		}
		supply++
	}
	return supply, nil
}

// neediestSensingSystem names the in-plan system with the largest unmet hull
// gap — the demand-proximal buy hint, so a bought probe spawns near the demand
// it will serve. Deterministic (systems iterated sorted); "" (the home-yard
// path) when no gap is positive. A hint only: the money guards are unchanged.
func neediestSensingSystem(plan domainScouting.SensingPlan, standingBySystem map[string]*domainScouting.ScoutPost) string {
	systems := make([]string, 0, len(plan.Hulls))
	for system := range plan.Hulls {
		systems = append(systems, system)
	}
	sort.Strings(systems)
	neediest, biggest := "", 0
	for _, system := range systems {
		current := 0
		if post := standingBySystem[system]; post != nil {
			current = post.HullBudget()
		}
		if gap := plan.Hulls[system] - current; gap > biggest {
			neediest, biggest = system, gap
		}
	}
	return neediest
}

// cursor returns the player's rotation cursor (memory-only by design: a
// restart merely restarts the rotation from the top of the ring).
func (h *RunProbeSensingCoordinatorHandler) cursor(playerID int) int {
	h.cursorMu.Lock()
	defer h.cursorMu.Unlock()
	return h.cursors[playerID]
}

func (h *RunProbeSensingCoordinatorHandler) setCursor(playerID, cursor int) {
	h.cursorMu.Lock()
	defer h.cursorMu.Unlock()
	h.cursors[playerID] = cursor
}
