package persistence

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

type GormCaptainEventRepository struct {
	db *gorm.DB
}

var _ captain.EventStore = (*GormCaptainEventRepository)(nil)

func NewGormCaptainEventRepository(db *gorm.DB) *GormCaptainEventRepository {
	return &GormCaptainEventRepository{db: db}
}

func (r *GormCaptainEventRepository) Record(ctx context.Context, e *captain.Event) error {
	payload := e.Payload
	if payload == "" {
		payload = "{}"
	}
	model := CaptainEventModel{
		PlayerID: e.PlayerID,
		Type:     string(e.Type),
		Ship:     e.Ship,
		Payload:  payload,
	}
	if err := r.db.WithContext(ctx).Create(&model).Error; err != nil {
		return err
	}
	e.ID = model.ID
	e.CreatedAt = model.CreatedAt
	return nil
}

func (r *GormCaptainEventRepository) FindUnprocessed(ctx context.Context, playerID int, limit int) ([]*captain.Event, error) {
	var models []CaptainEventModel
	q := r.db.WithContext(ctx).
		Where("player_id = ? AND processed_at IS NULL", playerID).
		Order("created_at ASC, id ASC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Find(&models).Error; err != nil {
		return nil, err
	}
	events := make([]*captain.Event, 0, len(models))
	for i := range models {
		events = append(events, modelToCaptainEvent(&models[i]))
	}
	return events, nil
}

func (r *GormCaptainEventRepository) MarkProcessed(ctx context.Context, ids []int64, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&CaptainEventModel{}).
		Where("id IN ?", ids).
		Update("processed_at", at).Error
}

func (r *GormCaptainEventRepository) HasUnprocessed(ctx context.Context, playerID int, t captain.EventType, ship string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&CaptainEventModel{}).
		Where("player_id = ? AND type = ? AND ship = ? AND processed_at IS NULL", playerID, string(t), ship).
		Count(&count).Error
	return count > 0, err
}

func modelToCaptainEvent(m *CaptainEventModel) *captain.Event {
	return &captain.Event{
		ID:          m.ID,
		Type:        captain.EventType(m.Type),
		Ship:        m.Ship,
		PlayerID:    m.PlayerID,
		Payload:     m.Payload,
		CreatedAt:   m.CreatedAt,
		ProcessedAt: m.ProcessedAt,
	}
}
