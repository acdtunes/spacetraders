package cli

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// These tests pin the `workflow batch-contract` CLI verb: it must keep validating
// its required --ship flag and must NOT re-expose --iterations (continuous,
// multi-contract operation is served by `contract start`, the contract fleet
// coordinator).

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

// sp-ehg9: batch-contract gains a --loop flag that turns the single-shot worker
// into a CONTINUOUS single-hull contract loop (re-negotiate + run until stopped)
// for the bootstrap command frigate. Unlike the retired --iterations (a silent
// no-op, sp-6fsq), --loop actually loops. It is a bool and defaults false so the
// plain `batch-contract --ship X` verb stays byte-identical single-shot.
func TestWorkflowBatchContractHasLoopFlag(t *testing.T) {
	cmd := newWorkflowBatchContractCommand()

	flag := cmd.Flags().Lookup("loop")
	require.NotNil(t, flag, "batch-contract must expose --loop (continuous single-hull contract loop, sp-ehg9)")
	require.Equal(t, "false", flag.DefValue,
		"--loop must default false so `batch-contract --ship X` stays byte-identical single-shot")
}

func TestWorkflowBatchContractRequiresShipFlag(t *testing.T) {
	cmd := newWorkflowBatchContractCommand()

	err := cmd.RunE(cmd, nil)

	require.Error(t, err)
	require.Contains(t, err.Error(), "--ship flag is required")
}

// The shipyard-backfill launch verb (the start path for the shipyard-backfill engine) must be
// registered under `workflow` so an operator can start the sweep of the charted-but-unscanned
// shipyard systems.
func TestWorkflowShipyardBackfillCommandIsRegistered(t *testing.T) {
	require.NotNil(t, findWorkflowSubcommand("shipyard-backfill"),
		"shipyard-backfill subcommand should be registered under `workflow`")
}

// The verb exposes exactly the two knobs the engine takes (--tick,
// --max-dispatches) and must NOT expose --dry-run: the engine has no dry-run mode,
// and an operator-facing flag that silently does nothing corrupts mental models.
func TestWorkflowShipyardBackfillFlags(t *testing.T) {
	cmd := newWorkflowShipyardBackfillCommand()

	require.NotNil(t, cmd.Flags().Lookup("tick"),
		"shipyard-backfill must expose --tick (reconcile cadence)")
	require.NotNil(t, cmd.Flags().Lookup("max-dispatches"),
		"shipyard-backfill must expose --max-dispatches (per-cycle rate cap)")
	require.Nil(t, cmd.Flags().Lookup("dry-run"),
		"shipyard-backfill must NOT expose --dry-run: the sp-rhju engine has no dry-run mode")
}
