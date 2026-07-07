package cli

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// These tests pin the manual "ship buy" CLI verb (bead sp-71bj), a mirror of the
// "ship sell" verb: it is registered under `ship` and validates its required
// flags before touching any infrastructure. The purchase behavior itself is
// exercised at the handler layer (see purchase_cargo_test.go in the cargo package).

func findShipSubcommand(use string) *cobra.Command {
	for _, c := range NewShipCommand().Commands() {
		if c.Use == use {
			return c
		}
	}
	return nil
}

func TestShipBuyCommandIsRegistered(t *testing.T) {
	require.NotNil(t, findShipSubcommand("buy"), "ship buy subcommand should be registered under `ship`")
}

func TestShipBuyRequiresShipFlag(t *testing.T) {
	cmd := newShipBuyCommand()

	err := cmd.RunE(cmd, nil)

	require.Error(t, err)
	require.Contains(t, err.Error(), "--ship flag is required")
}

func TestShipBuyRequiresGoodFlag(t *testing.T) {
	cmd := newShipBuyCommand()
	require.NoError(t, cmd.Flags().Set("ship", "AGENT-1"))

	err := cmd.RunE(cmd, nil)

	require.Error(t, err)
	require.Contains(t, err.Error(), "--good flag is required")
}

func TestShipBuyRequiresPositiveUnits(t *testing.T) {
	cmd := newShipBuyCommand()
	require.NoError(t, cmd.Flags().Set("ship", "AGENT-1"))
	require.NoError(t, cmd.Flags().Set("good", "IRON_ORE"))

	err := cmd.RunE(cmd, nil)

	require.Error(t, err)
	require.Contains(t, err.Error(), "--units must be greater than 0")
}
