package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// fakeSensingPhase is the EXPANSION gate's driven port. Adversarial: on err it
// still reports IN EXPANSION alongside the error, so a gate that reads the bool
// and swallows the error runs the whole tick — and the zero-action asserts below
// catch it instead of a convenient false masking the bug.
type fakeSensingPhase struct {
	inExpansion bool
	err         error
	calls       int
}

func (f *fakeSensingPhase) InExpansion(_ context.Context, _ shared.PlayerID) (bool, error) {
	f.calls++
	return f.inExpansion, f.err
}

// Pre-EXPANSION the world is still building its jump gate: bootstrap owns probe
// provisioning and the scout-post coordinator mans what it bought. Demand-driven
// sensing has no trading footprint to size against yet, so the tick does NOTHING
// — no census read, no post write, no fleet read, no buy.
func TestSensing_PreExpansion_TakesNoAction(t *testing.T) {
	dr := &fakeDepthReader{rows: richRows("X1-AA1", 20)}
	pr := newSensingPostRepo(sensingPost("X1-BB2", 1))
	fl := calmFleet(t)
	phase := &fakeSensingPhase{inExpansion: false}
	h, buyer := newSensingHandlerInPhase(dr, pr, fl, &fakePressure{}, phase)

	require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))

	require.Equal(t, 1, phase.calls, "the phase is read once per tick")
	require.Zero(t, dr.calls, "no census read — the gate is checked before any work")
	require.Zero(t, fl.calls, "no fleet read")
	require.Empty(t, pr.upserts, "no post declared")
	require.Empty(t, pr.stateWrites, "no post resized, woken, or re-stamped")
	require.Empty(t, pr.removed, "no post removed")
	require.Empty(t, buyer.calls, "no probe buy is even attempted")
}

// A phase that cannot be verified is not a licence to run: an unreadable world
// and an unwired reader both hold sensing inert, exactly as the probe buyer's
// gate does.
func TestSensing_PhaseUnverifiable_FailsClosed(t *testing.T) {
	cases := []struct {
		name  string
		phase expansionPhaseReader
	}{
		{"phase read fails", &fakeSensingPhase{inExpansion: true, err: errors.New("construction site unreachable")}},
		{"no reader wired", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dr := &fakeDepthReader{rows: richRows("X1-AA1", 20)}
			pr := newSensingPostRepo(sensingPost("X1-BB2", 1))
			fl := calmFleet(t)
			h, buyer := newSensingHandlerInPhase(dr, pr, fl, &fakePressure{}, tc.phase)

			require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))

			require.Zero(t, dr.calls, "no census read on an unverifiable phase")
			require.Zero(t, fl.calls, "no fleet read on an unverifiable phase")
			require.Empty(t, pr.upserts)
			require.Empty(t, pr.stateWrites)
			require.Empty(t, pr.removed)
			require.Empty(t, buyer.calls, "an unverifiable phase never reaches the buy path")
		})
	}
}
