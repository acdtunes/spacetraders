package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"gorm.io/gorm"
)

// GormContractRepository implements ContractRepository using GORM
type GormContractRepository struct {
	db *gorm.DB
}

// NewGormContractRepository creates a new GORM contract repository
func NewGormContractRepository(db *gorm.DB) *GormContractRepository {
	return &GormContractRepository{db: db}
}

// FindByID retrieves a contract by ID
func (r *GormContractRepository) FindByID(ctx context.Context, contractID string) (*contract.Contract, error) {
	var model ContractModel
	result := r.db.WithContext(ctx).Where("id = ?", contractID).First(&model)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("contract not found: %s", contractID)
		}
		return nil, fmt.Errorf("failed to find contract: %w", result.Error)
	}

	return r.modelToEntity(&model)
}

// FindActiveContracts retrieves all active contracts for a player (accepted but not fulfilled)
func (r *GormContractRepository) FindActiveContracts(ctx context.Context, playerID int) ([]*contract.Contract, error) {
	var models []ContractModel
	result := r.db.WithContext(ctx).
		Where("player_id = ? AND accepted = ? AND fulfilled = ?", playerID, true, false).
		Find(&models)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to find active contracts: %w", result.Error)
	}

	contracts := make([]*contract.Contract, 0, len(models))
	for _, model := range models {
		entity, err := r.modelToEntity(&model)
		if err != nil {
			return nil, fmt.Errorf("failed to convert contract %s: %w", model.ID, err)
		}
		contracts = append(contracts, entity)
	}

	return contracts, nil
}

// Add persists a contract to the database
func (r *GormContractRepository) Add(ctx context.Context, c *contract.Contract) error {
	model, err := r.entityToModel(c)
	if err != nil {
		return fmt.Errorf("failed to convert contract to model: %w", err)
	}

	result := r.db.WithContext(ctx).Save(model)
	if result.Error != nil {
		return fmt.Errorf("failed to add contract: %w", result.Error)
	}

	return nil
}

// RecentContractDemand is one recent contract's demand signal for the contract-hub
// placement coordinator: the DISTINCT goods it required plus its payment-on-fulfilled.
// The hub coordinator folds a SEQUENCE of these into an EWMA per good (payment ×
// recurrence, smoothed), so single-contract noise cannot move a hauler's home. It is a
// read-only projection over the contracts table — no schema change.
type RecentContractDemand struct {
	Goods              []string
	PaymentOnFulfilled int
}

// RecentContractDemand returns up to `limit` of the player's most-recent contracts as
// demand rows, ordered OLDEST→NEWEST — the order the hub coordinator's EWMA folds them
// (a recurring good keeps a high smoothed weight; a one-off decays). last_updated is an
// ISO-8601 string, so a lexicographic DESC sort is chronological newest-first; the slice
// is then reversed for the fold. A single unparseable deliveries blob is skipped, never
// fatal, so one corrupt row can never blind placement (the coordinator is fail-safe).
func (r *GormContractRepository) RecentContractDemand(ctx context.Context, playerID, limit int) ([]RecentContractDemand, error) {
	if limit <= 0 {
		limit = defaultRecentContractDemandLimit
	}

	var models []ContractModel
	result := r.db.WithContext(ctx).
		Where("player_id = ?", playerID).
		Order("last_updated DESC").
		Limit(limit).
		Find(&models)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to read recent contracts for demand: %w", result.Error)
	}

	out := make([]RecentContractDemand, 0, len(models))
	for i := len(models) - 1; i >= 0; i-- { // reverse newest-first → oldest→newest for the EWMA
		var deliveries []contract.Delivery
		if err := json.Unmarshal([]byte(models[i].DeliveriesJSON), &deliveries); err != nil {
			continue // one corrupt blob must not blind the whole demand read
		}
		seen := make(map[string]struct{}, len(deliveries))
		var goods []string
		for _, d := range deliveries {
			if _, ok := seen[d.TradeSymbol]; ok {
				continue
			}
			seen[d.TradeSymbol] = struct{}{}
			goods = append(goods, d.TradeSymbol)
		}
		out = append(out, RecentContractDemand{Goods: goods, PaymentOnFulfilled: models[i].PaymentOnFulfilled})
	}
	return out, nil
}

// defaultRecentContractDemandLimit bounds the demand read to the recent contract history
// (an era ran ~46 contracts; 200 comfortably covers it). It is a query bound, not an
// operational threshold — the EWMA half-life (the tuned knob) lives on the coordinator.
const defaultRecentContractDemandLimit = 200

// modelToEntity converts database model to domain entity
func (r *GormContractRepository) modelToEntity(model *ContractModel) (*contract.Contract, error) {
	var deliveries []contract.Delivery
	if err := json.Unmarshal([]byte(model.DeliveriesJSON), &deliveries); err != nil {
		return nil, fmt.Errorf("failed to unmarshal deliveries: %w", err)
	}

	terms := contract.Terms{
		Payment: contract.Payment{
			OnAccepted:  model.PaymentOnAccepted,
			OnFulfilled: model.PaymentOnFulfilled,
		},
		Deliveries:       deliveries,
		DeadlineToAccept: model.DeadlineToAccept,
		Deadline:         model.Deadline,
	}

	playerID, err := shared.NewPlayerID(model.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("invalid player ID in database: %w", err)
	}
	c, err := contract.NewContract(
		model.ID,
		playerID,
		model.FactionSymbol,
		model.Type,
		terms,
		nil, // Use default RealClock
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create contract entity: %w", err)
	}

	// accepted/fulfilled are private fields with no setter; restore them by replaying
	// the same domain transitions (Accept/Fulfill) the persisted flags say occurred.
	if model.Accepted {
		if err := c.Accept(); err != nil {
			return nil, fmt.Errorf("failed to set accepted state: %w", err)
		}
	}
	if model.Fulfilled {
		if err := c.Fulfill(); err != nil {
			// Fulfill requires CanFulfill (deliveries complete); a failure here means the
			// persisted deliveries disagree with the persisted Fulfilled flag — a data
			// integrity issue, not a normal error path.
			return nil, fmt.Errorf("failed to set fulfilled state: %w", err)
		}
	}

	return c, nil
}

// entityToModel converts domain entity to database model
func (r *GormContractRepository) entityToModel(c *contract.Contract) (*ContractModel, error) {
	deliveriesJSON, err := json.Marshal(c.Terms().Deliveries)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal deliveries: %w", err)
	}

	return &ContractModel{
		ID:                 c.ContractID(),
		PlayerID:           c.PlayerID().Value(),
		FactionSymbol:      c.FactionSymbol(),
		Type:               c.Type(),
		Accepted:           c.Accepted(),
		Fulfilled:          c.Fulfilled(),
		DeadlineToAccept:   c.Terms().DeadlineToAccept,
		Deadline:           c.Terms().Deadline,
		PaymentOnAccepted:  c.Terms().Payment.OnAccepted,
		PaymentOnFulfilled: c.Terms().Payment.OnFulfilled,
		DeliveriesJSON:     string(deliveriesJSON),
		LastUpdated:        time.Now().UTC().Format(time.RFC3339),
	}, nil
}
