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
	PlayerID          int
	ShipIdle          time.Duration
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
}

// Crash-loop defaults wired by the supervisor until CaptainConfig grows tunable
// fields (follow-up bead). Conservative: three unrecoverable deaths of one
// container inside 30 minutes is a genuine loop, not restart noise (sp-no9i).
const (
	defaultCrashLoopWindow    = 30 * time.Minute
	defaultCrashLoopThreshold = 3
)

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
	if err := detectIncomeStall(ctx, db, store, cfg, now); err != nil {
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
	return detectCreditsCrossing(ctx, store, cfg)
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

func detectIdleShips(ctx context.Context, db *gorm.DB, store captain.EventStore, cfg DetectorConfig, now time.Time) error {
	// A ship is idle if it is not IN_TRANSIT and no RUNNING container's config
	// references it. Container config is JSON text; a LIKE match on the quoted
	// symbol is the pragmatic join (config stores "ship_symbol":"X").
	var ships []persistence.ShipModel
	if err := db.WithContext(ctx).
		Where("player_id = ? AND nav_status != ?", cfg.PlayerID, "IN_TRANSIT").
		Find(&ships).Error; err != nil {
		return err
	}
	for _, s := range ships {
		var busy int64
		if err := db.WithContext(ctx).Model(&persistence.ContainerModel{}).
			Where("player_id = ? AND status = ? AND config LIKE ?",
				cfg.PlayerID, "RUNNING", "%\""+s.ShipSymbol+"\"%").
			Count(&busy).Error; err != nil {
			return err
		}
		if busy > 0 {
			continue
		}
		// Idle is a persistent state, not an edge: cooldown on ANY recent
		// idle event (processed or not) prevents a session-burn loop where
		// each processed event is re-emitted on the next poll.
		recent, err := store.HasSince(ctx, cfg.PlayerID, captain.EventShipIdle, s.ShipSymbol, now.Add(-cfg.ShipIdle))
		if err != nil || recent {
			continue
		}
		_ = store.Record(ctx, &captain.Event{
			Type: captain.EventShipIdle, Ship: s.ShipSymbol, PlayerID: cfg.PlayerID,
			Payload: fmt.Sprintf(`{"location":%q,"nav_status":%q}`, s.LocationSymbol, s.NavStatus),
		})
	}
	return nil
}

const incomeStallStreamKey = "income"

func detectIncomeStall(ctx context.Context, db *gorm.DB, store captain.EventStore, cfg DetectorConfig, now time.Time) error {
	if cfg.IncomeStall <= 0 {
		return nil
	}
	cutoff := now.Add(-cfg.IncomeStall)
	var runningCoordinators int64
	if err := db.WithContext(ctx).Model(&persistence.ContainerModel{}).
		Where("player_id = ? AND status = ? AND container_type LIKE ? AND started_at IS NOT NULL AND started_at <= ?",
			cfg.PlayerID, "RUNNING", "%coordinator%", cutoff).
		Count(&runningCoordinators).Error; err != nil {
		return err
	}
	if runningCoordinators == 0 {
		return nil
	}
	var incoming int64
	if err := db.WithContext(ctx).Model(&persistence.TransactionModel{}).
		Where("player_id = ? AND amount > 0 AND timestamp >= ?", cfg.PlayerID, cutoff).
		Count(&incoming).Error; err != nil {
		return err
	}
	if incoming > 0 {
		return nil
	}
	recent, err := store.HasSince(ctx, cfg.PlayerID, captain.EventIncomeStalled, incomeStallStreamKey, now.Add(-cfg.IncomeStall))
	if err != nil || recent {
		return err
	}
	return store.Record(ctx, &captain.Event{
		Type: captain.EventIncomeStalled, Ship: incomeStallStreamKey, PlayerID: cfg.PlayerID,
		Payload: fmt.Sprintf(`{"stall_hours":%.1f,"running_coordinators":%d}`,
			cfg.IncomeStall.Hours(), runningCoordinators),
	})
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

// gasSymbols is the fixed set of SpaceTraders goods the captain's "GAS"
// price class covers (extracted via gas-siphon operations, not mining).
// There is no exported domain classification to reuse — internal/domain/goods
// keeps no ore/gas grouping — so sp-zlfv defines its own minimal, local
// classifier rather than reach into an unrelated package for three strings.
var gasSymbols = map[string]bool{
	"HYDROCARBON":     true,
	"LIQUID_HYDROGEN": true,
	"LIQUID_NITROGEN": true,
}

// matchesGoodClass reports whether goodSymbol belongs to a tripwire's
// configured good scope: the class keyword "ORE" (any *_ORE symbol), the
// class keyword "GAS" (gasSymbols), or a literal comma-separated symbol
// allowlist (exact match, case-insensitive) for anything else.
func matchesGoodClass(goodSymbol, class string) bool {
	switch strings.ToUpper(strings.TrimSpace(class)) {
	case "ORE":
		return strings.HasSuffix(goodSymbol, "_ORE")
	case "GAS":
		return gasSymbols[goodSymbol]
	default:
		for _, sym := range strings.Split(class, ",") {
			if strings.EqualFold(strings.TrimSpace(sym), goodSymbol) {
				return true
			}
		}
		return false
	}
}

// resolveRegimeThreshold returns the effective price threshold to compare
// against, plus the baseline it was derived from. Absolute mode (Threshold
// set) needs no lookup: baseline reports as 0. Multiplier mode looks up the
// OLDEST recorded price-history sample within tw.Window as the baseline and
// scales it; ok=false when a multiplier tripwire has no baseline recorded
// yet within the window (nothing to compare the current price against).
func resolveRegimeThreshold(ctx context.Context, db *gorm.DB, playerID int, waypoint, good string, tw RegimeTripwire, now time.Time) (threshold int, baseline int, ok bool, err error) {
	if tw.Threshold != nil {
		return *tw.Threshold, 0, true, nil
	}
	if tw.Multiplier == nil {
		return 0, 0, false, nil
	}
	var oldest persistence.MarketPriceHistoryModel
	err = db.WithContext(ctx).
		Where("player_id = ? AND waypoint_symbol = ? AND good_symbol = ? AND recorded_at >= ?",
			playerID, waypoint, good, now.Add(-tw.Window)).
		Order("recorded_at ASC").
		Limit(1).
		Find(&oldest).Error
	if err != nil {
		return 0, 0, false, err
	}
	if oldest.WaypointSymbol == "" {
		return 0, 0, false, nil
	}
	baseline = oldest.SellPrice
	return int(*tw.Multiplier * float64(baseline)), baseline, true, nil
}

// regimeDedupKey scopes the edge-trigger cooldown to (good, market,
// direction): the natural identity of a single crossing, not the tripwire
// config that detected it. Two tripwires that happen to overlap on the same
// good+market+direction are a degenerate config the captain would not
// realistically declare (there is no reason to set two tripwires for the
// same good, same direction, different thresholds instead of just one).
func regimeDedupKey(good, waypoint, direction string) string {
	return good + "@" + waypoint + ":" + direction
}

// detectRegimeShift scans MarketData for prices crossing a captain-declared
// tripwire (sp-zlfv): mechanizes the per-wake price sweep the captain used to
// hand-roll ("any ore bid >=200 or gas bid >=150 (~3x baseline) triggers an
// immediate extraction re-consult"). Edge-triggered with cooldown via
// HasSince (sp-1hak lesson): one event per crossing, not per poll — once
// acknowledged, the same crossing does not re-fire until Window elapses AND
// the price re-crosses. No tripwires configured means no query at all (zero
// overhead when unset).
func detectRegimeShift(ctx context.Context, db *gorm.DB, store captain.EventStore, cfg DetectorConfig, now time.Time) error {
	if len(cfg.RegimeTripwires) == 0 {
		return nil
	}
	var markets []persistence.MarketData
	if err := db.WithContext(ctx).Where("player_id = ?", cfg.PlayerID).Find(&markets).Error; err != nil {
		return err
	}
	for _, tw := range cfg.RegimeTripwires {
		for _, m := range markets {
			if !matchesGoodClass(m.GoodSymbol, tw.Good) {
				continue
			}
			threshold, baseline, ok, err := resolveRegimeThreshold(ctx, db, cfg.PlayerID, m.WaypointSymbol, m.GoodSymbol, tw, now)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			price := m.SellPrice
			var crossed bool
			switch tw.Direction {
			case "bid-above":
				crossed = price >= threshold
			case "bid-below":
				crossed = price <= threshold
			}
			if !crossed {
				continue
			}
			key := regimeDedupKey(m.GoodSymbol, m.WaypointSymbol, tw.Direction)
			recent, err := store.HasSince(ctx, cfg.PlayerID, captain.EventMarketRegimeShift, key, now.Add(-tw.Window))
			if err != nil {
				return err
			}
			if recent {
				continue
			}
			_ = store.Record(ctx, &captain.Event{
				Type: captain.EventMarketRegimeShift, Ship: key, PlayerID: cfg.PlayerID,
				Payload: fmt.Sprintf(`{"good":%q,"market":%q,"price":%d,"baseline":%d,"threshold":%d}`,
					m.GoodSymbol, m.WaypointSymbol, price, baseline, threshold),
			})
		}
	}
	return nil
}

func detectCreditsCrossing(ctx context.Context, store captain.EventStore, cfg DetectorConfig) error {
	if cfg.LastCredits == 0 || len(cfg.CreditsThresholds) == 0 {
		return nil
	}
	// Use the supervisor-supplied current credits (sp-sk68 D4): the detector no
	// longer re-derives its own value via CurrentCredits, so it evaluates the
	// SAME number as the wake gate and cannot fail independently on a DB error.
	current := cfg.CurrentCreditsValue
	for _, th := range cfg.CreditsThresholds {
		crossedUp := cfg.LastCredits < th && current >= th
		crossedDown := cfg.LastCredits >= th && current < th
		if !crossedUp && !crossedDown {
			continue
		}
		direction := "up"
		if crossedDown {
			direction = "down"
		}
		key := fmt.Sprintf("%d", th)
		dup, err := store.HasUnprocessed(ctx, cfg.PlayerID, captain.EventCreditsThreshold, key)
		if err != nil || dup {
			continue
		}
		_ = store.Record(ctx, &captain.Event{
			Type: captain.EventCreditsThreshold, Ship: key, PlayerID: cfg.PlayerID,
			Payload: fmt.Sprintf(`{"threshold":%d,"direction":%q,"credits":%d}`, th, direction, current),
		})
	}
	return nil
}

func CurrentCredits(ctx context.Context, db *gorm.DB, playerID int) (int, error) {
	anchor, anchored, err := latestContractAnchor(ctx, db, playerID)
	if err != nil {
		return 0, err
	}
	if !anchored {
		return creditsFromLatestBalance(ctx, db, playerID)
	}
	return creditsAnchoredToContract(ctx, db, playerID, anchor)
}

func latestContractAnchor(ctx context.Context, db *gorm.DB, playerID int) (persistence.TransactionModel, bool, error) {
	var tx persistence.TransactionModel
	err := db.WithContext(ctx).
		Where("player_id = ? AND transaction_type LIKE ?", playerID, "CONTRACT_%").
		Order("timestamp DESC, created_at DESC, id DESC").
		Limit(1).
		Find(&tx).Error
	if err != nil {
		return persistence.TransactionModel{}, false, err
	}
	return tx, tx.ID != "", nil
}

func creditsAnchoredToContract(ctx context.Context, db *gorm.DB, playerID int, anchor persistence.TransactionModel) (int, error) {
	var delta struct{ Sum int }
	err := db.WithContext(ctx).Model(&persistence.TransactionModel{}).
		Select("COALESCE(SUM(amount), 0) AS sum").
		Where("player_id = ? AND timestamp > ?", playerID, anchor.Timestamp).
		Scan(&delta).Error
	if err != nil {
		return 0, err
	}
	return anchor.BalanceAfter + delta.Sum, nil
}

func creditsFromLatestBalance(ctx context.Context, db *gorm.DB, playerID int) (int, error) {
	var tx persistence.TransactionModel
	err := db.WithContext(ctx).
		Where("player_id = ?", playerID).
		Order("timestamp DESC, created_at DESC, id DESC").
		Limit(1).
		Find(&tx).Error
	if err != nil {
		return 0, err
	}
	return tx.BalanceAfter, nil
}
