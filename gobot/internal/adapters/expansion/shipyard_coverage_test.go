package expansion

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	expansionCmd "github.com/andrescamacho/spacetraders-go/internal/application/expansion/commands"
)

// fakeCandidateLister serves the gate-reachable frontier candidates (with their Scanned
// flags) the coverage reader derives shipyard-scan-exhaustion from. It faithfully honors the
// caller's maxHops the way the real ExpansionScanner's bfsHops does — a candidate deeper than
// the bound is NOT returned — so a test can prove the reach the caller passes actually gates
// enumeration. It also records the last maxHops it was asked for.
type fakeCandidateLister struct {
	candidates []expansionCmd.ExpansionCandidate
	err        error
	gotMaxHops int
}

func (f *fakeCandidateLister) ExpansionCandidates(_ context.Context, _ int, maxHops int) ([]expansionCmd.ExpansionCandidate, error) {
	f.gotMaxHops = maxHops
	if f.err != nil {
		return nil, f.err
	}
	within := make([]expansionCmd.ExpansionCandidate, 0, len(f.candidates))
	for _, c := range f.candidates {
		if c.Hops <= maxHops {
			within = append(within, c)
		}
	}
	return within, nil
}

// TestGateShipyardCoverage_ExhaustedOnlyWhenEveryReachableSystemSwept pins the
// trigger-(b) guard: gate shipyard coverage is scan-exhausted (a missing heavy yard is
// CONCLUSIVE) only when EVERY gate-reachable system has been swept — an unscanned reachable
// system means coverage is still sparse (a heavy yard might yet be found on-gate), and an
// empty reachable set is cold-start, also sparse.
func TestGateShipyardCoverage_ExhaustedOnlyWhenEveryReachableSystemSwept(t *testing.T) {
	cases := []struct {
		name       string
		candidates []expansionCmd.ExpansionCandidate
		exhausted  bool
	}{
		{
			name:       "every reachable system swept → exhausted",
			candidates: []expansionCmd.ExpansionCandidate{{SystemSymbol: "A", Scanned: true}, {SystemSymbol: "B", Scanned: true}},
			exhausted:  true,
		},
		{
			name:       "an unscanned reachable system → still sparse",
			candidates: []expansionCmd.ExpansionCandidate{{SystemSymbol: "A", Scanned: true}, {SystemSymbol: "B", Scanned: false}},
			exhausted:  false,
		},
		{
			name:       "no reachable systems (cold start) → sparse",
			candidates: nil,
			exhausted:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader := NewGateShipyardCoverageReader(&fakeCandidateLister{candidates: tc.candidates}, 8)
			exhausted, readable, err := reader.GateShipyardsScanExhausted(context.Background(), 1)
			require.NoError(t, err)
			require.True(t, readable, "a successful scan is readable")
			require.Equal(t, tc.exhausted, exhausted)
		})
	}
}

// TestGateShipyardCoverage_UnreadableWhenScanFails pins the fail-safe: a scanner error makes
// the signal unreadable, so the demand evaluator treats coverage as sparse and does NOT fire
// trigger (b) on a transient read failure.
func TestGateShipyardCoverage_UnreadableWhenScanFails(t *testing.T) {
	reader := NewGateShipyardCoverageReader(&fakeCandidateLister{err: errors.New("adjacency unreadable")}, 8)
	exhausted, readable, err := reader.GateShipyardsScanExhausted(context.Background(), 1)
	require.NoError(t, err, "the reader fails SAFE, not loud")
	require.False(t, readable, "an unreadable scan is not readable")
	require.False(t, exhausted, "unreadable → treated as not exhausted (sparse)")
}
