package cli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The verb must be registered under `ship` and must refuse a bare invocation
// before touching any infrastructure. The scrap behavior itself is exercised at
// the daemon handler layer (see the scrap package tests).
func TestShipScrapCommandIsRegistered(t *testing.T) {
	require.NotNil(t, findShipSubcommand("scrap"), "ship scrap should be registered under `ship`")
}

func TestShipScrapRequiresShipFlag(t *testing.T) {
	cmd := newShipScrapCommand()

	err := cmd.RunE(cmd, nil)

	require.Error(t, err)
	require.Contains(t, err.Error(), "--ship flag is required")
}

// Scrapping is irreversible, so the verb must not fire on a bare --ship. An
// operator has to say so explicitly.
func TestShipScrapRequiresExplicitConfirmation(t *testing.T) {
	cmd := newShipScrapCommand()
	require.NoError(t, cmd.Flags().Set("ship", "TORWIND-446"))

	err := cmd.RunE(cmd, nil)

	require.Error(t, err)
	require.Contains(t, err.Error(), "--yes")
}
