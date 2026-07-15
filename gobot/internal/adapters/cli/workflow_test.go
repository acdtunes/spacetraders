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

// sp-s1ek: the shipyard-backfill launch verb (the start path for the sp-rhju engine) must be
// registered under `workflow` so an operator can start the sweep of the charted-but-unscanned
// shipyard systems.
func TestWorkflowShipyardBackfillCommandIsRegistered(t *testing.T) {
	require.NotNil(t, findWorkflowSubcommand("shipyard-backfill"),
		"shipyard-backfill subcommand should be registered under `workflow`")
}

// sp-s1ek: the verb exposes exactly the two knobs the engine takes (--tick,
// --max-dispatches) and must NOT expose --dry-run: the sp-rhju engine has no dry-run mode,
// and an operator-facing flag that silently does nothing corrupts mental models (the sp-6fsq
// --iterations lesson).
func TestWorkflowShipyardBackfillFlags(t *testing.T) {
	cmd := newWorkflowShipyardBackfillCommand()

	require.NotNil(t, cmd.Flags().Lookup("tick"),
		"shipyard-backfill must expose --tick (reconcile cadence)")
	require.NotNil(t, cmd.Flags().Lookup("max-dispatches"),
		"shipyard-backfill must expose --max-dispatches (per-cycle rate cap)")
	require.Nil(t, cmd.Flags().Lookup("dry-run"),
		"shipyard-backfill must NOT expose --dry-run: the sp-rhju engine has no dry-run mode")
}
