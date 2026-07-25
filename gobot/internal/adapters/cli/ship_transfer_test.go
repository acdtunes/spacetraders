package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// `ship transfer` is the operator entry point to the ship-to-ship cargo move. It sits
// beside the outfit verbs it completes — remove a module, transfer it, install it on
// the other hull — and, like its siblings, validates its required flags before touching
// any infrastructure. The move itself is a daemon op (RULING #3), so the verb only
// issues the RPC; the behaviour is exercised at the handler layer
// (transfer_cargo_rpc_test.go).
func TestShipTransferCommandIsRegisteredAlongsideItsSiblings(t *testing.T) {
	for _, verb := range []string{"transfer", "outfit", "jump", "warp"} {
		require.NotNil(t, findShipSubcommand(verb), "`ship %s` should be registered under `ship`", verb)
	}
}

func TestShipTransferRequiresBothHullsTheGoodAndAPositiveUnitCount(t *testing.T) {
	cases := []struct {
		name    string
		preset  map[string]string
		wantErr string
	}{
		{"no flags", nil, "--from flag is required"},
		{"from only", map[string]string{"from": "TORWIND-F6"}, "--to flag is required"},
		{"no good", map[string]string{"from": "TORWIND-F6", "to": "TORWIND-2"}, "--good flag is required"},
		{"zero units", map[string]string{"from": "TORWIND-F6", "to": "TORWIND-2", "good": "MODULE_WARP_DRIVE_I", "units": "0"}, "--units must be greater than zero"},
		{"negative units", map[string]string{"from": "TORWIND-F6", "to": "TORWIND-2", "good": "MODULE_WARP_DRIVE_I", "units": "-3"}, "--units must be greater than zero"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newShipTransferCommand()
			for flag, value := range tc.preset {
				require.NoError(t, cmd.Flags().Set(flag, value))
			}

			err := cmd.RunE(cmd, nil)

			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// The two refusals an operator will actually hit are only useful if the help says they
// exist and reports them where the operator can read them — on the command line, since
// a synchronous verb has no container log to point at.
func TestShipTransferHelpNamesBothRefusals(t *testing.T) {
	cmd := findShipSubcommand("transfer")
	require.NotNil(t, cmd)

	help := cmd.Long

	require.True(t, strings.Contains(help, "same waypoint"),
		"the help names the co-location requirement")
	require.True(t, strings.Contains(help, "cargo space") || strings.Contains(help, "room"),
		"the help names the receiver-capacity refusal")
}
