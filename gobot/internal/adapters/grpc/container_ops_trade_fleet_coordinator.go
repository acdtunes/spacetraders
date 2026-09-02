package grpc

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// TradeFleetCoordinator starts the standing trade-fleet coordinator (sp-1278): a
// recovery-safe container that watches 'trade'-dedicated hulls and relaunches a
// continuous tour on each hull parked by an honest tour exit, after a cooldown —
// retiring the captain's hand-relaunch loop.
//
// It mirrors ScoutPostCoordinator's shape exactly (build config → buildCommandForType
// so creation and recovery share one builder → NewContainer with iterations=-1 for the
// infinite reconcile loop → Add → runner → registerContainer → go Start). All the tuning
// (enabled/cooldown/max-concurrent/per-tour caps) is resolved LIVE from config.yaml's
// [trade_fleet] section inside buildCommandForType (resolveTradeFleetConfig), so the
// launch config carries only the identity here; a config edit + restart retunes even a
// recovered coordinator (sp-ts82 live-config pattern).
func (s *DaemonServer) TradeFleetCoordinator(ctx context.Context, playerID int, agentSymbol string) (string, error) {
	containerID := utils.GenerateContainerID("trade_fleet_coordinator", fmt.Sprintf("player-%d", playerID))

	// Identity only — the [trade_fleet] knobs are injected by resolveTradeFleetConfig
	// inside buildCommandForType (below), the single injection point shared by creation
	// and restart recovery.
	config := map[string]interface{}{
		"container_id": containerID,
		"agent_symbol": agentSymbol,
	}

	cmd, err := s.buildCommandForType("trade_fleet_coordinator", config, playerID, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to create trade fleet coordinator command: %w", err)
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeTradeFleetCoordinator,
		playerID,
		-1,  // Infinite iterations (reconcile loop) — NOT a CoordinatorOwnsIterations type
		nil, // No parent container
		config,
		nil,
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "trade_fleet_coordinator"); err != nil {
		return "", fmt.Errorf("failed to persist trade fleet coordinator container: %w", err)
	}

	s.startContainerRunner(containerEntity, cmd, containerID, "Trade fleet coordinator container")

	return containerID, nil
}

// LaunchTour implements tradingCmd.TourLauncher (sp-1278): it starts one continuous
// tour for an idle trade hull by delegating to the EXACT path `workflow tour-run` uses
// (StartTourRun). That inherits every safety property the coordinator must not
// re-implement — the sg35 operation="trade" stamp, the atomic operation-checked
// ClaimShip (a busy / captain-reserved / foreign-dedicated hull is rejected at the DB),
// the single-writer recovery-safe container row, and release-on-death — so the
// coordinator stays a pure decision loop that claims nothing itself (RULINGS #3/#7).
func (s *DaemonServer) LaunchTour(ctx context.Context, spec tradingCmd.TourLaunchSpec) (string, error) {
	// sp-nxrt escalate-to-movement: a hull the coordinator flagged after its 2nd
	// consecutive fast-fail is relaunched with reposition-reach armed for THIS tour, so it
	// moves to a fresh system instead of the coordinator sleeping longer on a dead lane.
	// nil for a normal relaunch — byte-identical to today's config-only launch.
	// A hull tagged "trade-mvt" rides the same seam onto the MVT path (spec §8).
	var overrides *TourRunOverrides
	mvtLoop := spec.Fleet == "trade-mvt"
	if spec.RepositionReachEscalated || mvtLoop {
		overrides = &TourRunOverrides{
			RepositionReachEnabled: spec.RepositionReachEscalated,
			MVTLoop:                mvtLoop,
		}
	}
	result, err := s.StartTourRun(
		ctx,
		spec.ShipSymbol,
		spec.MaxHops,
		spec.MaxSpend,
		spec.MinMargin,
		spec.ReplanLimit,
		spec.WorkingCapitalReserve,
		spec.AgentSymbol,
		spec.Iterations,
		spec.PlayerID,
		overrides,
	)
	if err != nil {
		return "", err
	}
	return result.ContainerID, nil
}

// LastTourProgress implements tradingCmd.TourLivenessPort (sp-m3122): for each running trade
// tour container it reports the timestamp of the tour's most recent real activity — the newest
// line in its container-log trail. That trail advances on every real step (plan/navigate/
// arrive/buy/sell) but NOT on the wall-clock heartbeat (which writes no log on success), so it
// stalls the instant a tour goes silent — the persistent signal the liveness watchdog compares
// to the stall threshold to tell a working tour from a RUNNING-but-hung one. It READS the
// container_logs table the daemon single-writes; it mutates nothing. Built on demand over s.db
// (the "repositories on demand" pattern), like the CLI's container-log reader.
func (s *DaemonServer) LastTourProgress(ctx context.Context, playerID shared.PlayerID, containerIDs []string) (map[string]time.Time, error) {
	return persistence.NewGormContainerLogRepository(s.db, nil).LatestLogTimestamps(ctx, playerID.Value(), containerIDs)
}

// StopTour implements tradingCmd.TourStopper (sp-m3122): it kills a hung tour container through
// the daemon's normal StopContainer path — the automated form of the captain's manual
// `container stop <hung-tour>` that cleared the incident by hand. StopContainer stops the
// runner and releases the hull's claim, so the coordinator's fresh relaunch re-claims it
// cleanly. reason is recorded by the watchdog's own log line at the call site. The error (if
// any) is surfaced to the coordinator, which then does NOT relaunch (fail-closed: a hull whose
// hung container could not be killed must not be re-launched under a still-live doomed one).
func (s *DaemonServer) StopTour(_ context.Context, containerID, reason string) error {
	_ = reason // logged by the watchdog caller; StopContainer records the STOPPED row itself
	return s.StopContainer(containerID)
}

// deadContainerAbsorptionReclaimer adapts the absorption ledger's dead-container sweep to the
// trade-fleet coordinator's DeadContainerAbsorptionReclaimer port (sp-m3122 part 3). The
// ledger's Sweep reclaims TTL-expired PLANNED, hard-cap-expired EXECUTED, AND dead-container
// PLANNED holds; the last (keyed on the live-container set) is the part that promptly clears
// reservations left by a restart or by a just-killed hung tour, rather than waiting for the
// next reserve to trigger the sweep.
type deadContainerAbsorptionReclaimer struct {
	ledger *persistence.AbsorptionLedgerGORM
}

// NewDeadContainerAbsorptionReclaimer wires the ledger sweep behind the coordinator's reclaim
// port (sp-m3122 part 3).
func NewDeadContainerAbsorptionReclaimer(ledger *persistence.AbsorptionLedgerGORM) tradingCmd.DeadContainerAbsorptionReclaimer {
	return &deadContainerAbsorptionReclaimer{ledger: ledger}
}

func (a *deadContainerAbsorptionReclaimer) ReclaimDeadContainerAbsorption(ctx context.Context, playerID shared.PlayerID) (int, error) {
	return a.ledger.Sweep(ctx, playerID.Value())
}

// tradeFleetConfigKeys enumerates every launch-config key the [trade_fleet] knobs
// occupy. resolveTradeFleetConfig clears these before re-injecting the live values, so
// a stale persisted copy from a prior boot can never shadow the current config.yaml
// (the sp-ts82 live-config discipline). Keep in lockstep with injectTradeFleetConfig
// and buildTradeFleetCoordinatorCommand's reads. Note: container_id and agent_symbol
// are the coordinator's IDENTITY (set once at creation) and are deliberately NOT in
// this list — they must survive a rebuild untouched.
var tradeFleetConfigKeys = []string{
	"trade_fleet_disabled",
	"trade_fleet_cooldown_secs",
	"trade_fleet_max_concurrent",
	"trade_fleet_tick_secs",
	"trade_fleet_max_hops",
	"trade_fleet_max_spend",
	"trade_fleet_min_margin",
	"trade_fleet_replan_limit",
	"trade_fleet_reserve",
	"trade_fleet_relaunch_backoff_max_minutes",
	"trade_fleet_masspark_exempt_disabled",
	"trade_fleet_masspark_window_seconds",
	"trade_fleet_masspark_min_hulls",
	"trade_fleet_watchdog_stall_secs",
	"trade_fleet_full_hull_pause_pct",
	// MVT specialist pool: [trade_fleet]-owned despite the unprefixed key names.
	"specialist_fraction_pct",
	"fat_lane_multiple_pct",
	"specialist_cadence_minutes",
}

// resolveTradeFleetConfig makes config.yaml the single LIVE source of truth for the
// trade-fleet coordinator's knobs (sp-1278, mirroring resolveIdleArbConfig). It clears
// any trade_fleet_* keys already in the launch config (stale copies persisted at a
// prior boot) and re-injects the daemon's boot-loaded values, so the rebuilt command
// reflects the CURRENT config.yaml on every build — creation and restart recovery
// alike. The clear is what lets dropping a knob from config.yaml fall back to the
// coordinator's own default rather than being shadowed by the now-absent live value.
func (s *DaemonServer) resolveTradeFleetConfig(config map[string]interface{}) {
	for _, key := range tradeFleetConfigKeys {
		delete(config, key)
	}
	s.injectTradeFleetConfig(config)
}

// injectTradeFleetConfig writes the [trade_fleet] knobs from config.yaml
// (s.tradeFleetConfig) into a coordinator container's launch config. Only keys the
// captain actually set (non-zero) are written, so an unset knob defers to the
// coordinator's own documented default (RULINGS #5 — the daemon never hardcodes the
// operational values). Enabled is inverted to trade_fleet_disabled, written ONLY when
// the coordinator is off: an absent key therefore reads as enabled, so the default-ON
// intent survives both a fresh start and a recovery from an old config that predates
// the key.
func (s *DaemonServer) injectTradeFleetConfig(config map[string]interface{}) {
	tf := s.tradeFleetConfig
	if !tf.EnabledOrDefault() {
		config["trade_fleet_disabled"] = true
	}
	if tf.CooldownSeconds != 0 {
		config["trade_fleet_cooldown_secs"] = tf.CooldownSeconds
	}
	if tf.MaxConcurrentTours != 0 {
		config["trade_fleet_max_concurrent"] = tf.MaxConcurrentTours
	}
	if tf.TickSeconds != 0 {
		config["trade_fleet_tick_secs"] = tf.TickSeconds
	}
	if tf.MaxHops != 0 {
		config["trade_fleet_max_hops"] = tf.MaxHops
	}
	// The two int64 caps are stored as int for directness: intValue now coerces int64
	// too (sp-ggk2 fixed the omission that silently read a native int64 back as 0 on the
	// creation path), but int keeps the on-disk form identical across the native and JSON
	// paths. int is 64-bit on the daemon's target, so a credit cap never overflows.
	if tf.MaxSpend != 0 {
		config["trade_fleet_max_spend"] = int(tf.MaxSpend)
	}
	if tf.MinMargin != 0 {
		config["trade_fleet_min_margin"] = tf.MinMargin
	}
	if tf.ReplanLimit != 0 {
		config["trade_fleet_replan_limit"] = tf.ReplanLimit
	}
	if tf.WorkingCapitalReserve != 0 {
		config["trade_fleet_reserve"] = int(tf.WorkingCapitalReserve)
	}
	// sp-1pli: unset defers to the coordinator's own default ceiling (30 min).
	if tf.RelaunchBackoffMaxMinutes != 0 {
		config["trade_fleet_relaunch_backoff_max_minutes"] = tf.RelaunchBackoffMaxMinutes
	}
	// sp-nkci: the restart-mass-park exemption is live by default — only write the disable
	// flag when the captain actually set it, so an absent key reads as ON (like Enabled).
	if tf.MassParkExemptDisabled {
		config["trade_fleet_masspark_exempt_disabled"] = true
	}
	if tf.MassParkWindowSeconds != 0 {
		config["trade_fleet_masspark_window_seconds"] = tf.MassParkWindowSeconds
	}
	if tf.MassParkMinHulls != 0 {
		config["trade_fleet_masspark_min_hulls"] = tf.MassParkMinHulls
	}
	// sp-m3122: the watchdog is always ARMED (no on/off key); only the stall threshold is
	// tunable — an absent key defers to the coordinator's 12-min default.
	if tf.WatchdogStallSeconds != 0 {
		config["trade_fleet_watchdog_stall_secs"] = tf.WatchdogStallSeconds
	}
	// sp-tgll8: the inventory-pressure governor is always ARMED (no on/off key); only the
	// FULL-hull pause threshold is tunable — an absent key defers to the coordinator's 65%
	// default.
	if tf.FullHullPausePct != 0 {
		config["trade_fleet_full_hull_pause_pct"] = tf.FullHullPausePct
	}
	// MVT specialist pool: an absent key defers to the coordinator's spec default (RULINGS #5).
	if tf.SpecialistFractionPct > 0 {
		config["specialist_fraction_pct"] = tf.SpecialistFractionPct
	}
	if tf.FatLaneMultiplePct > 0 {
		config["fat_lane_multiple_pct"] = tf.FatLaneMultiplePct
	}
	if tf.SpecialistCadenceMinutes > 0 {
		config["specialist_cadence_minutes"] = tf.SpecialistCadenceMinutes
	}
}
