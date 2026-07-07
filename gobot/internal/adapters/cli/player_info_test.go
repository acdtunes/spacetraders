package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A realistic (fake) JWT: header.payload.signature. Only the leading prefix is
// safe to surface in logs; the payload and signature must never leak.
const fakeJWT = "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.PAYLOAD.SIGNATURE"

func TestMaskTokenHidesSecretByDefault(t *testing.T) {
	masked := maskToken(fakeJWT, false)

	// Only the 12-char prefix plus an ellipsis is revealed.
	require.Equal(t, "eyJhbGciOiJS...", masked)
	// The sensitive tail never appears.
	require.NotContains(t, masked, "PAYLOAD")
	require.NotContains(t, masked, "SIGNATURE")
	require.False(t, strings.Contains(masked, fakeJWT))
}

func TestMaskTokenRevealsFullTokenWhenRequested(t *testing.T) {
	require.Equal(t, fakeJWT, maskToken(fakeJWT, true))
}

func TestMaskTokenIsPanicSafeForShortOrEmptyTokens(t *testing.T) {
	// Tokens at or below the prefix length must not panic and must not reveal
	// most of a short secret.
	require.Equal(t, "...", maskToken("", false))
	require.Equal(t, "...", maskToken("short", false))
	require.Equal(t, "short", maskToken("short", true))
}

func TestPlayerInfoRegistersShowTokenFlag(t *testing.T) {
	cmd := newPlayerInfoCommand()

	flag := cmd.Flags().Lookup("show-token")
	require.NotNil(t, flag, "player info must register the --show-token flag")
	require.Equal(t, "false", flag.DefValue, "token stays masked unless --show-token is passed")
}
