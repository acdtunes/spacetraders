package cli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests pin the "ship jettison" CLI verb (bead sp-psfc): a container-based
// mirror of "ship dock"/"ship refuel" (CLI -> daemon gRPC -> JettisonCargoHandler
// -> SpaceTraders API), used to discard stranded/bait cargo that no reachable
// market buys. It validates its required flags before touching any
// infrastructure. The jettison behavior itself is exercised at the handler
// layer (see jettison_cargo_test.go in the cargo package).

func TestShipJettisonCommandIsRegistered(t *testing.T) {
	require.NotNil(t, findShipSubcommand("jettison"), "ship jettison subcommand should be registered under `ship`")
}

func TestShipJettisonRequiresShipFlag(t *testing.T) {
	cmd := newShipJettisonCommand()

	err := cmd.RunE(cmd, nil)

	require.Error(t, err)
	require.Contains(t, err.Error(), "--ship flag is required")
}

func TestShipJettisonRequiresGoodFlag(t *testing.T) {
	cmd := newShipJettisonCommand()
	require.NoError(t, cmd.Flags().Set("ship", "AGENT-1"))

	err := cmd.RunE(cmd, nil)

	require.Error(t, err)
	require.Contains(t, err.Error(), "--good flag is required")
}

func TestShipJettisonRequiresPositiveUnits(t *testing.T) {
	cmd := newShipJettisonCommand()
	require.NoError(t, cmd.Flags().Set("ship", "AGENT-1"))
	require.NoError(t, cmd.Flags().Set("good", "IRON_ORE"))

	err := cmd.RunE(cmd, nil)

	require.Error(t, err)
	require.Contains(t, err.Error(), "--units must be greater than 0")
}
