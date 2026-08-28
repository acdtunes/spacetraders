package persistence

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"gorm.io/gorm"
)

// GormTransactionRepository implements TransactionRepository using GORM
type GormTransactionRepository struct {
	db *gorm.DB
}

// NewGormTransactionRepository creates a new GORM transaction repository
func NewGormTransactionRepository(db *gorm.DB) *GormTransactionRepository {
	return &GormTransactionRepository{db: db}
}

// Create persists a new transaction
func (r *GormTransactionRepository) Create(ctx context.Context, transaction *ledger.Transaction) error {
	model, err := r.transactionToModel(transaction)
	if err != nil {
		return fmt.Errorf("failed to convert transaction to model: %w", err)
	}

	result := r.db.WithContext(ctx).Create(model)
	if result.Error != nil {
		return fmt.Errorf("failed to create transaction: %w", result.Error)
	}

	return nil
}

// FindByID retrieves a transaction by its ID
func (r *GormTransactionRepository) FindByID(ctx context.Context, id ledger.TransactionID, playerID shared.PlayerID) (*ledger.Transaction, error) {
	var model TransactionModel
	result := r.db.WithContext(ctx).
		Where("id = ? AND player_id = ?", id.String(), playerID.Value()).
		First(&model)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, &ledger.ErrTransactionNotFound{
				ID:       id.String(),
				PlayerID: playerID.Value(),
			}
		}
		return nil, fmt.Errorf("failed to find transaction: %w", result.Error)
	}

	return r.modelToTransaction(&model)
}

// FindByPlayer retrieves transactions for a player with optional filtering
func (r *GormTransactionRepository) FindByPlayer(ctx context.Context, playerID shared.PlayerID, opts ledger.QueryOptions) ([]*ledger.Transaction, error) {
	query := r.db.WithContext(ctx).Where("player_id = ?", playerID.Value())

	query = r.applyFilters(query, opts)

	orderBy := "timestamp DESC"
	if opts.OrderBy != "" {
		orderBy = opts.OrderBy
	}
	query = query.Order(orderBy)

	if opts.Limit > 0 {
		query = query.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		query = query.Offset(opts.Offset)
	}

	var models []TransactionModel
	result := query.Find(&models)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to find transactions: %w", result.Error)
	}

	transactions := make([]*ledger.Transaction, len(models))
	for i, model := range models {
		tx, err := r.modelToTransaction(&model)
		if err != nil {
			return nil, fmt.Errorf("failed to convert transaction model: %w", err)
		}
		transactions[i] = tx
	}

	return transactions, nil
}

// CountByPlayer returns the count of transactions matching the criteria
func (r *GormTransactionRepository) CountByPlayer(ctx context.Context, playerID shared.PlayerID, opts ledger.QueryOptions) (int, error) {
	query := r.db.WithContext(ctx).Model(&TransactionModel{}).Where("player_id = ?", playerID.Value())

	query = r.applyFilters(query, opts)

	var count int64
	result := query.Count(&count)
	if result.Error != nil {
		return 0, fmt.Errorf("failed to count transactions: %w", result.Error)
	}

	return int(count), nil
}

// PerOriginGateFees aggregates recorded jump fees into a {departure system: mean fee}
// table.
//
// WHY THE MEAN AND NOT THE MEDIAN, given sp-wtc47 argued the mean for the FLEET constant:
// there the estimator had to price a SUM of n crossings, so E[total] = n x E[fee] forced
// the mean. Here the estimator prices ONE crossing of ONE known gate, and within a single
// gate the spread is 2.38% of that gate's own mean with no meaningful skew — mean and
// median agree to well under a percent, so the mean is chosen for continuity with the
// constant it refines rather than because the distribution demands it.
//
// amount is stored NEGATIVE for an expense, so it is negated back to a positive fee. Rows
// without an origin are excluded rather than bucketed as blank — see the port doc.
func (r *GormTransactionRepository) PerOriginGateFees(
	ctx context.Context, playerID shared.PlayerID, since time.Time,
) (map[string]int64, error) {
	type row struct {
		OriginSystem string
		MeanFee      float64
	}
	var rows []row
	result := r.db.WithContext(ctx).Model(&TransactionModel{}).
		Select("metadata->>'origin_system' AS origin_system, AVG(-amount) AS mean_fee").
		Where("player_id = ?", playerID.Value()).
		Where("transaction_type = ?", string(ledger.TransactionTypeJump)).
		Where("timestamp >= ?", since).
		Where("metadata->>'origin_system' IS NOT NULL").
		Where("metadata->>'origin_system' <> ''").
		Group("metadata->>'origin_system'").
		Scan(&rows)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to aggregate per-origin gate fees: %w", result.Error)
	}

	fees := make(map[string]int64, len(rows))
	for _, rw := range rows {
		// A non-positive mean cannot be a real gate fee. Dropping it here means the
		// solver falls back to its flat charge instead of pricing a crossing at or below
		// zero, which is the one direction that biases every candidate toward crossing.
		if rw.MeanFee <= 0 {
			continue
		}
		fees[rw.OriginSystem] = int64(math.Round(rw.MeanFee))
	}
	return fees, nil
}

// aggregateTime scans a timestamp that came out of a SQL AGGREGATE rather than off a typed
// column. Postgres hands back a time.Time; sqlite loses the column's datetime affinity
// through MAX() and hands back a bare string, which no standard destination will take. A
// per-row query would dodge that (LatestLogTimestamps does exactly this) but is only
// affordable when the keys number in the tens, and markets do not.
//
// An unparseable value scans as the zero time rather than erroring: the caller already drops
// undated rows, and one odd row must not fail a whole ranking read.
type aggregateTime struct{ time.Time }

// Value is never used to WRITE — this type only ever appears in a read destination — but GORM
// refuses to treat a struct field as a scalar column unless both halves of the interface pair
// are present, and without it the field is parsed as a relation.
func (a aggregateTime) Value() (driver.Value, error) { return a.Time, nil }

func (a *aggregateTime) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		a.Time = time.Time{}
	case time.Time:
		a.Time = v
	case []byte:
		a.Time = parseAggregateTime(string(v))
	case string:
		a.Time = parseAggregateTime(v)
	default:
		a.Time = time.Time{}
	}
	return nil
}

func parseAggregateTime(s string) time.Time {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05",
	} {
		if at, err := time.Parse(layout, s); err == nil {
			return at
		}
	}
	return time.Time{}
}

var _ ledger.OwnTradeRecencyReader = (*GormTransactionRepository)(nil)

// LastTradeByWaypoint reports the most recent cargo buy or sell the fleet booked at each
// waypoint inside the window — see ledger.OwnTradeRecencyReader.
//
// Only the two CARGO types count. A refuel, a jump toll or a hull purchase moves credits
// without moving a tradeable good's price, so counting them would report ground as worked
// that nobody traded on.
func (r *GormTransactionRepository) LastTradeByWaypoint(
	ctx context.Context, playerID shared.PlayerID, since time.Time,
) (map[string]time.Time, error) {
	type row struct {
		Waypoint  string
		LastTrade aggregateTime
	}
	var rows []row
	result := r.db.WithContext(ctx).Model(&TransactionModel{}).
		Select("metadata->>'waypoint' AS waypoint, MAX(timestamp) AS last_trade").
		Where("player_id = ?", playerID.Value()).
		Where("transaction_type IN ?", []string{
			string(ledger.TransactionTypePurchaseCargo),
			string(ledger.TransactionTypeSellCargo),
		}).
		// `timestamp`, not created_at: the half of idx_player_timestamp that bounds this scan.
		Where("timestamp >= ?", since).
		Where("metadata->>'waypoint' IS NOT NULL").
		Where("metadata->>'waypoint' <> ''").
		Group("metadata->>'waypoint'").
		Scan(&rows)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to aggregate last trade by waypoint: %w", result.Error)
	}

	last := make(map[string]time.Time, len(rows))
	for _, rw := range rows {
		if rw.LastTrade.IsZero() {
			continue // an unstamped row cannot date anything
		}
		last[rw.Waypoint] = rw.LastTrade.Time
	}
	return last, nil
}

var _ ledger.CategoryTotalsReader = (*GormTransactionRepository)(nil)

// CategoryTotals sums signed amounts per category in one aggregate query — see
// ledger.CategoryTotalsReader. A category absent from the window is simply absent from the
// map, not a zero entry.
func (r *GormTransactionRepository) CategoryTotals(
	ctx context.Context, playerID shared.PlayerID, since, until time.Time,
) (map[string]int64, error) {
	type row struct {
		Category string
		Total    int64
	}
	var rows []row
	result := r.db.WithContext(ctx).Model(&TransactionModel{}).
		Select("category, SUM(amount) AS total").
		Where("player_id = ?", playerID.Value()).
		Where("timestamp >= ?", since).
		Where("timestamp <= ?", until).
		Group("category").
		Scan(&rows)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to aggregate category totals: %w", result.Error)
	}

	totals := make(map[string]int64, len(rows))
	for _, rw := range rows {
		totals[rw.Category] = rw.Total
	}
	return totals, nil
}

var _ ledger.TreasuryHighWaterReader = (*GormTransactionRepository)(nil)

// TreasuryHighWaterSince reports the window's peak balance, and whether it held anything to read.
func (r *GormTransactionRepository) TreasuryHighWaterSince(
	ctx context.Context, playerID shared.PlayerID, since time.Time,
) (int64, bool, error) {
	// EMPTY IS NOT ZERO, and an aggregate has TWO empty shapes: a bare int64 destination would
	// report a fleet that has never traded as one that has never held a credit, and a MAX over an
	// empty window arrives as ONE row of SQL NULL rather than as zero rows. A nullable element in a
	// slice covers both, and keeps both distinct from a genuine zero balance.
	var peaks []sql.NullInt64
	err := r.db.WithContext(ctx).Model(&TransactionModel{}).
		Select("MAX(balance_after)").
		Where("player_id = ?", playerID.Value()).
		// `timestamp`, not created_at: the half of idx_player_timestamp that bounds this scan.
		Where("timestamp >= ?", since).
		Scan(&peaks).Error
	if err != nil {
		return 0, false, fmt.Errorf("failed to read treasury high-water: %w", err)
	}
	if len(peaks) == 0 || !peaks[0].Valid {
		return 0, false, nil
	}
	return peaks[0].Int64, true, nil
}

// applyFilters applies query options to a GORM query
func (r *GormTransactionRepository) applyFilters(query *gorm.DB, opts ledger.QueryOptions) *gorm.DB {
	if opts.StartDate != nil {
		query = query.Where("timestamp >= ?", *opts.StartDate)
	}
	if opts.EndDate != nil {
		query = query.Where("timestamp <= ?", *opts.EndDate)
	}

	if opts.Category != nil {
		query = query.Where("category = ?", opts.Category.String())
	}

	if opts.TransactionType != nil {
		query = query.Where("transaction_type = ?", opts.TransactionType.String())
	}

	if opts.RelatedEntityType != nil {
		query = query.Where("related_entity_type = ?", *opts.RelatedEntityType)
	}
	if opts.RelatedEntityID != nil {
		query = query.Where("related_entity_id = ?", *opts.RelatedEntityID)
	}

	return query
}

// modelToTransaction converts database model to domain entity
func (r *GormTransactionRepository) modelToTransaction(model *TransactionModel) (*ledger.Transaction, error) {
	id, err := ledger.NewTransactionIDFromString(model.ID)
	if err != nil {
		return nil, fmt.Errorf("invalid transaction ID in database: %w", err)
	}

	playerID, err := shared.NewPlayerID(model.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("invalid player ID in database: %w", err)
	}

	transactionType, err := ledger.ParseTransactionType(model.TransactionType)
	if err != nil {
		return nil, fmt.Errorf("invalid transaction type in database: %w", err)
	}

	var metadata map[string]interface{}
	if model.Metadata != "" {
		if err := json.Unmarshal([]byte(model.Metadata), &metadata); err != nil {
			// If unmarshal fails, leave metadata as nil
			metadata = nil
		}
	}

	// Reconstruct transaction entity. category is intentionally NOT read from the
	// model: ReconstructTransaction re-derives it from type, so a
	// divergent or invalid stored category can never surface on read.
	return ledger.ReconstructTransaction(
		id,
		playerID,
		model.Timestamp,
		transactionType,
		model.Amount,
		model.BalanceBefore,
		model.BalanceAfter,
		model.Description,
		metadata,
		model.RelatedEntityType,
		model.RelatedEntityID,
		model.OperationType,
	), nil
}

// transactionToModel converts domain entity to database model
func (r *GormTransactionRepository) transactionToModel(tx *ledger.Transaction) (*TransactionModel, error) {
	var metadataJSON string
	if tx.Metadata() != nil {
		bytes, err := json.Marshal(tx.Metadata())
		if err != nil {
			return nil, fmt.Errorf("failed to marshal metadata: %w", err)
		}
		metadataJSON = string(bytes)
	}

	return &TransactionModel{
		ID:                tx.ID().String(),
		PlayerID:          tx.PlayerID().Value(),
		Timestamp:         tx.Timestamp(),
		TransactionType:   tx.TransactionType().String(),
		Category:          tx.Category().String(),
		Amount:            tx.Amount(),
		BalanceBefore:     tx.BalanceBefore(),
		BalanceAfter:      tx.BalanceAfter(),
		Description:       tx.Description(),
		Metadata:          metadataJSON,
		RelatedEntityType: tx.RelatedEntityType(),
		RelatedEntityID:   tx.RelatedEntityID(),
		OperationType:     tx.OperationType(),
	}, nil
}
