package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/contract/clusterstore"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/cluster"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GormContractClusterRepository is the durable driven adapter behind
// clusterstore.Repository (bead sp-u9xa). Every row is scoped to the player baked in
// at construction — the clusterstore.Store port carries no player id, so the adapter
// is a per-player view over the shared contract_clusters table, mirroring the
// gas/storage operation repositories. All cluster state lives here, which is exactly
// what makes the store restart-safe: a fresh process rebuilds the identical routing
// registry from these rows.
type GormContractClusterRepository struct {
	db       *gorm.DB
	playerID int
}

// NewGormContractClusterRepository builds the durable cluster repository for one player.
func NewGormContractClusterRepository(db *gorm.DB, playerID int) *GormContractClusterRepository {
	return &GormContractClusterRepository{db: db, playerID: playerID}
}

// List returns every persisted cluster for this player, ordered by id so the registry
// rebuild is deterministic regardless of insertion order.
func (r *GormContractClusterRepository) List(ctx context.Context) ([]*cluster.ContractCluster, error) {
	var models []ContractClusterModel
	if err := r.db.WithContext(ctx).
		Where("player_id = ?", r.playerID).
		Order("id ASC").
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("list contract clusters: %w", err)
	}

	out := make([]*cluster.ContractCluster, 0, len(models))
	for i := range models {
		c, err := r.toEntity(&models[i])
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// Get loads one cluster by id for this player. A missing row is (nil, false, nil) —
// the not-found contract the store's granular mutations key on, never an error.
func (r *GormContractClusterRepository) Get(ctx context.Context, id string) (*cluster.ContractCluster, bool, error) {
	var model ContractClusterModel
	err := r.db.WithContext(ctx).
		Where("id = ? AND player_id = ?", id, r.playerID).
		First(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get contract cluster %q: %w", id, err)
	}

	c, err := r.toEntity(&model)
	if err != nil {
		return nil, false, err
	}
	return c, true, nil
}

// Save upserts a cluster for this player keyed on (id, player_id): a new cluster is
// inserted, an existing one has its four element classes replaced in place. created_at
// is preserved across updates (only the mutable element columns + updated_at change),
// so the store's declarative apply and granular mutations both persist through one call.
func (r *GormContractClusterRepository) Save(ctx context.Context, c *cluster.ContractCluster) error {
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
		return fmt.Errorf("save contract cluster %q: %w", c.ID(), err)
	}
	return nil
}

// Delete removes a cluster for this player. A missing row is not an error, so the
// store's declarative ApplyTopology can prune stale clusters idempotently.
func (r *GormContractClusterRepository) Delete(ctx context.Context, id string) error {
	if err := r.db.WithContext(ctx).
		Where("id = ? AND player_id = ?", id, r.playerID).
		Delete(&ContractClusterModel{}).Error; err != nil {
		return fmt.Errorf("delete contract cluster %q: %w", id, err)
	}
	return nil
}

// toModel serializes a domain cluster into its player-scoped row, JSON-encoding each
// element class.
func (r *GormContractClusterRepository) toModel(c *cluster.ContractCluster) (*ContractClusterModel, error) {
	warehouses, err := marshalClusterElements(c.Warehouses())
	if err != nil {
		return nil, fmt.Errorf("cluster %q warehouses: %w", c.ID(), err)
	}
	stockers, err := marshalClusterElements(c.Stockers())
	if err != nil {
		return nil, fmt.Errorf("cluster %q stockers: %w", c.ID(), err)
	}
	deliveryHulls, err := marshalClusterElements(c.DeliveryHulls())
	if err != nil {
		return nil, fmt.Errorf("cluster %q delivery hulls: %w", c.ID(), err)
	}
	sourceHubs, err := marshalClusterElements(c.SourceHubs())
	if err != nil {
		return nil, fmt.Errorf("cluster %q source hubs: %w", c.ID(), err)
	}
	return &ContractClusterModel{
		ID:            c.ID(),
		PlayerID:      r.playerID,
		Warehouses:    warehouses,
		Stockers:      stockers,
		DeliveryHulls: deliveryHulls,
		SourceHubs:    sourceHubs,
	}, nil
}

// toEntity rebuilds a domain cluster from its row, decoding each element class and
// running the result back through the domain constructor so its invariants hold.
func (r *GormContractClusterRepository) toEntity(m *ContractClusterModel) (*cluster.ContractCluster, error) {
	warehouses, err := unmarshalClusterElements(m.Warehouses)
	if err != nil {
		return nil, fmt.Errorf("cluster %q warehouses: %w", m.ID, err)
	}
	stockers, err := unmarshalClusterElements(m.Stockers)
	if err != nil {
		return nil, fmt.Errorf("cluster %q stockers: %w", m.ID, err)
	}
	deliveryHulls, err := unmarshalClusterElements(m.DeliveryHulls)
	if err != nil {
		return nil, fmt.Errorf("cluster %q delivery hulls: %w", m.ID, err)
	}
	sourceHubs, err := unmarshalClusterElements(m.SourceHubs)
	if err != nil {
		return nil, fmt.Errorf("cluster %q source hubs: %w", m.ID, err)
	}
	return cluster.NewContractCluster(m.ID, warehouses, stockers, deliveryHulls, sourceHubs)
}

// marshalClusterElements JSON-encodes one element class, storing an empty class as the
// empty array "[]" rather than "null" so the column shape is stable.
func marshalClusterElements(elems []cluster.Element) (string, error) {
	if len(elems) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(elems)
	if err != nil {
		return "", fmt.Errorf("marshal cluster elements: %w", err)
	}
	return string(b), nil
}

// unmarshalClusterElements decodes one element class; a NULL/empty/"null" column reads
// as an absent class (nil slice).
func unmarshalClusterElements(raw string) ([]cluster.Element, error) {
	if raw == "" || raw == "null" {
		return nil, nil
	}
	var elems []cluster.Element
	if err := json.Unmarshal([]byte(raw), &elems); err != nil {
		return nil, fmt.Errorf("unmarshal cluster elements: %w", err)
	}
	if len(elems) == 0 {
		return nil, nil
	}
	return elems, nil
}

// Verify the adapter satisfies the application-layer driven port.
var _ clusterstore.Repository = (*GormContractClusterRepository)(nil)
