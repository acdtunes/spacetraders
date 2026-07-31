package outfitting

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Acceptance gate for the module half of sp-shq63. A shipyard modification fee
// is the same defect shape as the jump gate fee: charged by the API, parsed into
// ModuleModificationResult.Fee, and — before this — consumed by nothing. The
// auto-outfit coordinator drives installs autonomously, so this was a live
// unrecorded spend, not a CLI-only edge case.
//
// As with jump, the falsifier is RECONCILIATION: the ledger's latest
// balance_after must equal the agent's post-fee credits as the API reported them
// in-band, not merely "a row exists".

// latestLedgerRow returns the most recent transaction, failing loudly on an empty
// ledger so a missing row can never read as a satisfied assertion.
func latestLedgerRow(t *testing.T, db *gorm.DB, playerID int) *ledger.Transaction {
	t.Helper()
	repo := persistence.NewGormTransactionRepository(db)
	pid, err := shared.NewPlayerID(playerID)
	require.NoError(t, err)
	txs, err := repo.FindByPlayer(context.Background(), pid, ledger.QueryOptions{
		Limit: 1, OrderBy: "timestamp DESC, created_at DESC, id DESC",
	})
	require.NoError(t, err)
	require.Len(t, txs, 1, "ledger is empty: the shipyard modification fee was never recorded")
	return txs[0]
}

// seedOutfitShipRow persists the hull the modification acts on. cargoUnits must
// agree with cargoJSON — the ship repository rejects an inventory whose unit sum
// disagrees with the total, so a mismatched fixture fails before reaching the
// code under test.
func seedOutfitShipRow(t *testing.T, db *gorm.DB, pid int, modulesJSON, cargoJSON string, cargoUnits int) {
	t.Helper()
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "SHIP-1", PlayerID: pid,
		NavStatus: "DOCKED", LocationSymbol: "X1-JP61-A1", SystemSymbol: "X1-JP61", EngineSpeed: 10,
		CargoCapacity: 320, CargoUnits: cargoUnits,
		CargoInventory:   cargoJSON,
		Modules:          modulesJSON,
		AssignmentStatus: "idle",
	}).Error)
}

// TestInstallModuleFeeReconcilesLedgerWithLiveCredits kills "drop the ledger
// write" on the install path: with no row the ledger is empty and latestLedgerRow
// fails; with a row it must land exactly on the API's post-fee credits.
func TestInstallModuleFeeReconcilesLedgerWithLiveCredits(t *testing.T) {
	const startingCredits, fee = 800000, 4200
	postFeeCredits := startingCredits - fee

	fake := &outfitFakeAPIClient{
		shipData: &navigation.ShipData{Symbol: "SHIP-1", Location: "X1-JP61-A1", NavStatus: "DOCKED", CargoCapacity: 320, EngineSpeed: 10, FrameSymbol: "FRAME_FRIGATE", Role: "HAULER"},
		shipyard: &ports.ShipyardData{Symbol: "X1-JP61-A1", ModificationFee: fee},
		agent:    &player.AgentData{Credits: startingCredits},
		installResult: &ports.ModuleModificationResult{
			Fee: fee, CargoCapacity: 320,
			AgentCredits: &postFeeCredits, // in-band post-transaction balance
			Modules:      []ports.ModuleInfo{{Symbol: cargoHold3, Name: "Cargo Hold III", Capacity: 120}},
		},
	}
	handler, db, pid := newOutfitHarness(t, fake)
	seedOutfitShipRow(t, db, pid, "[]", `[{"symbol":"MODULE_CARGO_HOLD_III","name":"Cargo Hold III","description":"x","units":1}]`, 1)

	pidInt := pid
	_, err := handler.Handle(context.Background(), &InstallModuleCommand{ShipSymbol: "SHIP-1", ModuleSymbol: cargoHold3, PlayerID: &pidInt})
	require.NoError(t, err)
	require.Equal(t, 1, fake.installCalls, "precondition: the install actually happened")

	row := latestLedgerRow(t, db, pid)
	require.Equal(t, string(ledger.TransactionTypeModuleInstall), string(row.TransactionType()))
	require.Equal(t, postFeeCredits, row.BalanceAfter(),
		"balance_after must equal the agent's live post-fee credits (re-anchor, not append)")
	require.Equal(t, -fee, row.Amount(), "a shipyard fee is an expense")
	require.Equal(t, startingCredits, row.BalanceBefore(),
		"balance_before = credits - amount; a flipped sign would derive credits + fee here")
	require.Equal(t, string(ledger.CategoryShipInvestments), string(row.Category()))
}

// TestRemoveModuleFeeIsAlsoAnExpense: removal is NOT a refund — the API charges a
// modification fee in the same direction as an install. Recording it as income
// would push the ledger ABOVE real credits, the over-reporting direction.
func TestRemoveModuleFeeIsAlsoAnExpense(t *testing.T) {
	const startingCredits, fee = 800000, 3100
	postFeeCredits := startingCredits - fee

	fake := &outfitFakeAPIClient{
		shipData: &navigation.ShipData{Symbol: "SHIP-1", Location: "X1-JP61-A1", NavStatus: "DOCKED", CargoCapacity: 200, EngineSpeed: 10, FrameSymbol: "FRAME_FRIGATE", Role: "HAULER"},
		shipyard: &ports.ShipyardData{Symbol: "X1-JP61-A1", ModificationFee: fee},
		agent:    &player.AgentData{Credits: startingCredits},
		removeResult: &ports.ModuleModificationResult{
			Fee: fee, CargoCapacity: 200,
			AgentCredits: &postFeeCredits,
			Modules:      []ports.ModuleInfo{},
		},
	}
	handler, db, pid := newOutfitHarness(t, fake)
	seedOutfitShipRow(t, db, pid,
		`[{"symbol":"MODULE_CARGO_HOLD_III","name":"Cargo Hold III","capacity":120}]`,
		`[]`, 0)

	pidInt := pid
	_, err := handler.Handle(context.Background(), &RemoveModuleCommand{ShipSymbol: "SHIP-1", ModuleSymbol: cargoHold3, PlayerID: &pidInt})
	require.NoError(t, err)
	require.Equal(t, 1, fake.removeCalls, "precondition: the remove actually happened")

	row := latestLedgerRow(t, db, pid)
	require.Equal(t, string(ledger.TransactionTypeModuleRemove), string(row.TransactionType()))
	require.Equal(t, -fee, row.Amount(), "a removal fee is charged, not refunded")
	require.Equal(t, postFeeCredits, row.BalanceAfter())
	require.Less(t, row.BalanceAfter(), row.BalanceBefore(), "a module removal must never raise the balance")
}
