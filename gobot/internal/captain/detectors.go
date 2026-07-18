package watchkeeper

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

type DetectorConfig struct {
	PlayerID int
	// ShipIdle is retained for config plumbing (CaptainConfig back-compat) but,
	// as of sp-6g96, no longer drives ship.idle's dedup window: idle firing is
	// now deduped by IdleEpisodes (fire once per continuous idle episode, a
	// state-change not an elapsed-time cooldown). See detectIdleShips.
	ShipIdle time.Duration
	// IdleEpisodes (sp-6g96) replaces a rolling-time-window cooldown for
	// ship.idle. A nil tracker is safe (no dedup) — matches every caller from
	// before sp-6g96 that does not set this field — while the Supervisor wires
	// its own long-lived instance so the alarm fires once per continuous idle
	// episode instead of re-arming purely because time elapsed while the ship
	// never actually left idle.
	IdleEpisodes      *episodeTracker
	StaleHeartbeat    time.Duration
	CreditsThresholds []int
	LastCredits       int // credits at the previous poll; 0 disables crossing detection
	// CurrentCreditsValue is this poll's credits, supplied by the supervisor
	// (sp-sk68 D4) so the crossing detector and the wake gate evaluate the
	// SAME number and the detector has no independent DB failure mode.
	CurrentCreditsValue int

	IncomeStall     time.Duration // 0 disables income-stall detection
	StreamDown      time.Duration // 0 disables stream-down detection
	ExpectedStreams []string      // container-type prefixes expected to be RUNNING; empty disables

	// FactoryIncomeStall (sp-7vos) is the per-goods-factory zero-income window,
	// kept SEPARATE from IncomeStall on purpose (RULINGS #5). The aggregate/
	// per-engine IncomeStall window (2h) is bounded below by lumpy CONTRACT
	// payouts; goods factories are a different earner, so their window is its
	// own knob and can be tuned without loosening contract detection. <= 0
	// disables the per-factory detector. Because a factory's sale cadence is
	// itself lumpy — it sells a batch, then spends an hour-plus reacquiring and
	// re-manufacturing inputs before the next batch (observed healthy gaps reach
	// ~2h) — this must stay comfortably above the normal inter-batch gap or it
	// cries wolf; hence a conservative default (see defaultFactoryIncomeStall).
	FactoryIncomeStall time.Duration

	// Crash-loop detection (sp-no9i): a single container.crashed is self-healing
	// (auto-restart+resume) and deferred; a container that dies CrashLoopThreshold
	// times within CrashLoopWindow is a genuine loop worth an interrupt. Either
	// field <= 0 disables the detector.
	CrashLoopWindow    time.Duration
	CrashLoopThreshold int

	// RegimeTripwires (sp-zlfv): captain-declared price tripwires, loaded
	// fresh each tick from RegimePolicy. Empty disables the price-regime
	// detector entirely — no config means no scan.
	RegimeTripwires []RegimeTripwire

	// PinnedHullContainerless (sp-v63s watchdog): how long a fleet-dedicated hull
	// may sit with no running container before it fires an interrupt-class
	// hull.containerless event. <= 0 disables the detector.
	PinnedHullContainerless time.Duration

	// ContainerlessEpisodes (sp-6g96) replaces a rolling-time-window cooldown
	// for hull.containerless, mirroring IdleEpisodes above: nil is safe (no
	// dedup), and the Supervisor wires its own long-lived instance so a
	// stranded pinned hull wakes the captain once per continuous stranding
	// episode instead of re-alarming purely because a cooldown window lapsed
	// while the hull never actually recovered. See detectContainerlessPinnedHulls.
	ContainerlessEpisodes *episodeTracker

	// StandingCoordinatorFleets (sp-jetm) parametrizes which DedicatedFleet tags
	// are pool-managed by a standing coordinator container rather than pinned
	// 1:1 to a single always-running container (RULINGS #5). A hull pinned to
	// one of these fleets legitimately sits containerless BETWEEN claims — the
	// coordinator owns the pool and its own loss modes are covered by
	// income-stall detection — so detectContainerlessPinnedHulls exempts it
	// while that fleet's coordinator has a RUNNING container. Empty disables
	// the exemption entirely (every dedicated hull is treated as 1:1-pinned,
	// the original sp-v63s behavior). This cannot be derived from the
	// containers table: the coordinator's launch config carries no fleet-name
	// field (see run_fleet_coordinator.go's dedicatedFleetContract, an
	// unexported Go constant, not persisted data) — so the association is
	// asserted here as the well-known standing-coordinator fleet(s), the same
	// shape as incomeEngines below.
	StandingCoordinatorFleets []StandingCoordinatorFleet

	// sp-k7q5 layer 2 (scout.staleness_hiding_revenue): fires when a market-rich
	// system has enough markets aged past the tour-planner cap that the planner is
	// dropping their lanes — staleness is actively hiding tradeable revenue, the
	// XT71/UQ87 class the exclusion counter's Grafana view surfaces and this detector
	// wakes on. All three thresholds must be > 0 to enable; any <= 0 disables it (and,
	// with layer 3 also off, skips the market_data scan entirely).
	StalenessHidingStaleAge         time.Duration // a market whose newest scan predates this is being dropped by the planner
	StalenessHidingMinPricedMarkets int           // system must carry at least this many priced markets to qualify
	StalenessHidingThreshold        int           // fire once at least this many of them are stale
	StalenessHidingCooldown         time.Duration // re-fire suppression window (<= 0 reuses StalenessHidingStaleAge)

	// sp-k7q5 layer 3 (scout.post_proposal): fires when discovery has priced a system
	// past MinPricedMarkets yet NO scout post (standing OR frontier sweep-once) stands
	// over it — a coverage gap the captain should close, proposed with a hull count
	// from the circuit math instead of a default of 1. MinPricedMarkets and Freshness
	// must both be > 0 to enable.
	PostProposalMinPricedMarkets int           // propose a post once a system reaches this many priced markets
	PostProposalFreshness        time.Duration // freshness target the proposal's hull-count circuit math sizes for
	PostProposalAvgHop           time.Duration // circuit-model average per-market hop (<= 0 uses the package default)
	PostProposalCooldown         time.Duration // re-propose suppression window (<= 0 uses the package default)

	// PrometheusAlertsURL is the base URL of the Prometheus instance whose OWN
	// /api/v1/alerts endpoint (sp-y0f6) the detector polls directly — no
	// Alertmanager hop needed, since Prometheus evaluates+exposes firing alerts
	// on its own and the watchkeeper tick loop is already the cheap poller the
	// no-monitoring-between-wakes doctrine wants (one more state read alongside
	// the DB-backed detectors above, not a new standing loop). Empty disables
	// the detector entirely (matches the ExpectedStreams idiom).
	PrometheusAlertsURL string
}

// StandingCoordinatorFleet pairs a Ship.DedicatedFleet() tag with the
// container_type of the standing coordinator that pools it (sp-jetm).
type StandingCoordinatorFleet struct {
	Fleet         string // ship.DedicatedFleet() value this exemption covers
	ContainerType string // container_type of the fleet's pool-managing coordinator
}

// Crash-loop defaults wired by the supervisor until CaptainConfig grows tunable
// fields (follow-up bead). Conservative: three unrecoverable deaths of one
// container inside 30 minutes is a genuine loop, not restart noise (sp-no9i).
const (
	defaultCrashLoopWindow    = 30 * time.Minute
	defaultCrashLoopThreshold = 3
)

// defaultStandingCoordinatorFleets is the sp-jetm exemption list, wired by the
// supervisor until CaptainConfig grows a tunable field (follow-up bead, mirrors
// the crash-loop/pinned-hull defaults above). "contract" is the one fleet with a
// pooling standing coordinator today — CONTRACT_FLEET_COORDINATOR, matching
// dedicatedFleetContract in run_fleet_coordinator.go. Tour/trade pins are
// deliberately absent: those hulls run one dedicated container each with no
// pool, so a containerless tour/trade hull stays exactly the anomaly the
// watchdog was built to catch.
var defaultStandingCoordinatorFleets = []StandingCoordinatorFleet{
	{Fleet: "contract", ContainerType: "CONTRACT_FLEET_COORDINATOR"},
}

// defaultFactoryIncomeStall is the sp-7vos per-factory window, wired by the
// supervisor until CaptainConfig grows a tunable field (follow-up bead, mirrors
// the crash-loop/pinned-hull defaults above). Calibrated against observed live
// cadence: productive goods factories go quiet for up to ~2h between
// manufacture-and-sell batches, so 3h leaves margin above the normal gap while
// still catching a factory that is RUNNING yet has genuinely died (zero income
// for three hours is not a slow cycle). Deliberately longer than IncomeStall's
// 2h — the per-factory signal trades detection latency for a low false-positive
// rate, the failure mode this detector must avoid.
const defaultFactoryIncomeStall = 3 * time.Hour

// sp-k7q5 layer 2/3 defaults, wired by the supervisor until CaptainConfig grows
// tunable fields (a follow-up bead, mirroring the crash-loop / factory-stall defaults
// above). The staleness cap matches the tour planner's own maxListingAge (75min), the
// exact boundary past which a lane is dropped; the market-rich thresholds and hop model
// (~3min/market, the Admiral circuit doctrine) are conservative starting points, and the
// cooldowns keep these DEFERRED events from re-queuing every 30s poll while a gap persists.
const (
	defaultStalenessHidingStaleAge         = 75 * time.Minute
	defaultStalenessHidingMinPricedMarkets = 10
	defaultStalenessHidingThreshold        = 5
	defaultStalenessHidingCooldown         = 3 * time.Hour

	defaultPostProposalMinPricedMarkets = 10
	defaultPostProposalFreshness        = 60 * time.Minute
	defaultPostProposalAvgHop           = 3 * time.Minute
	defaultPostProposalCooldown         = 6 * time.Hour
)

// defaultPrometheusAlertsURL is the sp-y0f6 Prometheus base URL, wired by the
// supervisor until CaptainConfig grows a tunable field (follow-up bead,
// mirrors the crash-loop/pinned-hull/factory-stall defaults above). Matches
// the host port gobot/docker-compose.metrics.yml maps Prometheus to
// (9091:9090) — the same instance evaluating fleet-health.yml.
const defaultPrometheusAlertsURL = "http://localhost:9091"

// defaultPrometheusAlertsCooldown is the re-fire suppression window: a
// sustained firing alert reads true on every poll, so without a cooldown the
// SAME firing alert would emit a fresh interrupt event on every tick until it
// clears (mirrors the sibling income/staleness detectors' HasSince idiom).
const defaultPrometheusAlertsCooldown = 15 * time.Minute

// RunDetectors writes synthetic strategic events for conditions that are
// state (not daemon events): stale heartbeats, idle ships, credit crossings.
// Dedup: an event is skipped while an unprocessed twin exists.
func RunDetectors(ctx context.Context, db *gorm.DB, store captain.EventStore, cfg DetectorConfig, now time.Time) error {
	if err := detectStaleHeartbeats(ctx, db, store, cfg, now); err != nil {
		return err
	}
	if err := detectIdleShips(ctx, db, store, cfg, now); err != nil {
		return err
	}
	if err := detectContainerlessPinnedHulls(ctx, db, store, cfg, now); err != nil {
		return err
	}
	if err := detectIncomeStall(ctx, db, store, cfg, now); err != nil {
		return err
	}
	if err := detectEngineIncomeStall(ctx, db, store, cfg, now); err != nil {
		return err
	}
	if err := detectFactoryIncomeStall(ctx, db, store, cfg, now); err != nil {
		return err
	}
	if err := detectStreamDown(ctx, db, store, cfg, now); err != nil {
		return err
	}
	if err := detectCrashLoops(ctx, db, store, cfg, now); err != nil {
		return err
	}
	if err := detectRegimeShift(ctx, db, store, cfg, now); err != nil {
		return err
	}
	if err := detectScoutStaleness(ctx, db, store, cfg, now); err != nil {
		return err
	}
	if err := detectCreditsCrossing(ctx, store, cfg); err != nil {
		return err
	}
	// sp-y0f6: deliberately last in the chain. Every detector above depends on
	// the same Postgres the rest of the app depends on, so its errors are
	// effectively "the app is already broken." This one instead depends on a
	// separate service's HTTP endpoint (Prometheus), which can be transiently
	// unreachable (restart, redeploy, network blip) while the app is perfectly
	// healthy. Keeping it last means that blip can only cost this detector's
	// own poll for the tick — it can never mask another detector's run.
	return detectPrometheusAlerts(ctx, store, cfg, now)
}

func detectStaleHeartbeats(ctx context.Context, db *gorm.DB, store captain.EventStore, cfg DetectorConfig, now time.Time) error {
	cutoff := now.Add(-cfg.StaleHeartbeat)
	var stale []persistence.ContainerModel
	if err := db.WithContext(ctx).
		Where("player_id = ? AND status = ? AND heartbeat_at IS NOT NULL AND heartbeat_at < ?",
			cfg.PlayerID, "RUNNING", cutoff).
		Find(&stale).Error; err != nil {
		return err
	}
	if len(stale) == 0 {
		return nil
	}
	// A slow scout mid-transit legitimately stops heart-beating while the leg
	// runs; its ADVANCING position (nav_status IN_TRANSIT) is proof it is alive.
	// Exempt any stale container whose ship is in transit — a FROZEN position
	// (not in transit) plus a stale heartbeat is the real death signal (sp-no9i).
	// Load the in-transit ship symbols once and match them against each
	// container's config (same quoted-symbol convention as detectIdleShips).
	inTransit, err := inTransitShipSymbols(ctx, db, cfg.PlayerID)
	if err != nil {
		return err
	}
	for _, c := range stale {
		if configReferencesAny(c.Config, inTransit) {
			continue
		}
		// Staleness is a persistent state, not an edge: cooldown on ANY recent
		// heartbeat_lost event (processed or not) prevents a session-burn loop
		// where each acked event is re-emitted — and, being interrupt-class,
		// re-wakes the captain — on the next poll (mirrors detectIdleShips).
		recent, err := store.HasSince(ctx, cfg.PlayerID, captain.EventHeartbeatLost, c.ID, now.Add(-cfg.StaleHeartbeat))
		if err != nil || recent {
			continue
		}
		_ = store.Record(ctx, &captain.Event{
			Type: captain.EventHeartbeatLost, Ship: c.ID, PlayerID: cfg.PlayerID,
			Payload: fmt.Sprintf(`{"container_id":%q,"command_type":%q,"last_heartbeat":%q}`,
				c.ID, c.CommandType, c.HeartbeatAt.UTC().Format(time.RFC3339)),
		})
	}
	return nil
}

// inTransitShipSymbols returns the symbols of the player's ships whose position
// is advancing (nav_status IN_TRANSIT). Used to exempt their worker containers
// from stale-heartbeat detection (sp-no9i).
func inTransitShipSymbols(ctx context.Context, db *gorm.DB, playerID int) ([]string, error) {
	var ships []persistence.ShipModel
	if err := db.WithContext(ctx).
		Select("ship_symbol").
		Where("player_id = ? AND nav_status = ?", playerID, "IN_TRANSIT").
		Find(&ships).Error; err != nil {
		return nil, err
	}
	symbols := make([]string, 0, len(ships))
	for _, s := range ships {
		symbols = append(symbols, s.ShipSymbol)
	}
	return symbols, nil
}

// configReferencesAny reports whether a container's config JSON references any
// of the given ship symbols, matching the quoted symbol the same way
// detectIdleShips joins containers to ships (config stores "...":"SYMBOL").
func configReferencesAny(config string, shipSymbols []string) bool {
	for _, sym := range shipSymbols {
		if sym != "" && strings.Contains(config, `"`+sym+`"`) {
			return true
		}
	}
	return false
}

// episodeTracker deduplicates a per-ship alarm to fire once per CONTINUOUS
// episode of an alarmed state. It replaces the rolling-time-window HasSince
// cooldown for ship.idle and hull.containerless (sp-6g96): that cooldown
// re-armed purely by elapsed time, so a ship deliberately left idle (or a
// hull deliberately left stranded) in the SAME state for hours would re-fire
// — and, being interrupt class for hull.containerless, re-wake the captain —
// every time the cooldown window lapsed, even though nothing had actually
// changed. An episodeTracker instead remembers which ships currently have an
// OPEN episode: enter reports whether this call starts a new one (true the
// first time, false on every subsequent call while the ship stays alarmed);
// clear closes the episode (the ship left the alarmed state), re-arming enter
// to report a fresh episode the next time the ship becomes alarmed again.
//
// Nil-safe by design: a nil *episodeTracker — the zero value of the
// DetectorConfig fields below, e.g. every test literal from before sp-6g96
// that never sets them — makes enter always report true (no dedup at all)
// and clear a no-op, so callers that never wire a tracker keep compiling and
// keep their prior behavior unchanged.
//
// Not safe for concurrent use, but none is needed: the Supervisor that owns
// the long-lived tracker instance runs its tick loop strictly sequentially
// (see Supervisor.Run), so two ticks can never call enter/clear concurrently.
type episodeTracker struct {
	active map[string]bool
}

// enter reports whether ship is starting a NEW episode: true the first time
// it is seen (or the first time again after a clear), false on every
// subsequent call while it remains in the tracker.
func (t *episodeTracker) enter(ship string) bool {
	if t == nil {
		return true
	}
	if t.active == nil {
		t.active = make(map[string]bool)
	}
	if t.active[ship] {
		return false
	}
	t.active[ship] = true
	return true
}

// clear ends ship's episode, if any, so the next enter call reports a fresh
// one. A no-op for a ship with no open episode (or a nil tracker).
func (t *episodeTracker) clear(ship string) {
	if t == nil {
		return
	}
	delete(t.active, ship)
}

func detectIdleShips(ctx context.Context, db *gorm.DB, store captain.EventStore, cfg DetectorConfig, now time.Time) error {
	// A ship is idle if it is not IN_TRANSIT and no RUNNING container's config
	// references it. Container config is JSON text; a LIKE match on the quoted
	// symbol is the pragmatic join (config stores "ship_symbol":"X").
	//
	// sp-6g96: fetches ALL of the player's ships rather than pre-filtering
	// IN_TRANSIT out in SQL, so a ship that leaves idle by starting a new
	// transit — not just by being claimed by a container — still reaches the
	// IdleEpisodes.clear() below. Without this, a transit-only exit would
	// never close the ship's idle episode, wrongly suppressing a legitimately
	// later idle episode after the transit ends.
	var ships []persistence.ShipModel
	if err := db.WithContext(ctx).
		Where("player_id = ?", cfg.PlayerID).
		Find(&ships).Error; err != nil {
		return err
	}
	for _, s := range ships {
		if s.NavStatus == "IN_TRANSIT" {
			cfg.IdleEpisodes.clear(s.ShipSymbol)
			continue
		}
		var busy int64
		if err := db.WithContext(ctx).Model(&persistence.ContainerModel{}).
			Where("player_id = ? AND status = ? AND config LIKE ?",
				cfg.PlayerID, "RUNNING", "%\""+s.ShipSymbol+"\"%").
			Count(&busy).Error; err != nil {
			return err
		}
		if busy > 0 {
			cfg.IdleEpisodes.clear(s.ShipSymbol)
			continue
		}
		// Idle is a persistent state, not an edge: fire once per CONTINUOUS
		// idle episode (sp-6g96 state-change dedup, not a rolling time
		// cooldown) — entering idle emits, staying idle does not, and leaving
		// (transit or claimed, above) re-arms so the NEXT idle episode emits
		// again regardless of how much or little time has passed.
		if !cfg.IdleEpisodes.enter(s.ShipSymbol) {
			continue
		}
		_ = store.Record(ctx, &captain.Event{
			Type: captain.EventShipIdle, Ship: s.ShipSymbol, PlayerID: cfg.PlayerID,
			Payload: fmt.Sprintf(`{"location":%q,"nav_status":%q}`, s.LocationSymbol, s.NavStatus),
		})
	}
	return nil
}

// detectContainerlessPinnedHulls (sp-v63s watchdog) is the belt-and-suspenders
// detector for EVERY silent-death class. A hull with a standing fleet dedication
// (dedicated_fleet != ”) is meant to ALWAYS have a running coordinator container:
// the continuous trade/tour engines run one container per hull across manifests, so
// a dedicated hull with NO running container is an anomaly, never normal churn. The
// root silent-death cause is fixed at the source (a live container carrying a stale
// FAILED row, dropped from recovery), but any FUTURE defect that strands a pinned
// hull — a dropped claim, an unlogged crash, a recovery miss — surfaces HERE as one
// interrupt-class event naming the hull, cargo, and how long it has been stranded.
//
// Edge-triggered like detectIdleShips/detectStaleHeartbeats: the age gate
// (containerless for >= threshold, anchored on the assignment's released_at)
// tolerates the brief containerless window of a normal redeploy+recovery, and the
// HasSince cooldown suppresses per-poll re-fire while the state persists (no o8wi
// spam). A hull WITH a running container, an UNDEDICATED hull, and a dedicated hull
// only briefly containerless all stay silent.
//
// A hull pinned to a StandingCoordinatorFleets entry (sp-jetm) is a further
// exemption, orthogonal to the age gate: a contract-fleet hull pooled-idle
// between claims is by design, for as long as the pool's coordinator stays up
// — not just briefly. It stays silent WHILE that fleet's coordinator container
// is RUNNING; the moment the coordinator itself dies, the hull loses its
// exemption and the watchdog fires exactly as it would for any other pin (the
// coordinator-died case SHOULD alarm — that loss mode is real).
func detectContainerlessPinnedHulls(ctx context.Context, db *gorm.DB, store captain.EventStore, cfg DetectorConfig, now time.Time) error {
	if cfg.PinnedHullContainerless <= 0 {
		return nil // disabled
	}
	cutoff := now.Add(-cfg.PinnedHullContainerless)

	// Precompute once per sweep (not per-ship) which configured fleets currently
	// have a RUNNING pool coordinator — one query per configured fleet, mirroring
	// inTransitShipSymbols's "compute the exemption set up front" shape.
	pooledFleets, err := runningStandingCoordinatorFleets(ctx, db, cfg.PlayerID, cfg.StandingCoordinatorFleets)
	if err != nil {
		return err
	}

	var ships []persistence.ShipModel
	if err := db.WithContext(ctx).
		Where("player_id = ? AND dedicated_fleet <> ''", cfg.PlayerID).
		Find(&ships).Error; err != nil {
		return err
	}
	for _, s := range ships {
		// A RUNNING container referencing this hull → healthy, skip. Same quoted-
		// symbol LIKE join detectIdleShips uses (config stores "ship_symbol":"X").
		var busy int64
		if err := db.WithContext(ctx).Model(&persistence.ContainerModel{}).
			Where("player_id = ? AND status = ? AND config LIKE ?",
				cfg.PlayerID, "RUNNING", "%\""+s.ShipSymbol+"\"%").
			Count(&busy).Error; err != nil {
			return err
		}
		if busy > 0 {
			cfg.ContainerlessEpisodes.clear(s.ShipSymbol)
			continue
		}
		// Pool-managed fleet with its coordinator up → containerless is by design,
		// regardless of how long. Skip before the age gate so a pool hull idle for
		// hours between claims never fires (see doc comment above).
		if pooledFleets[s.DedicatedFleet] {
			cfg.ContainerlessEpisodes.clear(s.ShipSymbol)
			continue
		}
		// Age gate: only a hull containerless for >= threshold is anomalous. Anchor
		// on released_at (when its last assignment ended). A dedicated hull that has
		// never held an assignment (released_at NULL) is a launch/config concern, not
		// a silent death — leave it to detectIdleShips rather than false-alarm here.
		if s.ReleasedAt == nil || s.ReleasedAt.After(cutoff) {
			cfg.ContainerlessEpisodes.clear(s.ShipSymbol)
			continue
		}
		// Containerless is a persistent state, not an edge: fire once per
		// CONTINUOUS containerless episode (sp-6g96 state-change dedup, not a
		// rolling time cooldown) — mirrors detectIdleShips. Entering emits;
		// staying containerless does not; leaving (busy or pool-exempt, above)
		// re-arms so the NEXT stranding emits again regardless of elapsed time.
		if !cfg.ContainerlessEpisodes.enter(s.ShipSymbol) {
			continue
		}
		containerlessMinutes := int(now.Sub(*s.ReleasedAt).Minutes())
		_ = store.Record(ctx, &captain.Event{
			Type: captain.EventPinnedHullContainerless, Ship: s.ShipSymbol, PlayerID: cfg.PlayerID,
			Payload: fmt.Sprintf(`{"ship_symbol":%q,"dedicated_fleet":%q,"location":%q,"cargo_units":%d,"cargo_capacity":%d,"containerless_minutes":%d}`,
				s.ShipSymbol, s.DedicatedFleet, s.LocationSymbol, s.CargoUnits, s.CargoCapacity, containerlessMinutes),
		})
	}
	return nil
}

// runningStandingCoordinatorFleets returns the set of DedicatedFleet tags
// (sp-jetm) whose configured pool coordinator currently has a RUNNING
// container. Empty fleets means the exemption is off entirely — the query
// loop below simply does not run — so a DetectorConfig that never sets
// StandingCoordinatorFleets reproduces the original sp-v63s behavior exactly.
func runningStandingCoordinatorFleets(ctx context.Context, db *gorm.DB, playerID int, fleets []StandingCoordinatorFleet) (map[string]bool, error) {
	if len(fleets) == 0 {
		return nil, nil
	}
	pooled := make(map[string]bool, len(fleets))
	for _, f := range fleets {
		var running int64
		if err := db.WithContext(ctx).Model(&persistence.ContainerModel{}).
			Where("player_id = ? AND status = ? AND container_type = ?", playerID, "RUNNING", f.ContainerType).
			Count(&running).Error; err != nil {
			return nil, err
		}
		if running > 0 {
			pooled[f.Fleet] = true
		}
	}
	return pooled, nil
}

func detectStreamDown(ctx context.Context, db *gorm.DB, store captain.EventStore, cfg DetectorConfig, now time.Time) error {
	if cfg.StreamDown <= 0 || len(cfg.ExpectedStreams) == 0 {
		return nil
	}
	var runningTotal int64
	if err := db.WithContext(ctx).Model(&persistence.ContainerModel{}).
		Where("player_id = ? AND status = ?", cfg.PlayerID, "RUNNING").
		Count(&runningTotal).Error; err != nil {
		return err
	}
	if runningTotal == 0 {
		return nil
	}
	cutoff := now.Add(-cfg.StreamDown)
	for _, stream := range cfg.ExpectedStreams {
		like := stream + "%"
		var running int64
		if err := db.WithContext(ctx).Model(&persistence.ContainerModel{}).
			Where("player_id = ? AND status = ? AND container_type LIKE ?", cfg.PlayerID, "RUNNING", like).
			Count(&running).Error; err != nil {
			return err
		}
		if running > 0 {
			continue
		}
		var lastStopped persistence.ContainerModel
		if err := db.WithContext(ctx).
			Where("player_id = ? AND container_type LIKE ? AND stopped_at IS NOT NULL", cfg.PlayerID, like).
			Order("stopped_at DESC").
			Limit(1).
			Find(&lastStopped).Error; err != nil {
			return err
		}
		if lastStopped.ID == "" || lastStopped.StoppedAt == nil || lastStopped.StoppedAt.After(cutoff) {
			continue
		}
		recent, err := store.HasSince(ctx, cfg.PlayerID, captain.EventStreamDown, stream, now.Add(-cfg.StreamDown))
		if err != nil {
			return err
		}
		if recent {
			continue
		}
		_ = store.Record(ctx, &captain.Event{
			Type: captain.EventStreamDown, Ship: stream, PlayerID: cfg.PlayerID,
			Payload: fmt.Sprintf(`{"stream":%q,"down_minutes":%d,"stopped_at":%q}`,
				stream, int(cfg.StreamDown.Minutes()), lastStopped.StoppedAt.UTC().Format(time.RFC3339)),
		})
	}
	return nil
}

// detectCrashLoops turns a burst of true container deaths into a single
// interrupt. sp-okwk made container.crashed count true (unrecoverable) deaths;
// a lone death is self-healing (auto-restart+resume) and stays deferred. When
// the SAME container dies CrashLoopThreshold times within CrashLoopWindow it is
// a genuine loop, so emit one interrupt-class container.crashloop for it — one
// per loop, not per death (sp-no9i).
func detectCrashLoops(ctx context.Context, db *gorm.DB, store captain.EventStore, cfg DetectorConfig, now time.Time) error {
	if cfg.CrashLoopWindow <= 0 || cfg.CrashLoopThreshold <= 0 {
		return nil
	}
	windowStart := now.Add(-cfg.CrashLoopWindow)
	var crashes []persistence.CaptainEventModel
	if err := db.WithContext(ctx).
		Where("player_id = ? AND type = ? AND created_at > ?",
			cfg.PlayerID, string(captain.EventContainerCrashed), windowStart).
		Find(&crashes).Error; err != nil {
		return err
	}
	deaths := make(map[string]int)
	for i := range crashes {
		if id := crashContainerID(crashes[i].Payload); id != "" {
			deaths[id]++
		}
	}
	for id, n := range deaths {
		if n < cfg.CrashLoopThreshold {
			continue
		}
		// One interrupt per loop, not per death: cooldown on any recent
		// crashloop for this container (mirrors the other detectors' idiom).
		recent, err := store.HasSince(ctx, cfg.PlayerID, captain.EventContainerCrashLoop, id, windowStart)
		if err != nil || recent {
			continue
		}
		_ = store.Record(ctx, &captain.Event{
			Type: captain.EventContainerCrashLoop, Ship: id, PlayerID: cfg.PlayerID,
			Payload: fmt.Sprintf(`{"container_id":%q,"deaths":%d,"window_minutes":%d}`,
				id, n, int(cfg.CrashLoopWindow.Minutes())),
		})
	}
	return nil
}

// crashContainerID extracts the container_id recorded in a container.crashed
// event payload (see recordCrash); returns "" when absent or unparseable.
func crashContainerID(payload string) string {
	var p struct {
		ContainerID string `json:"container_id"`
	}
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return ""
	}
	return p.ContainerID
}
