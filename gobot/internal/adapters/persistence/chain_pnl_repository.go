package persistence

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// GormChainPnLRepository reads the per-good realized-P&L aggregates the factory chain
// kill-switch judges. It satisfies the application-layer ChainPnLReader interface
// STRUCTURALLY against the domain types (manufacturing.ChainPnLRaw) — so persistence need not
// import the application service, avoiding an import cycle, exactly as
// GormMarketPriceHistoryRepository satisfies InputPriceHistoryReader via domain/market types.
// It runs, Go-side, the SAME per-good attribution the validated manufacturing dashboard uses
// (panel 502 / sp-i0hl): transactions.metadata->>'good_symbol' for factory local buys/sells
// under the manufacturing/factory operation types, AND — since sp-461l — for tour realized net
// under operation_type='tour' too. The tour net formerly netted tour_leg_telemetry, which sp-rd21
// proved read ~2x inflated (dropped buy legs); it now reads the treasury-true transactions ledger,
// so every per-good flow this reader returns reconciles to the treasury on one consistent source.
// It adds the refuel pool the dashboard omits (attributed per-good in the service's ComputeChainPnL
// — refuel rows carry no good_symbol). PostgreSQL-specific (JSONB ->>, FILTER), matching the sibling
// manufacturing metrics collector; the daemon only ever wires it against Postgres.
type GormChainPnLRepository struct {
	db *gorm.DB
}

// NewGormChainPnLRepository builds the reader from the daemon's GORM handle.
func NewGormChainPnLRepository(db *gorm.DB) *GormChainPnLRepository {
	return &GormChainPnLRepository{db: db}
}

// ReadRealizedPnL returns the per-good factory + tour realized flows plus the manufacturing
// refuel pool over [since, now) for the given player. Amounts keep the transactions table's
// signs: spend negative, income positive (so PURCHASE_CARGO sums negative, SELL_CARGO positive).
func (r *GormChainPnLRepository) ReadRealizedPnL(ctx context.Context, playerID int, since time.Time) (manufacturing.ChainPnLRaw, error) {
	db := r.db.WithContext(ctx)

	// Per-good factory flows: input buys (negative) and local sells (positive), attributed to
	// the good literally transacted (atomic attribution), scoped to the manufacturing and
	// factory operation types the panel filters on.
	var factoryRows []struct {
		Good        string
		FactoryCost int
		FactorySell int
	}
	if err := db.Raw(`
		SELECT metadata->>'good_symbol' AS good,
		       COALESCE(SUM(amount) FILTER (WHERE transaction_type = 'PURCHASE_CARGO'), 0) AS factory_cost,
		       COALESCE(SUM(amount) FILTER (WHERE transaction_type = 'SELL_CARGO'), 0) AS factory_sell
		FROM transactions
		WHERE operation_type IN ('manufacturing', 'factory_workflow')
		  AND metadata->>'good_symbol' IS NOT NULL
		  AND created_at >= ?
		  AND player_id = ?
		GROUP BY 1
	`, since, playerID).Scan(&factoryRows).Error; err != nil {
		return manufacturing.ChainPnLRaw{}, err
	}

	// Refuel pool: manufacturing/factory refuel spend over the window (no good_symbol on refuel
	// rows, so a single scalar — attributed per-good downstream). Negative (spend).
	var refuelPool int
	if err := db.Raw(`
		SELECT COALESCE(SUM(amount), 0)
		FROM transactions
		WHERE operation_type IN ('manufacturing', 'factory_workflow')
		  AND transaction_type = 'REFUEL'
		  AND created_at >= ?
		  AND player_id = ?
	`, since, playerID).Scan(&refuelPool).Error; err != nil {
		return manufacturing.ChainPnLRaw{}, err
	}

	// Per-good tour realized net from the TRANSACTIONS ledger (sp-461l, epic sp-g9td): a signed
	// SUM(amount) over the tour cargo trades — SELL_CARGO(+) and PURCHASE_CARGO(−) — scoped to
	// operation_type='tour' and attributed to metadata->>'good_symbol', the SAME treasury-true,
	// per-good source the factory-flows CTE above reads. It REPLACES the former tour_leg_telemetry
	// netting (SUM(sign(is_buy)·realized_units·realized_unit_price)) that sp-rd21 proved read ~2x
	// inflated: telemetry dropped ~1/3 of buy legs while their sells stayed logged, so net = sells −
	// (partial buys) over-counted. The ledger records EVERY cargo trade and reconciles to the
	// treasury, so this is the definitive per-good tour net the factory chain kill-switch judges.
	// REFUEL is excluded (it carries no good_symbol and the prior CTE summed only cargo legs), so
	// the semantics — signed cargo net per good — are preserved, only the source is now treasury-true.
	var tourRows []struct {
		Good    string
		TourNet int
	}
	if err := db.Raw(`
		SELECT metadata->>'good_symbol' AS good,
		       COALESCE(SUM(amount), 0) AS tour_net
		FROM transactions
		WHERE operation_type = 'tour'
		  AND transaction_type IN ('PURCHASE_CARGO', 'SELL_CARGO')
		  AND metadata->>'good_symbol' IS NOT NULL
		  AND created_at >= ?
		  AND player_id = ?
		GROUP BY 1
	`, since, playerID).Scan(&tourRows).Error; err != nil {
		return manufacturing.ChainPnLRaw{}, err
	}

	// Merge the two per-good sources on good (LEFT-JOIN-in-Go over the union of keys), so a good
	// with only factory activity OR only tour activity still appears.
	flows := make(map[string]*manufacturing.ChainGoodFlow)
	for _, row := range factoryRows {
		flows[row.Good] = &manufacturing.ChainGoodFlow{Good: row.Good, FactoryCost: row.FactoryCost, FactorySell: row.FactorySell}
	}
	for _, row := range tourRows {
		if f, ok := flows[row.Good]; ok {
			f.TourNet = row.TourNet
		} else {
			flows[row.Good] = &manufacturing.ChainGoodFlow{Good: row.Good, TourNet: row.TourNet}
		}
	}

	goods := make([]manufacturing.ChainGoodFlow, 0, len(flows))
	for _, f := range flows {
		goods = append(goods, *f)
	}
	return manufacturing.ChainPnLRaw{Goods: goods, RefuelPool: refuelPool}, nil
}
