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
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

const (
	// Config defaults (RULINGS #5: every operational number is container config,
	// filled here only when the launch config leaves it unset).
	defaultSensingTickSeconds          = 30
	defaultSensingDepthFloor           = 2_000_000 // whitelisted-goods depth below which a system earns no probe (~bottom quartile)
	defaultSensingProbeBudget          = 150       // N — the single budget dial: the total probe count the fleet may hold
	defaultSensingSecondProbeThreshold = 12        // hot markets above which a system earns a second probe
	defaultSensingPurchaseCooldownSecs = 10
	// defaultSensingFreshnessTargetSecs is stamped on every standing post; the
	// reconciler's manning watchdog reads it, and the scout tour deliberately
	// STRETCHES each circuit to the post's target — so it must stay comfortably
	// UNDER the trade planner's 75-min firm-sink freshness cap (the [trade_fleet]
	// sink_freshness_max_minutes default, sp-tgll8 item 2). A target at or past
	// the cap paces every scanned market stale, and the fail-closed cap (RULINGS
	// #4, never weakened) then refuses every trade buy fleet-wide — the
	// outage, where adopted era-4 posts carried 3h targets. 3600 (1h) leaves a
	// 15-minute margin for scan jitter and tour turnaround.
	defaultSensingFreshnessTargetSecs = 3600
	defaultSensingWaitLowMs           = 50   // limiter wait at or under this: full scanning, discovery allowed
	defaultSensingWaitHighMs          = 1000 // limiter wait at or past this: scanning sheds toward the 0.5 floor
	defaultSensingMaxSpend            = 500_000
	defaultSensingSpendWindowSecs     = 3600
	defaultSensingDiscoveryDeclares   = 4 // sweep-once frontier declares per tick — paces propagation, never floods the reconciler
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

// SensingPostRepository is the coordinator's posts-table surface: the shared
// desired-state port plus the narrow live-post delta seam. EVERY live-post
// delta — resize, dormancy flip, hot-set stamp, freshness-target refresh — goes
// through UpdateSensingState, which touches only the four sensing-owned columns,
// so a write from this tick's snapshot can never clobber the manning/partition/
// respawn columns the scout reconciler writes concurrently, nor the min_hulls
// floor bootstrap stamps behind a once-latch. Upsert is for CREATES only.
// Satisfied by the GORM scout-post repository.
type SensingPostRepository interface {
	domainScouting.ScoutPostRepository
	UpdateSensingState(ctx context.Context, playerID int, systemSymbol string, hulls int, dormant bool, hotWaypoints []string, freshnessTarget time.Duration) error
}

// guardedBuyer is the shared fail-closed buy engine (probebuy.GuardedProbeBuyer):
// it runs the whole money-guard stack per call, so this coordinator never
// re-derives a spend decision of its own.
type guardedBuyer interface {
	MaybeBuy(ctx context.Context, playerID shared.PlayerID, demand, supply int, dryRun bool, target probebuy.ProbeTarget) probebuy.Outcome
}

// GateAdjacencyReader is the discovery pass's PURE STORE read of the gate
// graph: one Adjacency query, zero live API — a system absent from the stored
// adjacency is simply not counted, never fetched. The fetch-through
// Connections family must never be wired here (topology is least cached
// exactly where discovery looks). *gategraph.Service satisfies it.
type GateAdjacencyReader interface {
	Adjacency(ctx context.Context) (map[string][]system.GateEdge, error)
}

// WaypointCatalogReader is the discovery pass's swept-knowledge read: the
// persisted waypoint catalog. BuildSystemGraph persists a system's ENTIRE
// waypoint set the moment a probe sweeps it, while gate charting persists
// edges only — so one persisted NON-gate waypoint proves a real sweep (the
// frontier queue's Scanned discriminator). Market rows cannot carry this
// signal: a swept system with no marketplace anywhere never writes one.
// Satisfied by the GORM waypoint repository.
type WaypointCatalogReader interface {
	ListBySystem(ctx context.Context, systemSymbol string) ([]*shared.Waypoint, error)
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

	// DiscoveryDeclaresPerTick bounds the sweep-once frontier declares per
	// tick (the discovery_declares_per_tick config key).
	DiscoveryDeclaresPerTick int
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
	postRepo    SensingPostRepository
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

	// gateGraph is the discovery pass's stored-adjacency read, wired via
	// setter like treasury/purchaser. Nil keeps discovery entirely inert:
	// no sweep declares and no funded discovery demand.
	gateGraph GateAdjacencyReader

	// waypointCatalog is the discovery pass's swept-knowledge read, wired via
	// setter like gateGraph. Nil disables only the swept-marketless exclusion
	// (every not-in-census neighbour stays a candidate — the pre-seam shape).
	waypointCatalog WaypointCatalogReader

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
	postRepo SensingPostRepository,
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

// SetGateGraph wires the stored gate adjacency the discovery pass propagates
// over (a pure store read — wire *gategraph.Service, never a fetch-through
// resolver). Leaving it unset keeps discovery inert.
func (h *RunProbeSensingCoordinatorHandler) SetGateGraph(g GateAdjacencyReader) {
	h.gateGraph = g
}

// SetWaypointCatalog wires the persisted waypoint catalog the discovery pass
// reads swept-knowledge from (wire the waypoint repository, in the same
// breath as SetGateGraph). Leaving it unset keeps the swept-marketless
// exclusion off — every not-in-census neighbour stays a candidate.
func (h *RunProbeSensingCoordinatorHandler) SetWaypointCatalog(w WaypointCatalogReader) {
	h.waypointCatalog = w
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
	DiscoveryDeclares    int
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
		DiscoveryDeclares:    cmd.DiscoveryDeclaresPerTick,
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
	if c.DiscoveryDeclares <= 0 {
		c.DiscoveryDeclares = defaultSensingDiscoveryDeclares
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

	// hotBySystem is each census system's stage-2 circuit, stamped onto every
	// standing post below. Membership is goods-based only (see
	// SystemSensingProfile.HotWaypoints): depth gates whether a post EXISTS,
	// never which markets it circuits — a crushed market still deals its goods
	// and stays in the circuit while its prices recover.
	hotBySystem := make(map[string][]string, len(profiles))
	for _, profile := range profiles {
		hotBySystem[profile.System] = profile.HotWaypoints
	}

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
		wantHot := hotBySystem[system]
		existing := standingBySystem[system]
		if existing == nil {
			post := &domainScouting.ScoutPost{
				PlayerID:        cmd.PlayerID.Value(),
				SystemSymbol:    system,
				FreshnessTarget: cfg.FreshnessTarget,
				Kind:            domainScouting.PostKindStanding,
				Hulls:           plan.Hulls[system],
				Dormant:         wantDormant,
				HotWaypoints:    wantHot,
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
		if existing.HullBudget() == hulls && existing.Dormant == wantDormant && sameWaypointList(existing.HotWaypoints, wantHot) && existing.FreshnessTarget == cfg.FreshnessTarget {
			continue
		}
		// Narrow delta, never a full-row Upsert: this snapshot is a tick old, and
		// under saturation the rotation makes a delta land EVERY tick — a whole-row
		// write would clobber whatever the reconciler/bootstrap wrote since the read.
		// FreshnessTarget rides the delta: an adopted post carrying a dead
		// era's pacing target converges to the config target here — the scout tour
		// paces circuits to the post's target, so a target past the trade planner's
		// sink-freshness cap ages every market stale and trading fail-closes on the
		// buy. A converged post writes nothing (the zero-write guard above).
		if err := h.postRepo.UpdateSensingState(ctx, cmd.PlayerID.Value(), system, hulls, wantDormant, wantHot, cfg.FreshnessTarget); err != nil {
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
			// Kept, woken if dormant, and its hot set held census-true: a stale
			// restriction on the kept post would blind exactly the markets
			// stage 2 exists to watch (no whitelisted goods left ⇒ cleared ⇒
			// the tour flies its full circuit again). Its freshness target
			// converges too : a kept era-4 post pacing HOME's markets
			// past the trade sink-freshness cap starves home trading the same way.
			wantHot := hotBySystem[post.SystemSymbol]
			if !post.Dormant && sameWaypointList(post.HotWaypoints, wantHot) && post.FreshnessTarget == cfg.FreshnessTarget {
				continue
			}
			// Same narrow seam as the in-scope delta: the wake write may only touch
			// the sensing-owned columns, and it never shrinks the post.
			if err := h.postRepo.UpdateSensingState(ctx, cmd.PlayerID.Value(), post.SystemSymbol, post.HullBudget(), false, wantHot, cfg.FreshnessTarget); err != nil {
				logger.Log("WARNING", fmt.Sprintf("Failed to refresh floored sensing post %s: %v", post.SystemSymbol, err), nil)
				continue
			}
			upserts++
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

	// Discovery pass: propagate the frontier over the STORED gate adjacency —
	// census systems' uncharted neighbours become sweep-once declares, and
	// every open sweep-once post is one probe of funded demand. Gated on the
	// rotation's discovery verdict, so exploration sheds FIRST under pressure:
	// no declares, no funded demand, not even the store read.
	discoveryDemand, frontierSystem := 0, ""
	if discovery {
		discoveryDemand, frontierSystem = h.discoverFrontier(ctx, cmd, cfg, censusSystems(rows), sweepSystems, standingBySystem, now)
	}

	// Budgeted buy: demand is the plan total plus funded discovery, clamped by
	// N — the single budget dial. Supply counts every scout-type hull available
	// to scouting (idle, in-flight, or manning), so an en-route probe is never
	// double-bought.
	supply, err := h.probeSupply(ctx, cmd)
	if err != nil {
		return err
	}
	demand := plan.TotalHulls + discoveryDemand
	if demand > cfg.ProbeBudget {
		demand = cfg.ProbeBudget
	}

	// The buy hint serves the older demand first: an unmet standing post. Only
	// when the plan is fully manned does the buy aim AT the frontier — the
	// yard nearest a sweep candidate's parent, so the probe spawns one hop
	// from the system it will sweep.
	targetSystem := neediestSensingSystem(plan, standingBySystem)
	if targetSystem == "" {
		targetSystem = frontierSystem
	}
	buyer := h.newBuyer(cfg.Buy)
	target := probebuy.ProbeTarget{
		System:                    targetSystem,
		HopPenaltyCredits:         probebuy.DefaultHopPenaltyCredits,
		SiblingPriceMarginCredits: probebuy.DefaultSiblingPriceMarginCredits,
		ClaimOwnerContainerID:     cmd.ContainerID,
	}
	outcome := buyer.MaybeBuy(ctx, cmd.PlayerID, demand, supply, false, target)

	logger.Log("INFO", fmt.Sprintf("Probe sensing cycle: %d in-scope systems, %d hulls desired (+%d discovery), supply %d, share %.2f, %d dormant, discovery=%v — %s",
		len(inScope), plan.TotalHulls, discoveryDemand, supply, share, len(dormant), discovery, outcome.Reason), map[string]interface{}{
		"action":           "probe_sensing_cycle",
		"in_scope":         len(inScope),
		"hulls_desired":    plan.TotalHulls,
		"discovery_demand": discoveryDemand,
		"supply":           supply,
		"share":            share,
		"dormant":          len(dormant),
		"discovery":        discovery,
		"bought":           outcome.Bought,
		"upserts":          upserts,
		"removed":          removed,
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

// sameWaypointList reports element-wise equality of two waypoint lists (nil
// and empty are equal) — the hot-set delta guard, so a converged tick writes
// nothing. Both sides are coordinator-stamped sorted asc, so plain positional
// comparison is exact.
func sameWaypointList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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

// discoverFrontier is the branching-discovery pass: every census system's
// stored gate neighbour that is neither in census nor already postered becomes
// a sweep-once declare (bounded per tick), and every OPEN sweep-once post is
// one probe of discovery demand — the buyer funds one hull per open frontier
// direction. Returns that demand plus the frontier buy hint: the parent census
// system of the sorted-first open direction seen this walk, so the funded
// probe is bought at the yard nearest the frontier it will cross.
//
// The adjacency is the PURE STORE read — zero live API. Its verdicts mirror
// the stored-distance walk, no laxer than the strict resolver the relay
// flies: an uncached system is simply not counted, an under-construction edge
// is impassable, and a stale edge set (one Replace, one timestamp — one stale
// row condemns it) is not expanded through. A neighbour holding ANY post is
// excluded: Upsert is keyed by (player, system), so a sweep declare against a
// posted system would replace that row and wipe its manning. A neighbour the
// fleet has SWEPT (waypoint-catalog knowledge, checked last — it is the only
// check that costs a read) is known ground even with zero market rows: a
// swept-marketless system can never enter the market census, so without this
// exclusion it would loop declare → man → tour → retire → redeclare forever.
//
// The pass is speculative by definition, so every input failure degrades it
// to zero — no declares, no funded demand — without aborting the standing
// reconcile around it (a value returned alongside an error is never
// consumed). Sweeps already declared before a mid-walk failure stand: they
// are durable open posts the next healthy tick counts and funds.
func (h *RunProbeSensingCoordinatorHandler) discoverFrontier(
	ctx context.Context,
	cmd *RunProbeSensingCoordinatorCommand,
	cfg sensingConfig,
	census map[string]bool,
	sweepSystems map[string]bool,
	standingBySystem map[string]*domainScouting.ScoutPost,
	now time.Time,
) (demand int, frontierSystem string) {
	if h.gateGraph == nil {
		return 0, ""
	}
	logger := common.LoggerFromContext(ctx)
	adjacency, err := h.gateGraph.Adjacency(ctx)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Discovery pass skipped: failed to read stored gate adjacency: %v", err), nil)
		return 0, ""
	}

	parentsSorted := make([]string, 0, len(census))
	for parent := range census {
		parentsSorted = append(parentsSorted, parent)
	}
	sort.Strings(parentsSorted)

	declared := 0
	declaredSet := make(map[string]bool)
	sweptMemo := make(map[string]bool)     // per-pass catalog memo: two parents sharing a neighbour cost one read
	openParents := make(map[string]string) // open sweep system → its census parent (buy-hint anchor)
	for _, parent := range parentsSorted {
		edges := append([]system.GateEdge(nil), adjacency[parent]...)
		if sweepEdgeSetStale(edges) {
			continue
		}
		sort.Slice(edges, func(i, j int) bool { return edges[i].ConnectedSystem < edges[j].ConnectedSystem })
		for _, edge := range edges {
			neighbour := edge.ConnectedSystem
			if neighbour == "" || edge.UnderConstruction {
				continue
			}
			if census[neighbour] {
				continue // known ground whatever its depth — a sweep would re-scan it
			}
			if sweepSystems[neighbour] || declaredSet[neighbour] {
				if _, known := openParents[neighbour]; !known {
					openParents[neighbour] = parent
				}
				continue
			}
			if standingBySystem[neighbour] != nil {
				continue
			}
			if declared >= cfg.DiscoveryDeclares {
				continue // bounded per tick; the candidate re-derives next tick
			}
			swept, sweptErr := h.neighbourSwept(ctx, neighbour, sweptMemo)
			if sweptErr != nil {
				logger.Log("WARNING", fmt.Sprintf("Discovery pass stopped: failed to read waypoint catalog for %s: %v", neighbour, sweptErr), nil)
				return 0, ""
			}
			if swept {
				continue // swept and marketless: its markets were looked for and none exist
			}
			post := &domainScouting.ScoutPost{
				PlayerID:        cmd.PlayerID.Value(),
				SystemSymbol:    neighbour,
				FreshnessTarget: cfg.FreshnessTarget,
				Kind:            domainScouting.PostKindSweepOnce,
				Hulls:           1,
				CreatedAt:       now,
			}
			if err := h.postRepo.Upsert(ctx, post); err != nil {
				logger.Log("WARNING", fmt.Sprintf("Failed to declare sweep-once discovery post %s: %v", neighbour, err), nil)
				continue
			}
			declared++
			declaredSet[neighbour] = true
			openParents[neighbour] = parent
			logger.Log("INFO", fmt.Sprintf("Declared sweep-once discovery post %s (frontier of %s) — reconciler relays a probe; its arrival scan is the first scan", neighbour, parent), map[string]interface{}{
				"action":        "sensing_sweep_declared",
				"system_symbol": neighbour,
				"parent_system": parent,
			})
		}
	}

	openSweeps := make([]string, 0, len(openParents))
	for sweep := range openParents {
		openSweeps = append(openSweeps, sweep)
	}
	sort.Strings(openSweeps)
	if len(openSweeps) > 0 {
		frontierSystem = openParents[openSweeps[0]]
	}
	return len(sweepSystems) + declared, frontierSystem
}

// neighbourSwept reports whether the fleet has SWEPT the system, by the
// frontier queue's waypoint-derived discriminator: BuildSystemGraph persists
// a system's ENTIRE waypoint set the moment a probe sweeps it, while gate
// charting persists edges only — so one persisted NON-gate waypoint proves a
// real sweep (a lone jump-gate row is merely reachable, still frontier). An
// unwired catalog reads as not-swept (the exclusion stays off, the pre-seam
// shape); a read FAILURE surfaces so the caller declares nothing new — for a
// speculative spend, unreadable is refused, never guessed.
func (h *RunProbeSensingCoordinatorHandler) neighbourSwept(ctx context.Context, systemSymbol string, memo map[string]bool) (bool, error) {
	if h.waypointCatalog == nil {
		return false, nil
	}
	if swept, seen := memo[systemSymbol]; seen {
		return swept, nil
	}
	waypoints, err := h.waypointCatalog.ListBySystem(ctx, systemSymbol)
	if err != nil {
		return false, err
	}
	swept := sweptWaypointSet(waypoints)
	memo[systemSymbol] = swept
	return swept, nil
}

// sweptWaypointSet mirrors the frontier scanner's hasNonGateWaypoint rule:
// true iff at least one persisted waypoint is NOT a jump gate.
func sweptWaypointSet(waypoints []*shared.Waypoint) bool {
	for _, waypoint := range waypoints {
		if !waypoint.IsJumpGate() {
			return true
		}
	}
	return false
}

// censusSystems is the charted+scanned set: every system with ANY market
// cache row, whatever the good. The whitelist scopes STANDING sensing, not
// discovery — an already-scanned ore-only system is known ground and must
// never be re-swept (its goods never change, so a whitelist-filtered census
// would re-declare it forever).
func censusSystems(rows []domainScouting.MarketDepthRow) map[string]bool {
	census := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row.System == "" {
			continue
		}
		census[row.System] = true
	}
	return census
}

// sweepEdgeSetStale mirrors the stored-distance walk's staleness rule: a
// system's edges are written in one Replace under a single timestamp, so one
// stale row condemns the whole set — its onward gates are unverified.
func sweepEdgeSetStale(edges []system.GateEdge) bool {
	for _, e := range edges {
		if e.Stale {
			return true
		}
	}
	return false
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
