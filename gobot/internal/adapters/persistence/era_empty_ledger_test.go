package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// sp-2ms9x: AN EMPTY LEDGER IS NOT A ZERO BALANCE.
//
// GORM's Find into a SINGLE STRUCT does not return ErrRecordNotFound: an empty result leaves the
// struct zero-valued with a nil error, so an unreadable balance silently became the VALUE zero.
// Both callers of anchoredCredits write that figure to the database as the era's final_credits, so a
// fabricated zero becomes a permanent, wrong historical record.

// AN EMPTY LEDGER MUST BE DISTINGUISHABLE FROM A REAL ZERO AT THE CALL SITE.
//
// It does NOT block the close: an earlier version of this fix errored here, and that broke era
// TRANSITION — a universe flip whose closing era has no recorded transactions is real and reachable
// (TestTransition_MintPathPersistsANonNullEraFaction caught it). Blocking the flip is worse than
// recording an unknown. What must not happen is the SILENT zero: anchoredCredits now reports
// known=false and the caller says so.
func TestCloseEra_AnEmptyLedgerIsReportedAsUnknownNotABalance(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	reset := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, db.Create(&persistence.EraModel{
		Name: "empty-era", AgentSymbol: "TORWIND", PlayerID: 1, UniverseResetDate: &reset,
	}).Error)

	// NON-VACUITY: the ledger really is empty for this player, which is the whole premise.
	var txCount int64
	require.NoError(t, db.Model(&persistence.TransactionModel{}).Where("player_id = ?", 1).Count(&txCount).Error)
	require.Zero(t, txCount, "fixture is inert: the player has transactions, so the empty-ledger path is not exercised")

	repo := persistence.NewEraRepository(db)
	report, err := repo.CloseEra(context.Background(), "empty-era")

	// The close proceeds — blocking it would strand an era flip.
	require.NoError(t, err)
	require.NotNil(t, report)
	require.Zero(t, report.FinalCredits)
	require.False(t, report.FinalCreditsKnown,
		"the report claims its 0 is a READING. An empty ledger and a bankrupt agent both produce 0, so without this flag the caller — and this test — cannot tell them apart, which is the whole defect")
}

// A REAL BALANCE STILL READS NORMALLY — including a genuine zero, which must NOT be confused with
// the empty case. This is the other half of "distinguishable": both directions.
func TestCloseEra_ReadsAGenuineZeroBalanceAsAValue(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	reset := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	player := persistence.PlayerModel{AgentSymbol: "BROKE-AGENT", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	require.NoError(t, db.Create(&persistence.EraModel{
		Name: "broke-era", AgentSymbol: "BROKE-AGENT", PlayerID: player.ID, UniverseResetDate: &reset,
	}).Error)
	// A recorded transaction that genuinely left the agent at zero credits.
	require.NoError(t, db.Create(&persistence.TransactionModel{
		ID: "tx-broke", PlayerID: player.ID, TransactionType: "PURCHASE_CARGO",
		Amount: -100, BalanceAfter: 0, Timestamp: time.Now().UTC(),
	}).Error)

	repo := persistence.NewEraRepository(db)
	report, err := repo.CloseEra(context.Background(), "broke-era")

	require.NoError(t, err, "a genuinely zero balance is a READABLE value and must close normally — refusing it would trade one indistinguishable case for another")
	require.NotNil(t, report)
	require.Zero(t, report.FinalCredits)
	require.True(t, report.FinalCreditsKnown,
		"a recorded zero balance IS a reading and must be reported as one, or the fix trades one indistinguishable case for another")
}
