package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
)

// transaction_position_repository.go — the reader behind position-matched realised accounting
// (sp-76r2c). It extracts cargo legs from the transactions ledger and defers to the pure
// matcher ledger.MatchPositions for every accounting decision, so the SQL read and the
// matching rule are independently testable — the same split RealizedCashRate uses.
//
// WHY IT READS THE FULL HISTORY AND NOT A WINDOW. Scoping the READ to a reporting window
// truncates the purchase stream: every position opened before the window boundary loses its
// purchase and degrades into an uncosted sale, which relocates the sp-76r2c artefact from the
// hour boundary to the window edge instead of removing it. Live p99 holding time is 25.6h and
// the maximum is 90h, so even a generous prologue would corrupt a six-hour report. The
// correct shape is: match everything, then filter the OUTPUT by ClosedAt
// (ledger.FilterClosedByWindow).
//
// The cost is bounded and small — 53k cargo rows live, growing ~2k/day, one indexed scan and
// an O(n log n) match. That is an analysis/report read, not a hot path.
//
// WHY THE METADATA IS PARSED IN GO. The legs come from metadata JSON (ship_symbol,
// good_symbol, units), and the live ledger contains TWO metadata shapes: the current one and a
// legacy manufacturing shape keyed good/quantity with no ship_symbol at all (226 purchases +
// 27 sales). Parsing in Go rather than with SQL JSON operators keeps the reader dialect-neutral
// across Postgres and the SQLite test harness AND lets an unattributable row be COUNTED
// (LegReadStats) rather than silently coerced to a zero-unit position.

// cargoLegTransactionTypes are the two ledger transaction types that open and close a cargo
// position. REFUEL is deliberately absent: it carries no good and no units, its metadata
// ship_symbol is the empty string on 66,635 of 66,639 live rows, and it is a cost of
// TRAVELLING rather than of a position. Folding it in would attribute fuel burn to whichever
// position happened to close nearby — a fresh instance of the very misattribution sp-76r2c
// documents. Subtract fuel at the fleet level (RealizedCashRate already does), never per
// position.
var cargoLegTransactionTypes = []string{"PURCHASE_CARGO", "SELL_CARGO"}

// LegReadStats records what the extraction saw, so a consumer can judge whether the matched
// figures cover the ledger. Rows that cannot be attributed to a position are COUNTED here and
// excluded from matching — never fed in as zero-unit legs, which would corrupt reconciliation
// while looking complete.
type LegReadStats struct {
	// RowsScanned is the number of PURCHASE_CARGO/SELL_CARGO rows read for the player.
	RowsScanned int
	// LegsMatched is the number of rows that carried a full position key and entered matching.
	LegsMatched int
	// UnattributableRows is the number of rows dropped for want of a ship_symbol, good_symbol
	// or units. Live this is the 253 legacy-metadata manufacturing rows. A rise here means a
	// new writer is emitting a metadata shape this reader does not understand, and the matched
	// figures have silently stopped covering part of the ledger.
	UnattributableRows int
	// UnattributableCredits is the absolute credit value of those dropped rows — the honest
	// measure of how much of the ledger the matched figures do NOT explain.
	UnattributableCredits int64
	// MalformedMetadataRows counts rows whose metadata would not parse as JSON at all.
	MalformedMetadataRows int
}

// Covered reports whether every scanned row was attributable. False means the matched figures
// are incomplete by UnattributableCredits, and a report should say so rather than present a
// total as if it were the whole ledger.
func (s LegReadStats) Covered() bool {
	return s.UnattributableRows == 0 && s.MalformedMetadataRows == 0
}

// cargoLegMetadata is the current metadata shape a position can be built from. The legacy
// manufacturing shape (good/quantity, no ship_symbol) leaves these zero-valued and the row is
// counted as unattributable rather than guessed at: without a hull there is no position
// stream to place it in, and inventing one would fabricate matches.
type cargoLegMetadata struct {
	ShipSymbol string          `json:"ship_symbol"`
	GoodSymbol string          `json:"good_symbol"`
	Units      json.RawMessage `json:"units"`
}

// ReadCargoLegs extracts every attributable cargo leg for a player from the transactions
// ledger, ascending by created_at. It reads the FULL history on purpose — see the file comment.
//
// created_at is the instant used, matching every other cash-true reader (RealizedCashRate,
// ReadRealizedPnL) so consumers share one basis. It differs from the `timestamp` column on
// every live row but by at most 0.16s, far below any bucket width, so the choice cannot
// reorder a real position.
func (r *GormTransactionRepository) ReadCargoLegs(ctx context.Context, playerID int) ([]ledger.CargoLeg, LegReadStats, error) {
	var rows []struct {
		ID              string
		TransactionType string
		Amount          int64
		OperationType   string
		Metadata        string
		CreatedAt       time.Time
	}

	err := r.db.WithContext(ctx).
		Model(&TransactionModel{}).
		Where("player_id = ?", playerID).
		Where("transaction_type IN ?", cargoLegTransactionTypes).
		Order("created_at ASC, id ASC").
		Select("id, transaction_type, amount, operation_type, metadata, created_at").
		Find(&rows).Error
	if err != nil {
		return nil, LegReadStats{}, fmt.Errorf("read cargo legs for player %d: %w", playerID, err)
	}

	stats := LegReadStats{RowsScanned: len(rows)}
	legs := make([]ledger.CargoLeg, 0, len(rows))

	for _, row := range rows {
		var meta cargoLegMetadata
		if err := json.Unmarshal([]byte(row.Metadata), &meta); err != nil {
			stats.MalformedMetadataRows++
			stats.UnattributableCredits += absInt64(row.Amount)
			continue
		}

		units, unitsOK := parseUnits(meta.Units)
		if meta.ShipSymbol == "" || meta.GoodSymbol == "" || !unitsOK || units <= 0 {
			stats.UnattributableRows++
			stats.UnattributableCredits += absInt64(row.Amount)
			continue
		}

		legs = append(legs, ledger.CargoLeg{
			TxID:          row.ID,
			Hull:          meta.ShipSymbol,
			Good:          meta.GoodSymbol,
			Units:         units,
			Amount:        row.Amount,
			OperationType: row.OperationType,
			At:            row.CreatedAt,
			IsBuy:         row.TransactionType == "PURCHASE_CARGO",
		})
	}

	stats.LegsMatched = len(legs)
	return legs, stats, nil
}

// ReadMatchedPositions reads the player's full cargo-leg history and returns the
// position-matched partition: realised margin attributed to CLOSING instants, open inventory
// at cost, and uncosted sales — the three buckets that between them account for every credit
// (MatchedPositions.ReconcilesTo).
//
// Scope a report by filtering the RESULT (ledger.FilterClosedByWindow), never by narrowing
// this read.
func (r *GormTransactionRepository) ReadMatchedPositions(ctx context.Context, playerID int) (ledger.MatchedPositions, LegReadStats, error) {
	legs, stats, err := r.ReadCargoLegs(ctx, playerID)
	if err != nil {
		return ledger.MatchedPositions{}, stats, err
	}
	return ledger.MatchPositions(legs), stats, nil
}

// parseUnits reads the units field, tolerating both the JSON number the current writer emits
// and a quoted string, so a formatting change in a writer degrades into a correct read rather
// than a silent zero.
func parseUnits(raw json.RawMessage) (int, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var asNumber int
	if err := json.Unmarshal(raw, &asNumber); err == nil {
		return asNumber, true
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err != nil {
		return 0, false
	}
	parsed, err := strconv.Atoi(asString)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
