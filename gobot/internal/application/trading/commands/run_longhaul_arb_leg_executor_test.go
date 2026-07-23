package commands

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// The reuse linchpin: a long-haul directed leg maps onto a one-shot arb command whose OWN
// fail-closed guards backstop the money envelope — WorkingCapitalReserve pinned to the 200k
// ContractScalerCushion fence, MaxSpend to the per-haul cap, MaxUnits to the worker's sized
// tranche. So the reused RunArbCoordinatorHandler enforces the cushion + per-haul bounds a
// second time at execution, and never buys more than the worker sized.
func TestLongHaulLegExecutor_MapsEnvelopeBackstopOntoArbCommand(t *testing.T) {
	leg := directedLegCommand{
		ShipSymbol:  "LH-1",
		Good:        "LASER_RIFLES",
		BuyAt:       "X1-ZC66-AX1B",
		SellAt:      "X1-XD86-A1",
		Units:       40,
		PerHaulCap:  1_000_000,
		MinMargin:   250,
		PlayerID:    7,
		ContainerID: "lh-c-1",
	}

	arbCmd := arbCommandForLeg(leg)

	require.Equal(t, "LH-1", arbCmd.ShipSymbol)
	require.Equal(t, "LASER_RIFLES", arbCmd.Good)
	require.Equal(t, "X1-ZC66-AX1B", arbCmd.BuyAt)
	require.Equal(t, "X1-XD86-A1", arbCmd.SellAt)
	require.Equal(t, 40, arbCmd.MaxUnits, "the arb buy never exceeds the worker's sized tranche")
	require.Equal(t, 1_000_000, arbCmd.MaxSpend, "per-haul cap becomes the arb spend cap")
	require.Equal(t, int(common.ContractScalerCushion), arbCmd.WorkingCapitalReserve, "the 200k cushion fence is the arb spend-floor backstop")
	require.Equal(t, 250, arbCmd.MinMargin)
	require.Equal(t, 7, arbCmd.PlayerID)
	require.Equal(t, "lh-c-1", arbCmd.ContainerID)
}
