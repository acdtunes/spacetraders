package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestContainerListHeaderIncludesPlayerColumn(t *testing.T) {
	header := formatContainerListHeader()

	require.Contains(t, header, "PLAYER",
		"container list header must show a PLAYER column so era-transition orphans are distinguishable at a glance")
}

func TestContainerRowShowsPlayerID(t *testing.T) {
	c := &ContainerInfo{
		ContainerID:      "navigate-SCOUT-1-1111111111",
		ContainerType:    "navigate",
		Status:           "RUNNING",
		PlayerID:         3,
		CreatedAt:        "2024-01-01T00:00:00Z",
		CurrentIteration: 1,
		MaxIterations:    10,
	}

	row := formatContainerRow(c)
	fields := strings.Fields(row)

	require.Equal(t, "3", fields[1],
		"the column following CONTAINER ID must render the container's PlayerID")
}

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
