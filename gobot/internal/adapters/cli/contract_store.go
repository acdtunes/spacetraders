package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
)

// contractStore is the subset of contract persistence the CLI needs for
// read-only observability. Implemented directly against the contracts
// table since the domain repository only exposes active-contract queries.
type contractStore interface {
	ListContracts(ctx context.Context, playerID int) ([]persistence.ContractModel, error)
	FindContractsByIDPrefix(ctx context.Context, prefix string) ([]persistence.ContractModel, error)
}

// gormContractStore reads contract rows directly via GORM (read-only, no API calls).
type gormContractStore struct {
	db *gorm.DB
}

func newContractStore() (contractStore, error) {
	store, _, err := newContractStoreAndPlayerRepo()
	return store, err
}

// newContractStoreAndPlayerRepo builds the contract store together with a player
// repository backed by the same database connection, so `contract list` can resolve
// the default player without opening a second connection.
func newContractStoreAndPlayerRepo() (contractStore, player.PlayerRepository, error) {
	db, err := openDatabase()
	if err != nil {
		return nil, nil, err
	}

	return &gormContractStore{db: db}, persistence.NewGormPlayerRepository(db), nil
}

// demandMinerProvider is the read-only demand miner surface `contract demand` renders.
// Implemented by *persistence.DemandMiner; an interface so the renderer is unit-testable
// with a fake (mirrors the historyProvider seam).
type demandMinerProvider interface {
	Mine(ctx context.Context, homeSystem string, playerID int, eraID *int, opts persistence.DemandMinerOptions) ([]persistence.DemandCandidate, error)
}

// newDemandMinerAndPlayerRepo builds the demand miner and a player repository over one
// DB connection, so `contract demand` can resolve the default player and mine without
// opening a second connection (mirrors newContractStoreAndPlayerRepo).
func newDemandMinerAndPlayerRepo() (*persistence.DemandMiner, player.PlayerRepository, error) {
	db, err := openDatabase()
	if err != nil {
		return nil, nil, err
	}
	return persistence.NewDemandMiner(db), persistence.NewGormPlayerRepository(db), nil
}

// likeWildcardEscaper neutralises LIKE metacharacters so an operator-typed id is
// matched literally rather than as a pattern.
var likeWildcardEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func (s *gormContractStore) ListContracts(ctx context.Context, playerID int) ([]persistence.ContractModel, error) {
	var models []persistence.ContractModel
	result := s.db.WithContext(ctx).Where("player_id = ?", playerID).Find(&models)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to list contracts: %w", result.Error)
	}
	return models, nil
}

func (s *gormContractStore) FindContractsByIDPrefix(ctx context.Context, prefix string) ([]persistence.ContractModel, error) {
	var models []persistence.ContractModel
	result := s.db.WithContext(ctx).
		Where(`id LIKE ? ESCAPE '\'`, likeWildcardEscaper.Replace(prefix)+"%").
		Order("id").
		Find(&models)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to find contracts by id prefix %s: %w", prefix, result.Error)
	}
	return models, nil
}

// marshalDeliveries serializes deliveries the same way the contract
// repository persists them, for use by tests building fixtures.
func marshalDeliveries(deliveries []contract.Delivery) (string, error) {
	if deliveries == nil {
		deliveries = []contract.Delivery{}
	}
	data, err := json.Marshal(deliveries)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func unmarshalDeliveries(raw string) ([]contract.Delivery, error) {
	var deliveries []contract.Delivery
	if raw == "" {
		return deliveries, nil
	}
	if err := json.Unmarshal([]byte(raw), &deliveries); err != nil {
		return nil, fmt.Errorf("failed to unmarshal deliveries: %w", err)
	}
	return deliveries, nil
}
