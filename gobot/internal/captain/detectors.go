package captainsup

import (
	"context"
	"fmt"
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
}

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
	for _, c := range stale {
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
