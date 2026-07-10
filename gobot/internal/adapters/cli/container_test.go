package cli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestContainerLogsRegistersTailFlag(t *testing.T) {
	cmd := newContainerLogsCommand()

	flag := cmd.Flags().Lookup("tail")
	require.NotNil(t, flag, "container logs must register the --tail flag")
	require.Equal(t, "0", flag.DefValue, "--tail is unset (0) by default, falling back to --limit")
}

func TestContainerLogsLimitFlagUnchanged(t *testing.T) {
	cmd := newContainerLogsCommand()

	flag := cmd.Flags().Lookup("limit")
	require.NotNil(t, flag, "container logs must still register the --limit flag")
	require.Equal(t, "100", flag.DefValue, "--limit default must stay 100 (regression)")
}

func TestEffectiveLogLimitPrefersTailWhenSet(t *testing.T) {
	cmd := newContainerLogsCommand()
	require.NoError(t, cmd.Flags().Set("limit", "100"))
	require.NoError(t, cmd.Flags().Set("tail", "50"))

	require.Equal(t, 50, effectiveLogLimit(cmd, 100, 50),
		"--tail must win when both --tail and --limit are explicitly set")
}

func TestEffectiveLogLimitFallsBackToLimitWhenTailUnset(t *testing.T) {
	cmd := newContainerLogsCommand()
	require.NoError(t, cmd.Flags().Set("limit", "75"))
	// --tail intentionally left untouched (not Changed).

	require.Equal(t, 75, effectiveLogLimit(cmd, 75, 0),
		"--limit semantics must be unchanged when --tail is not passed")
}
