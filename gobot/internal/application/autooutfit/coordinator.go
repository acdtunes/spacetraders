// Package autooutfit is the guarded auto-outfit coordinator: the module
// analogue of hull acquisition. Each tick it MEASURES per-hull cargo saturation from
// tour_leg_telemetry, CATALOGS the modules available to buy off the persisted market
// cache, PICKS the highest-marginal-value (hull, module) pair (the pure
// domain/outfitting scorer), and — behind a fail-closed money/ceiling/cap guard stack —
// buys the module, delivers it, and installs it on the hull. It is a SECOND capacity
// actuator alongside the fleet autosizer's hull-buying: an upgrade is chosen over a new
// hull only when it is cheaper per unit of capacity gained.
//
// DEPLOY-INERT (mirrors the capacity reconciler, contrast the market-freshness sizer):
// nothing launches this at boot. It runs only when explicitly started and then survives
// restarts through the persisted-container recovery idiom. Every spend is guarded and
// every unreadable input fails CLOSED to a no-op, so a running coordinator can only
// upgrade a measured, affordable, role-matched hull with a free slot — or do nothing.
//
// The loop is idempotent and restart-safe: every decision is re-derived from persisted
// telemetry, market data, and fleet state each tick. The coordinator persists no state
// of its own (the watchlist dedupe is delegated to the captain event store).
package autooutfit

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/application/probebuy"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainOutfit "github.com/andrescamacho/spacetraders-go/internal/domain/outfitting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

const (
	// Config defaults (RULINGS #5: every operational value is a config key, filled here
	// only when neither the live container config nor the launch command carries one).
	defaultTickSeconds         = 300 // 5m — outfitting is not time-critical
	defaultMinTelemetrySamples = 8   // fail-closed thin-telemetry floor: 5 legs is too thin a sample
	defaultPriceCeiling        = 500000
	defaultMaxInstallsPerTick  = 1
	// defaultPaybackHorizonHours 0 = the absolute payback gate is OFF by default (the
	// value-per-hour it needs is unmeasured until per-hull throughput is wired).
	defaultPaybackHorizonHours    = 0
	defaultTreasuryReserve        = 50000 // hard working-capital floor, mirrors the outfitting handler
	defaultMaxTreasuryFractionPct = 25    // a single module never exceeds 25% of live treasury
	defaultInstallFeeEstimate     = 1000
	defaultHopCost                = 5000         // logistics cost per gate hop to divert to the module's market
	defaultTelemetryWindowSecs    = 12 * 60 * 60 // 12h read window, mirrors the heavy tour-rate window
)

// defaultWantedModules is the watchlist: the high-value capacity modules the catalog
// tracks and the coordinator announces on first appearance in reach.
var defaultWantedModules = []string{"MODULE_FUEL_TANK", "MODULE_CARGO_HOLD_II", "MODULE_CARGO_HOLD_III"}

// TelemetryReader reads persisted per-leg tour telemetry — the cargo-saturation source.
// Satisfied by *persistence.TourTelemetryRepositoryGORM.
type TelemetryReader interface {
	ListByPlayer(ctx context.Context, playerID int, since time.Time) ([]trading.TourLegTelemetry, error)
}

// FleetReader reads the player's hulls (capacity, role, module slots, location). Read-only.
type FleetReader interface {
	FindAllByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error)
}

// CatalogReader builds the module catalog off the persisted market cache: for each
// wanted module, the nearest market selling it, its price (the market ask), and reach
// hops. Fails CLOSED (readable=false) — an unreadable market surface yields no offers,
// so the tick installs nothing.
type CatalogReader interface {
	ReadCatalog(ctx context.Context, playerID int, systems, wanted []string) (offers []domainOutfit.ModuleOffer, readable bool, err error)
}

// Outfitter is the physical acquire+install actuation port: buy the module at the market,
// deliver it to the hull, install it (InstallModuleCommand). The coordinator calls it
// only after every guard passes; it returns the hull's post-install cargo capacity.
type Outfitter interface {
	AcquireAndInstall(ctx context.Context, playerID int, shipSymbol, moduleSymbol, buyAtWaypoint string) (installedCapacity int, err error)
}

// WatchlistNotifier announces a wanted module the FIRST time it enters reach (idempotent —
// announced=true only on the first sighting). Backed by the captain event store so the
// dedupe survives restarts without a table of its own.
type WatchlistNotifier interface {
	AnnounceInReach(ctx context.Context, playerID int, moduleSymbol, waypoint string, price int) (announced bool, err error)
}

// NewHullCostReader reads the autosizer buy-hull alternative: the $/unit-cargo-capacity
// of the cheapest new hauler. The relative payback gate refuses an upgrade that costs
// more per unit than this — buy-new is then the cheaper lever. readable=false leaves the
// comparison off (the standalone spend guards still bind).
type NewHullCostReader interface {
	CostPerUnitCapacity(ctx context.Context, playerID int) (costPerUnit float64, readable bool, err error)
}

// RunAutoOutfitCoordinatorCommand launches the standing coordinator for a player. All
// knobs are launch-config keys; <= 0 (or the zero value) falls back to the documented
// default. The tunable subset is also live-tunable (see AutoOutfitTunableDefaults).
type RunAutoOutfitCoordinatorCommand struct {
	PlayerID         shared.PlayerID
	ContainerID      string
	TickIntervalSecs int
	DryRun           bool

	MinTelemetrySamples    int
	PriceCeiling           int
	MaxInstallsPerTick     int
	PaybackHorizonHours    int
	TreasuryReserve        int
	MaxTreasuryFractionPct int
	InstallFeeEstimate     int
	HopCost                int
	TelemetryWindowSecs    int
	WantedModules          []string
}

// RunAutoOutfitCoordinatorResponse reports reconcile progress (observed only on shutdown).
type RunAutoOutfitCoordinatorResponse struct {
	Ticks  int
	Errors []string
}

// RunAutoOutfitCoordinatorHandler reconciles measured saturation against the module
// catalog every tick. A registered singleton (one instance serves every player's ticks);
// it holds no mutable per-player state, so it is restart-safe by construction.
type RunAutoOutfitCoordinatorHandler struct {
	telemetry TelemetryReader
	fleetRepo FleetReader
	catalog   CatalogReader
	clock     shared.Clock

	// Optional collaborators wired via setters (the codebase's optional-injection idiom).
	// A nil treasury or outfitter fails the INSTALL path closed; the coordinator still
	// measures, catalogs, and announces the watchlist without them.
	treasury    probebuy.TreasuryReader
	outfitter   Outfitter
	watchlist   WatchlistNotifier
	newHullCost NewHullCostReader

	liveConfig    liveconfig.Reader
	captainEvents captain.EventRecorder
}

// NewRunAutoOutfitCoordinatorHandler wires the coordinator's required read ports. clock
// defaults to the real clock when nil (production). The spend/actuation collaborators are
// optional and injected separately.
func NewRunAutoOutfitCoordinatorHandler(
	telemetry TelemetryReader,
	fleetRepo FleetReader,
	catalog CatalogReader,
	clock shared.Clock,
) *RunAutoOutfitCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunAutoOutfitCoordinatorHandler{
		telemetry: telemetry,
		fleetRepo: fleetRepo,
		catalog:   catalog,
		clock:     clock,
	}
}

// SetTreasuryReader wires the live-treasury source for the money guards. Unset keeps the
// INSTALL path fail-closed.
func (h *RunAutoOutfitCoordinatorHandler) SetTreasuryReader(t probebuy.TreasuryReader) {
	h.treasury = t
}

// SetOutfitter wires the physical buy+deliver+install actuator. Unset keeps INSTALL closed.
func (h *RunAutoOutfitCoordinatorHandler) SetOutfitter(o Outfitter) { h.outfitter = o }

// SetWatchlistNotifier wires the first-sighting announcer.
func (h *RunAutoOutfitCoordinatorHandler) SetWatchlistNotifier(w WatchlistNotifier) { h.watchlist = w }

// SetNewHullCostReader wires the autosizer buy-hull payback baseline.
func (h *RunAutoOutfitCoordinatorHandler) SetNewHullCostReader(r NewHullCostReader) {
	h.newHullCost = r
}

// SetLiveConfigReader wires the per-tick live-config snapshot source. Unset
// keeps every knob launch-frozen.
func (h *RunAutoOutfitCoordinatorHandler) SetLiveConfigReader(r liveconfig.Reader) { h.liveConfig = r }

// SetEventRecorder wires the captain outbox for the install + error-loop events.
func (h *RunAutoOutfitCoordinatorHandler) SetEventRecorder(rec captain.EventRecorder) {
	h.captainEvents = rec
}

// Handle runs the reconcile loop until the context is cancelled.
func (h *RunAutoOutfitCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	logger := common.LoggerFromContext(ctx)
	cmd, ok := request.(*RunAutoOutfitCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type %T", request)
	}

	tick := time.Duration(cmd.TickIntervalSecs) * time.Second
	if tick <= 0 {
		tick = defaultTickSeconds * time.Second
	}

	result := &RunAutoOutfitCoordinatorResponse{Errors: []string{}}
	logger.Log("INFO", fmt.Sprintf("Auto-outfit coordinator starting (tick %s, dry_run=%v)", tick, cmd.DryRun), map[string]interface{}{
		"action": "auto_outfit_start", "container_id": cmd.ContainerID, "dry_run": cmd.DryRun,
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
			logger.Log("ERROR", fmt.Sprintf("Auto-outfit reconcile failed: %v", err), nil)
		}
		if streak, crossed := errMon.Note("reconcile", errString(err)); crossed {
			health.RecordErrorLoop(h.captainEvents, logger, cmd.ContainerID, cmd.PlayerID.Value(), "reconcile", err, streak)
		}
		result.Ticks++

		select {
		case <-time.After(tick):
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ReconcileOnce is one reconcile pass — the unit the tests drive directly. It measures
// per-hull saturation, catalogs available modules, announces watchlist first-sightings,
// then installs the highest-marginal-value pair(s) behind the guard stack. Every
// unreadable input fails CLOSED to a no-op (nothing installed), never a hard error.
func (h *RunAutoOutfitCoordinatorHandler) ReconcileOnce(ctx context.Context, cmd *RunAutoOutfitCoordinatorCommand) error {
	logger := common.LoggerFromContext(ctx)
	cfg := resolveAutoOutfitConfig(cmd, h.liveConfigSnapshot(ctx, cmd))

	ships, err := h.fleetRepo.FindAllByPlayer(ctx, cmd.PlayerID)
	if err != nil {
		return fmt.Errorf("failed to read fleet: %w", err)
	}

	since := h.clock.Now().Add(-cfg.TelemetryWindow)
	legs, err := h.telemetry.ListByPlayer(ctx, cmd.PlayerID.Value(), since)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Tour telemetry unreadable — no auto-outfit this tick (fail-closed): %v", err), nil)
		return nil
	}

	facts := buildHullFacts(ships)
	bottlenecks := domainOutfit.AggregateBottlenecks(toLegSaturation(legs), facts)

	systems := distinctSystems(ships)
	offers := h.readCatalog(ctx, cmd, cfg, systems)
	h.announceWatchlist(ctx, cmd, cfg, offers)

	cfg.Selection.NewHullCostPerUnit = h.newHullCostPerUnit(ctx, cmd)

	installed := h.installUpgrades(ctx, cmd, cfg, bottlenecks, offers)
	logger.Log("INFO", fmt.Sprintf("Auto-outfit cycle: %d hulls measured, %d catalog offers, %d installed (dry_run=%v)", len(bottlenecks), len(offers), installed, cmd.DryRun), map[string]interface{}{
		"action": "auto_outfit_cycle", "hulls": len(bottlenecks), "offers": len(offers), "installed": installed, "dry_run": cmd.DryRun,
	})
	return nil
}

// installUpgrades selects and installs up to MaxInstallsPerTick pairs, each the current
// highest-marginal-value pick over the hulls not yet upgraded this tick. A guard refusal
// stops the tick (the top pick is unaffordable/blocked; retrying a worse pick would only
// spend more). Returns the count installed.
func (h *RunAutoOutfitCoordinatorHandler) installUpgrades(ctx context.Context, cmd *RunAutoOutfitCoordinatorCommand, cfg autoOutfitConfig, bottlenecks []domainOutfit.HullBottleneck, offers []domainOutfit.ModuleOffer) int {
	if len(offers) == 0 {
		return 0
	}
	upgraded := map[string]bool{}
	installed := 0
	for installed < cfg.MaxInstallsPerTick {
		pick, ok := domainOutfit.SelectUpgrade(remainingHulls(bottlenecks, upgraded), offers, cfg.Selection)
		if !ok {
			return installed
		}
		if !h.guardedInstall(ctx, cmd, cfg, pick) {
			return installed
		}
		upgraded[pick.ShipSymbol] = true
		installed++
	}
	return installed
}

// guardedInstall runs the fail-closed money/ceiling guard stack for one pick and, when
// every guard passes and it is not a dry run, actuates the buy+install. Returns true iff
// a hull was actually upgraded.
func (h *RunAutoOutfitCoordinatorHandler) guardedInstall(ctx context.Context, cmd *RunAutoOutfitCoordinatorCommand, cfg autoOutfitConfig, pick domainOutfit.UpgradePick) bool {
	logger := common.LoggerFromContext(ctx)
	price := pick.Module.Price

	if h.treasury == nil {
		logger.Log("WARNING", "Auto-outfit parked: no treasury reader wired (fail-closed)", nil)
		return false
	}
	credits, err := h.treasury.LiveCredits(ctx, cmd.PlayerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Auto-outfit parked: treasury unreadable (fail-closed): %v", err), nil)
		return false
	}
	if credits-price-cfg.Selection.InstallFeeEstimate < cfg.TreasuryReserve {
		logger.Log("INFO", fmt.Sprintf("Auto-outfit parked %s on %s: spend would drop treasury %d below the %d reserve", pick.Module.Symbol, pick.ShipSymbol, credits, cfg.TreasuryReserve), nil)
		return false
	}
	if price*100 > credits*cfg.MaxTreasuryFractionPct {
		logger.Log("INFO", fmt.Sprintf("Auto-outfit parked %s on %s: price %d exceeds %d%% of treasury %d", pick.Module.Symbol, pick.ShipSymbol, price, cfg.MaxTreasuryFractionPct, credits), nil)
		return false
	}
	if price > cfg.PriceCeiling {
		logger.Log("INFO", fmt.Sprintf("Auto-outfit parked %s on %s: price %d over the %d ceiling", pick.Module.Symbol, pick.ShipSymbol, price, cfg.PriceCeiling), nil)
		return false
	}

	if cmd.DryRun {
		logger.Log("INFO", fmt.Sprintf("DRY-RUN would install %s on %s (buy at %s for %d, cost/unit %.1f)", pick.Module.Symbol, pick.ShipSymbol, pick.Module.Waypoint, price, pick.CostPerUnitCapacity), map[string]interface{}{
			"action": "auto_outfit_would_install", "ship": pick.ShipSymbol, "module": pick.Module.Symbol,
		})
		return false
	}

	if h.outfitter == nil {
		logger.Log("WARNING", "Auto-outfit parked: no outfitter wired (fail-closed)", nil)
		return false
	}
	capacity, err := h.outfitter.AcquireAndInstall(ctx, cmd.PlayerID.Value(), pick.ShipSymbol, pick.Module.Symbol, pick.Module.Waypoint)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Auto-outfit install of %s on %s failed (fail-closed): %v", pick.Module.Symbol, pick.ShipSymbol, err), nil)
		return false
	}
	logger.Log("INFO", fmt.Sprintf("Auto-outfit installed %s on %s (bought at %s for %d, capacity now %d)", pick.Module.Symbol, pick.ShipSymbol, pick.Module.Waypoint, price, capacity), map[string]interface{}{
		"action": "auto_outfit_installed", "ship": pick.ShipSymbol, "module": pick.Module.Symbol, "price": price, "capacity": capacity,
	})
	h.recordInstall(ctx, cmd, pick, price, capacity)
	return true
}

// remainingHulls filters out the hulls already upgraded this tick so a second install
// re-selects among fresh candidates.
func remainingHulls(bottlenecks []domainOutfit.HullBottleneck, upgraded map[string]bool) []domainOutfit.HullBottleneck {
	if len(upgraded) == 0 {
		return bottlenecks
	}
	out := make([]domainOutfit.HullBottleneck, 0, len(bottlenecks))
	for _, b := range bottlenecks {
		if upgraded[b.ShipSymbol] {
			continue
		}
		out = append(out, b)
	}
	return out
}

// readCatalog reads the module catalog, failing CLOSED (nil offers) on an unreadable or
// erroring market surface.
func (h *RunAutoOutfitCoordinatorHandler) readCatalog(ctx context.Context, cmd *RunAutoOutfitCoordinatorCommand, cfg autoOutfitConfig, systems []string) []domainOutfit.ModuleOffer {
	if h.catalog == nil {
		return nil
	}
	offers, readable, err := h.catalog.ReadCatalog(ctx, cmd.PlayerID.Value(), systems, cfg.WantedModules)
	if err != nil || !readable {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Module catalog unreadable — no auto-outfit this tick (fail-closed): %v", err), nil)
		return nil
	}
	return offers
}

// announceWatchlist emits a first-sighting announcement for every wanted module the
// catalog shows in reach. Idempotent at the notifier (announced=true only once).
func (h *RunAutoOutfitCoordinatorHandler) announceWatchlist(ctx context.Context, cmd *RunAutoOutfitCoordinatorCommand, cfg autoOutfitConfig, offers []domainOutfit.ModuleOffer) {
	if h.watchlist == nil {
		return
	}
	logger := common.LoggerFromContext(ctx)
	wanted := stringSet(cfg.WantedModules)
	for _, offer := range offers {
		if !wanted[offer.Symbol] {
			continue
		}
		announced, err := h.watchlist.AnnounceInReach(ctx, cmd.PlayerID.Value(), offer.Symbol, offer.Waypoint, offer.Price)
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to announce watchlist module %s: %v", offer.Symbol, err), nil)
			continue
		}
		if announced {
			logger.Log("INFO", fmt.Sprintf("Watchlist module %s entered reach at %s (price %d)", offer.Symbol, offer.Waypoint, offer.Price), map[string]interface{}{
				"action": "auto_outfit_watchlist_in_reach", "module": offer.Symbol, "waypoint": offer.Waypoint, "price": offer.Price,
			})
		}
	}
}

// newHullCostPerUnit reads the autosizer buy-hull payback baseline, 0 (gate off) on an
// unreadable surface.
func (h *RunAutoOutfitCoordinatorHandler) newHullCostPerUnit(ctx context.Context, cmd *RunAutoOutfitCoordinatorCommand) float64 {
	if h.newHullCost == nil {
		return 0
	}
	costPerUnit, readable, err := h.newHullCost.CostPerUnitCapacity(ctx, cmd.PlayerID.Value())
	if err != nil || !readable {
		return 0
	}
	return costPerUnit
}

// recordInstall emits the deferred captain event for a real spend (an install moves
// treasury — the captain must be able to see and audit it). Nil-safe.
func (h *RunAutoOutfitCoordinatorHandler) recordInstall(ctx context.Context, cmd *RunAutoOutfitCoordinatorCommand, pick domainOutfit.UpgradePick, price, capacity int) {
	if h.captainEvents == nil {
		return
	}
	payload := fmt.Sprintf(`{"ship":%q,"module":%q,"price":%d,"capacity":%d,"cost_per_unit":%.1f}`, pick.ShipSymbol, pick.Module.Symbol, price, capacity, pick.CostPerUnitCapacity)
	_ = h.captainEvents.Record(ctx, &captain.Event{
		Type:     captain.EventAutoOutfitInstalled,
		Ship:     pick.ShipSymbol,
		PlayerID: cmd.PlayerID.Value(),
		Payload:  payload,
	})
}

// buildHullFacts folds each ship into the per-hull static context the aggregator needs:
// role classification, cargo capacity, and free module slots (frame slots minus the
// weighted slots the installed modules already use).
func buildHullFacts(ships []*navigation.Ship) map[string]domainOutfit.HullFacts {
	facts := make(map[string]domainOutfit.HullFacts, len(ships))
	for _, ship := range ships {
		facts[ship.ShipSymbol()] = domainOutfit.HullFacts{
			Role:            ship.Role(),
			IsCargoHauler:   isCargoHauler(ship.Role()),
			CargoCapacity:   ship.CargoCapacity(),
			FreeModuleSlots: ship.ModuleSlots() - navigation.ModuleSlotsUsed(ship),
			// IsRangeConstrained + ThroughputPerHour are enriched once the refuel-per-leg
			// and legs-per-hour signals are aggregated (banked): the domain scorer already
			// supports both, so wiring them widens this coordinator to fuel-tank upgrades
			// and throughput-weighted ranking without a scorer change.
		}
	}
	return facts
}

// isCargoHauler reports whether a role can carry cargo (a CARGO_HOLD candidate). A
// scout/SATELLITE cannot; the COMMAND frigate can (last-resort hauler).
func isCargoHauler(role string) bool {
	return role == "HAULER" || role == "COMMAND"
}

// toLegSaturation maps persisted tour telemetry to the slim per-leg shape the domain
// aggregator folds, keeping the domain scorer free of the trading package.
func toLegSaturation(legs []trading.TourLegTelemetry) []domainOutfit.LegSaturation {
	out := make([]domainOutfit.LegSaturation, len(legs))
	for i, leg := range legs {
		out[i] = domainOutfit.LegSaturation{ShipSymbol: leg.ShipSymbol, RealizedUnits: leg.RealizedUnits, IsBuy: leg.IsBuy}
	}
	return out
}

// distinctSystems returns the distinct systems the player's hulls occupy — the trading
// grounds the catalog scans for reachable module sources.
func distinctSystems(ships []*navigation.Ship) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(ships))
	for _, ship := range ships {
		loc := ship.CurrentLocation()
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

func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, v := range values {
		set[v] = true
	}
	return set
}
