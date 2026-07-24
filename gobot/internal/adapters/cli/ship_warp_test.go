package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// `ship warp` is the operator entry point to the off-gate warp executor. It sits
// beside jump/navigate/route under `ship` and, like them, validates its required
// flags before touching any infrastructure. The warp itself is a daemon op
// (RULING #3), so the verb only issues the RPC — the behaviour is exercised at the
// handler layer (warp_ship_test.go).
func TestShipWarpCommandIsRegisteredAlongsideItsSiblings(t *testing.T) {
	for _, verb := range []string{"warp", "jump", "navigate", "route"} {
		require.NotNil(t, findShipSubcommand(verb), "`ship %s` should be registered under `ship`", verb)
	}
}

func TestShipWarpRequiresShipAndDestinationFlags(t *testing.T) {
	cases := []struct {
		name    string
		preset  map[string]string
		wantErr string
	}{
		{"no flags", nil, "--ship flag is required"},
		{"ship only", map[string]string{"ship": "TORWIND-F6"}, "--destination flag is required"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newShipWarpCommand()
			for flag, value := range tc.preset {
				require.NoError(t, cmd.Flags().Set(flag, value))
			}

			err := cmd.RunE(cmd, nil)

			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// The help text must tell an operator that a refusal (no warp drive / would strand)
// is reported verbatim in the container log — otherwise the diagnostic value of the
// typed refusals is invisible from the command line.
func TestShipWarpHelpPointsAtWhereARefusalIsReported(t *testing.T) {
	cmd := findShipSubcommand("warp")
	require.NotNil(t, cmd)

	help := cmd.Long

	require.Contains(t, help, "container logs", "the help names the command that shows a refusal")
	require.True(t, strings.Contains(help, "warp drive") && strings.Contains(help, "strand"),
		"the help names both fail-closed refusals an operator can hit")
}
