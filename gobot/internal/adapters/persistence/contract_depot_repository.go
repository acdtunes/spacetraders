package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/contract/depotstore"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GormContractDepotRepository is the durable driven adapter behind
// depotstore.Repository. Every row is scoped to the player baked in at construction —
// the depotstore.Store port carries no player id, so the adapter is a per-player view
// over the shared contract_depots table, mirroring the gas/storage operation
// repositories. All depot state lives here, which is exactly what makes the store
// restart-safe: a fresh process rebuilds the identical routing registry from these
// rows.
type GormContractDepotRepository struct {
	db       *gorm.DB
	playerID int
}

// NewGormContractDepotRepository builds the durable depot repository for one player.
func NewGormContractDepotRepository(db *gorm.DB, playerID int) *GormContractDepotRepository {
	return &GormContractDepotRepository{db: db, playerID: playerID}
}

// List returns every persisted depot for this player, ordered by id so the registry
// rebuild is deterministic regardless of insertion order.
func (r *GormContractDepotRepository) List(ctx context.Context) ([]*depot.ContractDepot, error) {
	var models []ContractDepotModel
	if err := r.db.WithContext(ctx).
		Where("player_id = ?", r.playerID).
		Order("id ASC").
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list contract depots: %w", err)
	}

	out := make([]*depot.ContractDepot, 0, len(models))
	for i := range models {
		c, err := r.toEntity(&models[i])
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// Get loads one depot by id for this player. A missing row is (nil, false, nil) —
// the not-found contract the store's granular mutations key on, never an error.
func (r *GormContractDepotRepository) Get(ctx context.Context, id string) (*depot.ContractDepot, bool, error) {
	var model ContractDepotModel
	err := r.db.WithContext(ctx).
		Where("id = ? AND player_id = ?", id, r.playerID).
		First(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get contract depot %q: %w", id, err)
	}

	c, err := r.toEntity(&model)
	if err != nil {
		return nil, false, err
	}
	return c, true, nil
}

// Save upserts a depot for this player keyed on (id, player_id): a new depot is
// inserted, an existing one has its four element classes replaced in place. created_at
// is preserved across updates (only the mutable element columns + updated_at change),
// so the store's declarative apply and granular mutations both persist through one call.
func (r *GormContractDepotRepository) Save(ctx context.Context, c *depot.ContractDepot) error {
	model, err := r.toModel(c)
	if err != nil {
		return err
	}
	if err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}, {Name: "player_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"warehouses", "stockers", "delivery_hulls", "source_hubs", "updated_at"}),
		}).
		Create(model).Error; err != nil {
		return fmt.Errorf("save contract depot %q: %w", c.ID(), err)
	}
	return nil
}

// Delete removes a depot for this player. A missing row is not an error, so the
// store's declarative ApplyTopology can prune stale depots idempotently.
func (r *GormContractDepotRepository) Delete(ctx context.Context, id string) error {
	if err := r.db.WithContext(ctx).
		Where("id = ? AND player_id = ?", id, r.playerID).
		Delete(&ContractDepotModel{}).Error; err != nil {
		return fmt.Errorf("delete contract depot %q: %w", id, err)
	}
	return nil
}

// toModel serializes a domain depot into its player-scoped row, JSON-encoding each
// element class.
func (r *GormContractDepotRepository) toModel(c *depot.ContractDepot) (*ContractDepotModel, error) {
	warehouses, err := marshalDepotElements(c.Warehouses())
	if err != nil {
		return nil, fmt.Errorf("depot %q warehouses: %w", c.ID(), err)
	}
	stockers, err := marshalDepotElements(c.Stockers())
	if err != nil {
		return nil, fmt.Errorf("depot %q stockers: %w", c.ID(), err)
	}
	deliveryHulls, err := marshalDepotElements(c.DeliveryHulls())
	if err != nil {
		return nil, fmt.Errorf("depot %q delivery hulls: %w", c.ID(), err)
	}
	sourceHubs, err := marshalDepotElements(c.SourceHubs())
	if err != nil {
		return nil, fmt.Errorf("depot %q source hubs: %w", c.ID(), err)
	}
	return &ContractDepotModel{
		ID:            c.ID(),
		PlayerID:      r.playerID,
		Warehouses:    warehouses,
		Stockers:      stockers,
		DeliveryHulls: deliveryHulls,
		SourceHubs:    sourceHubs,
	}, nil
}

// toEntity rebuilds a domain depot from its row, decoding each element class and
// running the result back through the domain constructor so its invariants hold.
func (r *GormContractDepotRepository) toEntity(m *ContractDepotModel) (*depot.ContractDepot, error) {
	warehouses, err := unmarshalDepotElements(m.Warehouses)
	if err != nil {
		return nil, fmt.Errorf("depot %q warehouses: %w", m.ID, err)
	}
	stockers, err := unmarshalDepotElements(m.Stockers)
	if err != nil {
		return nil, fmt.Errorf("depot %q stockers: %w", m.ID, err)
	}
	deliveryHulls, err := unmarshalDepotElements(m.DeliveryHulls)
	if err != nil {
		return nil, fmt.Errorf("depot %q delivery hulls: %w", m.ID, err)
	}
	sourceHubs, err := unmarshalDepotElements(m.SourceHubs)
	if err != nil {
		return nil, fmt.Errorf("depot %q source hubs: %w", m.ID, err)
	}
	return depot.NewContractDepot(m.ID, warehouses, stockers, deliveryHulls, sourceHubs)
}

// marshalDepotElements JSON-encodes one element class, storing an empty class as the
// empty array "[]" rather than "null" so the column shape is stable.
func marshalDepotElements(elems []depot.Element) (string, error) {
	if len(elems) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(elems)
	if err != nil {
		return "", fmt.Errorf("marshal depot elements: %w", err)
	}
	return string(b), nil
}

// unmarshalDepotElements decodes one element class; a NULL/empty/"null" column reads
// as an absent class (nil slice).
func unmarshalDepotElements(raw string) ([]depot.Element, error) {
	if raw == "" || raw == "null" {
		return nil, nil
	}
	var elems []depot.Element
	if err := json.Unmarshal([]byte(raw), &elems); err != nil {
		return nil, fmt.Errorf("unmarshal depot elements: %w", err)
	}
	if len(elems) == 0 {
		return nil, nil
	}
	return elems, nil
}

// Verify the adapter satisfies the application-layer driven port.
var _ depotstore.Repository = (*GormContractDepotRepository)(nil)
