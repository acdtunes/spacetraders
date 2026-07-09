package cli

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/stretchr/testify/require"
)

// These tests pin the `construction start --min-supply` flag (bead sp-ezz9):
// it lowers the sourcing locator's EXPORT acceptance floor below the default
// MODERATE baseline, but only for values that are real manufacturing.SupplyLevel
// states. Invalid values must be rejected with a clear error before any
// infrastructure (player resolution, daemon connection) is touched - mirroring
// newShipBuyCommand's flag-validation-first pattern (see ship_buy_test.go).
//
// The valid-input cases are asserted directly against parseMinSupplyFlag
// rather than through cmd.RunE: RunE's happy path falls through to
// connectDaemon(), which dials a real daemon and blocks for several seconds
// per call with none running - unacceptably slow for a unit test suite.
// parseMinSupplyFlag is the actual validation contract being added; calling
// it directly keeps these tests instant and deterministic.

func TestConstructionStartRejectsInvalidMinSupply(t *testing.T) {
	cmd := newConstructionStartCommand()
	require.NoError(t, cmd.Flags().Set("min-supply", "NOT_A_REAL_LEVEL"))

	err := cmd.RunE(cmd, []string{"X1-TEST-A1"})

	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid --min-supply value")
	require.Contains(t, err.Error(), "NOT_A_REAL_LEVEL")
}

func TestParseMinSupplyFlag_UnsetIsValid(t *testing.T) {
	level, err := parseMinSupplyFlag("")

	require.NoError(t, err)
	require.Equal(t, manufacturing.SupplyLevel(""), level,
		"unset must preserve the zero value, keeping the default MODERATE floor unchanged")
}

func TestParseMinSupplyFlag_AcceptsEachRealSupplyLevel(t *testing.T) {
	for _, level := range []manufacturing.SupplyLevel{
		manufacturing.SupplyLevelAbundant,
		manufacturing.SupplyLevelHigh,
		manufacturing.SupplyLevelModerate,
		manufacturing.SupplyLevelLimited,
		manufacturing.SupplyLevelScarce,
	} {
		t.Run(string(level), func(t *testing.T) {
			got, err := parseMinSupplyFlag(string(level))

			require.NoError(t, err)
			require.Equal(t, level, got)
		})
	}
}

func TestParseMinSupplyFlag_RejectsUnknownValue(t *testing.T) {
	_, err := parseMinSupplyFlag("NOT_A_REAL_LEVEL")

	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid --min-supply value")
	require.Contains(t, err.Error(), "NOT_A_REAL_LEVEL")
}

// TestConstructionStopIsRegisteredWithExactlyOneArg pins the wiring for the
// `construction stop <site>` verb (bead sp-yzrv): the construction command
// surface was start/status only, so a misplanned pipeline had no way to be
// stopped. This guards the registration itself - unlike `start`, `stop` has
// no flags/pre-infrastructure validation logic of its own to unit test, so
// there is nothing else here worth asserting without invoking RunE's happy
// path, which dials a real daemon via connectDaemon() (see file comment above).
func TestConstructionStopIsRegisteredWithExactlyOneArg(t *testing.T) {
	root := NewConstructionCommand()

	stop, _, err := root.Find([]string{"stop"})
	require.NoError(t, err)
	require.Equal(t, "stop <construction-site>", stop.Use)

	require.NoError(t, stop.Args(stop, []string{"X1-TEST-A1"}))
	require.Error(t, stop.Args(stop, []string{}))
	require.Error(t, stop.Args(stop, []string{"X1-TEST-A1", "extra"}))
}
