package cargo

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	ledgerCommands "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// sp-br0m: a factory input buy records the a5j7 selector branch that chose its source into the
// PURCHASE_CARGO transaction metadata (beside good_symbol), so the analyst can grade A1
// (supply-first compliance) straight from the ledger and split legal RESCUE buys from
// violations. These pins run the REAL CargoTransactionHandler through the REAL ledger recorder
// onto an FK-enforcing SQLite DB — the same harness as the optype pins — and read the tag back
// off the persisted row, proving the full write -> serialize -> persist -> read -> unmarshal
// round-trip. They also pin that a caller that did NOT stamp a branch (every non-factory cargo
// path: trade, tour, arb, contract delivery, refuel, the fabricated-output harvest) records NO
// selector_branch key, so their rows are byte-identical to before.

// persistedPurchaseMetadata dispatches one PURCHASE_CARGO through the real handler + ledger and
// returns the metadata map persisted on the single resulting row. A non-empty branch is stamped
// onto ctx via shared.WithSelectorBranch, exactly as production_executor.buyGood does for a
// factory input buy; branch "" models every other cargo caller (no stamp).
func persistedPurchaseMetadata(t *testing.T, branch string) map[string]interface{} {
	t.Helper()

	db, err := database.NewTestConnection()
	require.NoError(t, err)

	// Seed the FK parent: transactions.player_id references players.id, and the test harness
	// enforces foreign keys (a bare write would 23503 like production).
	p := persistence.PlayerModel{AgentSymbol: "AGT", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&p).Error)
	playerID := p.ID

	med := &ledgerRoutingMediator{record: ledgerCommands.NewRecordTransactionHandler(persistence.NewGormTransactionRepository(db), nil)}
	api := &optypeFakeAPI{}
	playerRepo := &buyFakePlayerRepo{player: player.NewPlayer(shared.MustNewPlayerID(playerID), "ENDURANCE", "tok")}
	marketRepo := &buyFakeMarketRepo{}
	shipRepo := &buyFakeShipRepo{ship: newDockedShipWithCargo(t, playerID, optypeGood, 20)}

	ctx := auth.WithPlayerToken(context.Background(), "tok")
	if branch != "" {
		ctx = shared.WithSelectorBranch(ctx, branch)
	}

	h := NewPurchaseCargoHandler(shipRepo, playerRepo, api, marketRepo, med, nil)
	_, err = h.Handle(ctx, &PurchaseCargoCommand{ShipSymbol: "OPTYPE-1", GoodSymbol: optypeGood, Units: 10, PlayerID: shared.MustNewPlayerID(playerID)})
	require.NoError(t, err)

	var rows []persistence.TransactionModel
	require.NoError(t, db.Where("player_id = ?", playerID).Find(&rows).Error)
	require.Len(t, rows, 1, "expected exactly one persisted ledger row")

	var metadata map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(rows[0].Metadata), &metadata))
	return metadata
}

// An input buy stamped ELIGIBLE round-trips the tag onto the persisted PURCHASE_CARGO row,
// beside an intact good_symbol.
func TestPurchaseMetadata_SelectorBranchEligibleRoundTrips(t *testing.T) {
	metadata := persistedPurchaseMetadata(t, "eligible_supply_first")
	require.Equal(t, "eligible_supply_first", metadata["selector_branch"], "eligible input buy must persist selector_branch=eligible_supply_first")
	require.Equal(t, optypeGood, metadata["good_symbol"], "the tag rides beside good_symbol, which stays intact")
}

// An input buy stamped RESCUE round-trips the rescue tag — the branch A1 must not score as a
// violation.
func TestPurchaseMetadata_SelectorBranchRescueRoundTrips(t *testing.T) {
	metadata := persistedPurchaseMetadata(t, "rescue")
	require.Equal(t, "rescue", metadata["selector_branch"], "rescue input buy must persist selector_branch=rescue")
}

// A caller that stamped no branch records NO selector_branch key — its metadata is unchanged by
// this feature, so every non-factory cargo path is untouched.
func TestPurchaseMetadata_NoBranchNoTag(t *testing.T) {
	metadata := persistedPurchaseMetadata(t, "")
	_, present := metadata["selector_branch"]
	require.False(t, present, "an unstamped cargo buy must not carry a selector_branch tag")
}
