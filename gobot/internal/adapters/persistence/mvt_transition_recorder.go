package persistence

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
)

// MVTTransitionRecorderGORM appends hull-loop transitions to mvt_transitions.
type MVTTransitionRecorderGORM struct {
	db *gorm.DB
}

var _ mvt.TransitionRecorder = (*MVTTransitionRecorderGORM)(nil)

func NewMVTTransitionRecorder(db *gorm.DB) *MVTTransitionRecorderGORM {
	return &MVTTransitionRecorderGORM{db: db}
}

func (r *MVTTransitionRecorderGORM) Record(ctx context.Context, t mvt.Transition) error {
	row := MVTTransitionModel{PlayerID: t.PlayerID, Hull: t.Hull, FromState: string(t.From), ToState: string(t.To),
		System: t.System, YieldHere: t.YieldHere, BestAlternative: t.BestAlternative, TravelCost: t.TravelCost,
		Reason: t.Reason, At: t.At}
	if err := r.db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("record mvt transition %s %s→%s: %w", t.Hull, t.From, t.To, err)
	}
	return nil
}

// ListSince returns a player's transitions at or after since, oldest first.
func (r *MVTTransitionRecorderGORM) ListSince(ctx context.Context, playerID int, since time.Time) ([]mvt.Transition, error) {
	var rows []MVTTransitionModel
	if err := r.db.WithContext(ctx).Where("player_id = ? AND at >= ?", playerID, since).Order("at ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list mvt transitions: %w", err)
	}
	out := make([]mvt.Transition, 0, len(rows))
	for _, row := range rows {
		out = append(out, mvt.Transition{PlayerID: row.PlayerID, Hull: row.Hull, From: mvt.State(row.FromState), To: mvt.State(row.ToState),
			System: row.System, YieldHere: row.YieldHere, BestAlternative: row.BestAlternative, TravelCost: row.TravelCost,
			Reason: row.Reason, At: row.At})
	}
	return out, nil
}
