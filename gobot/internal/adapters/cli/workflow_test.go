package cli

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// These tests pin the `workflow batch-contract` CLI verb after bead sp-6fsq
// retired its dead `--iterations` flag. The flag silently ran exactly one
// contract regardless of N, corrupting operator mental models. Continuous,
// multi-contract operation is served by `contract start` (the contract fleet
// coordinator), so batch-contract must keep validating its required --ship flag
// and must NOT re-expose --iterations.

func findWorkflowSubcommand(use string) *cobra.Command {
	for _, c := range NewWorkflowCommand().Commands() {
		if c.Use == use {
			return c
		}
	}
	return nil
}

func TestWorkflowBatchContractCommandIsRegistered(t *testing.T) {
	require.NotNil(t, findWorkflowSubcommand("batch-contract"),
		"batch-contract subcommand should be registered under `workflow`")
}

func TestWorkflowBatchContractHasNoIterationsFlag(t *testing.T) {
	cmd := newWorkflowBatchContractCommand()

	require.Nil(t, cmd.Flags().Lookup("iterations"),
		"the dead --iterations flag must not be registered on batch-contract (sp-6fsq)")
}

func TestWorkflowBatchContractRequiresShipFlag(t *testing.T) {
	cmd := newWorkflowBatchContractCommand()

	err := cmd.RunE(cmd, nil)

	require.Error(t, err)
	require.Contains(t, err.Error(), "--ship flag is required")
}
