// detectors_credits.go — credit-threshold crossing detector and the CurrentCredits
// balance reconstruction it (and the wake gate) read. Split out of detectors.go
// for navigability; behavior unchanged.
package watchkeeper

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

func detectCreditsCrossing(ctx context.Context, store captain.EventStore, cfg DetectorConfig) error {
	if cfg.LastCredits == 0 || len(cfg.CreditsThresholds) == 0 {
		return nil
	}
	// Use the supervisor-supplied current credits (sp-sk68 D4): this evaluates
	// the SAME number as the wake gate and cannot fail independently on a DB
	// error.
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

// creditsFromLatestBalance reports the newest recorded balance for the player.
//
// EMPTY IS NOT ZERO, AND First IS WHAT DISTINGUISHES THEM (sp-2ms9x). This used Find into a SINGLE
// STRUCT, which does not return ErrRecordNotFound: an empty result leaves the struct zero-valued and
// returns a nil error, so an unreadable balance became the VALUE zero. A fresh era legitimately has
// an empty ledger and 0 credits is itself a legitimate balance, so the two are indistinguishable in
// exactly the case where getting it wrong matters.
//
// It reports the empty ledger as an ERROR rather than a number, which is the fail-safe direction for
// every caller it has: CurrentCredits propagates it, and the watchkeeper's sampler already handles a
// read failure by RETAINING ITS LAST KNOWN VALUE. Reporting 0 instead made it record a bankrupt
// agent, which is what the credits-threshold detector then fires on.
//
// Find into a SLICE stays correct and is deliberately not touched — len() makes the empty case
// observable to the caller. latestContractAnchor above keeps its Find for the same reason: it tests
// tx.ID != "" and so observes emptiness explicitly.
func creditsFromLatestBalance(ctx context.Context, db *gorm.DB, playerID int) (int, error) {
	var tx persistence.TransactionModel
	err := db.WithContext(ctx).
		Where("player_id = ?", playerID).
		Order("timestamp DESC, created_at DESC, id DESC").
		First(&tx).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, fmt.Errorf("no recorded balance for player %d: the ledger is empty, which is not the same as a zero balance", playerID)
	}
	if err != nil {
		return 0, err
	}
	return tx.BalanceAfter, nil
}
