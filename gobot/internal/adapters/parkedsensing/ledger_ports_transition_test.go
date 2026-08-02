package parkedsensing_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	adapterSensing "github.com/andrescamacho/spacetraders-go/internal/adapters/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	appSensing "github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
)

func TestLedgerPort_TransitionSlotCarriesEachFieldToItsOwnArgument(t *testing.T) {
	db := newShipPortsDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	port := adapterSensing.NewLedgerPort(repo)
	ctx := context.Background()

	for _, kind := range []string{appSensing.SlotKindMarket, appSensing.SlotKindSpare} {
		require.NoError(t, repo.UpsertSpareSlot(ctx, persistence.SensingSlotModel{
			PlayerID: testPlayerID, WaypointSymbol: "X1-A-YARD", SystemSymbol: "X1-A",
			SlotKind: kind, State: appSensing.SlotStateParked,
			AssignedShip: strPtr("PROBE-" + kind), WhitelistGoods: "[]",
		}))
	}

	ship, yard := "PROBE-NEW", "X1-A-OTHERYARD"
	require.NoError(t, port.TransitionSlot(ctx, testPlayerID, appSensing.SlotTransition{
		Waypoint: "X1-A-YARD", Kind: appSensing.SlotKindMarket,
		From: appSensing.SlotStateParked, To: appSensing.SlotStateWanted,
	}, appSensing.SlotFields{AssignedShip: &ship, PurchaseYard: &yard}),
		"Waypoint and Kind address the row and From guards it; any pair of them read in the other order finds nothing")

	rows := slotRows(t, db, "X1-A-YARD")
	require.Len(t, rows, 2)
	market, spare := rows[0], rows[1]
	require.Equal(t, appSensing.SlotKindMarket, market.SlotKind)
	require.Equal(t, appSensing.SlotKindSpare, spare.SlotKind)

	require.Equal(t, appSensing.SlotStateWanted, market.State,
		"To is the state written and From is only the guard; swapping them leaves the row where it stood")
	require.Equal(t, ship, *market.AssignedShip)
	require.Equal(t, yard, *market.PurchaseYard,
		"AssignedShip and PurchaseYard land in their own columns")

	require.Equal(t, appSensing.SlotStateParked, spare.State,
		"Kind addresses ONE of the two placements sharing this waypoint; a kind-blind write moves both and the probe cap then reads high")
	require.Equal(t, "PROBE-"+appSensing.SlotKindSpare, *spare.AssignedShip)
}

func TestLedgerPort_TransitionSlotRefusesOnFromAndNamesTheSlotItRefused(t *testing.T) {
	db := newShipPortsDB(t)
	repo := persistence.NewSensingLedgerRepository(db)
	port := adapterSensing.NewLedgerPort(repo)
	ctx := context.Background()

	require.NoError(t, repo.UpsertSpareSlot(ctx, persistence.SensingSlotModel{
		PlayerID: testPlayerID, WaypointSymbol: "X1-A-YARD", SystemSymbol: "X1-A",
		SlotKind: appSensing.SlotKindMarket, State: appSensing.SlotStateParked,
		AssignedShip: strPtr("PROBE-1"), WhitelistGoods: "[]",
	}))

	err := port.TransitionSlot(ctx, testPlayerID, appSensing.SlotTransition{
		Waypoint: "X1-A-YARD", Kind: appSensing.SlotKindMarket,
		From: appSensing.SlotStateQueued, To: appSensing.SlotStateWanted,
	}, appSensing.SlotFields{})
	require.ErrorIs(t, err, appSensing.ErrSlotClaimed,
		"a row that is not in From is another writer's, and the engines read that as routine contention")
	require.Contains(t, err.Error(), "X1-A-YARD (MARKET)",
		"the refusal names the waypoint and then the kind")

	rows := slotRows(t, db, "X1-A-YARD")
	require.Equal(t, appSensing.SlotStateParked, rows[0].State,
		"a refused transition writes nothing")
	require.Equal(t, "PROBE-1", *rows[0].AssignedShip)
}
