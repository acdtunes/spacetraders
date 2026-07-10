package cli

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// These tests pin the "ship outfit" CLI verb family (bead sp-wh0t): the outfit
// group and its install/remove/list children are registered under `ship`, and
// each verb validates its required flags before touching any infrastructure.
// The install/remove/list behavior itself is exercised at the daemon handler
// layer (see the outfitting package tests).

func findOutfitSubcommand(use string) *cobra.Command {
	outfit := findShipSubcommand("outfit")
	if outfit == nil {
		return nil
	}
	for _, c := range outfit.Commands() {
		if c.Use == use {
			return c
		}
	}
	return nil
}

func TestShipOutfitCommandIsRegistered(t *testing.T) {
	require.NotNil(t, findShipSubcommand("outfit"), "ship outfit group should be registered under `ship`")
	require.NotNil(t, findOutfitSubcommand("install"), "ship outfit install should be registered")
	require.NotNil(t, findOutfitSubcommand("remove"), "ship outfit remove should be registered")
	require.NotNil(t, findOutfitSubcommand("list"), "ship outfit list should be registered")
}

func TestShipOutfitInstallRequiresShipFlag(t *testing.T) {
	cmd := newShipOutfitInstallCommand()

	err := cmd.RunE(cmd, nil)

	require.Error(t, err)
	require.Contains(t, err.Error(), "--ship flag is required")
}

func TestShipOutfitInstallRequiresModuleFlag(t *testing.T) {
	cmd := newShipOutfitInstallCommand()
	require.NoError(t, cmd.Flags().Set("ship", "AGENT-1"))

	err := cmd.RunE(cmd, nil)

	require.Error(t, err)
	require.Contains(t, err.Error(), "--module flag is required")
}

func TestShipOutfitRemoveRequiresShipFlag(t *testing.T) {
	cmd := newShipOutfitRemoveCommand()

	err := cmd.RunE(cmd, nil)

	require.Error(t, err)
	require.Contains(t, err.Error(), "--ship flag is required")
}

func TestShipOutfitRemoveRequiresModuleFlag(t *testing.T) {
	cmd := newShipOutfitRemoveCommand()
	require.NoError(t, cmd.Flags().Set("ship", "AGENT-1"))

	err := cmd.RunE(cmd, nil)

	require.Error(t, err)
	require.Contains(t, err.Error(), "--module flag is required")
}

func TestShipOutfitListRequiresShipFlag(t *testing.T) {
	cmd := newShipOutfitListCommand()

	err := cmd.RunE(cmd, nil)

	require.Error(t, err)
	require.Contains(t, err.Error(), "--ship flag is required")
}
