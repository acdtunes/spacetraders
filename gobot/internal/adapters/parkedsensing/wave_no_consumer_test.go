package parkedsensing

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// cappedWorld is the LIVE frame of sp-5iz7a, staging 2026-08-08: a live heavy buyer, 82 lanes still
// unserved, 2,772 units of reachable depth against 1,540 units of trade hold (so NOT saturated),
// and the operator's cap met — which is why the reservation resolves to zero while the cap verdict
// says the cap is what did it.
//
// THE TARGET IS ZERO ON PURPOSE. At the cap the heavy pricing errand stops reading yards, so there
// is no ask to reserve against; the wave therefore publishes reason=unreachable, borrowed from the
// affordability clause (sp-suzfh). This world is what proves the hold reads the CAP as a fact and
// not that label.
func cappedWorld() (*fakeSwitch, *fakeReserveTarget, *fakeLanes, *fakePeak) {
	return &fakeSwitch{enabled: true, exists: true},
		&fakeReserveTarget{target: 0, capBinding: true},
		&fakeLanes{count: 82, readable: true, saturated: false, hold: 1_540},
		&fakePeak{peak: 8_400_000, readable: true}
}

// THE LIVE FRAME. A capped fleet whose reachable surface already holds 80% more work than it can
// lift must refuse further probe spending and name why.
func TestWavePort_CappedFleetIntoAmpleDepthHoldsProbeSpending(t *testing.T) {
	sw, res, lanes, peak := cappedWorld()
	hold, err := holdOf(t, sw, res, lanes, peak)

	require.NoError(t, err)
	require.Equal(t, common.ProbeSpendHoldHeavyCapped, hold,
		"heavy_cap met with 2,772 units of depth on 1,540 of hold: every further probe buys depth no hull will consume")
}

// THE REGIME IS UNTOUCHED, which is half of "do not weaken the wave". The same frame still derives
// exactly the PROBE it derived before this term existed, with the same reason string — including
// the borrowed one, which stays sp-suzfh's to fix.
func TestWavePort_TheHoldDoesNotMoveTheRegime(t *testing.T) {
	sw, res, lanes, peak := cappedWorld()
	wave, reason, target, err := waveOf(t, sw, res, lanes, peak)

	require.NoError(t, err)
	require.Equal(t, common.WaveProbe, wave, "the cap forcing PROBE is documented behaviour and must not change")
	require.Equal(t, common.WaveProbeReasonUnreachable, reason,
		"the wave's own reason is not this bead's to change; a hold that also renamed it would be two fixes wearing one commit")
	require.Zero(t, target)
}

// THE ANTI-VACUITY CONTROL, and the direction that matters more. Depth on an UNCAPPED fleet is the
// ordinary expansion regime — the whole growth leg of the design. Nothing here may hold.
func TestWavePort_UncappedGrowthRegimeHoldsNothing(t *testing.T) {
	sw, res, lanes, peak := cappedWorld()
	// The same surface, but the fleet has headroom and a priced yard: 1,983 units of depth on 1,540
	// of hold with heavies uncapped is exactly the frame the bead names as must-not-regress.
	res.capBinding = false
	res.target = 1_764_591

	hold, err := holdOf(t, sw, res, lanes, peak)
	require.NoError(t, err)
	require.Equal(t, common.ProbeSpendHoldNone, hold, "an uncapped fleet buying depth toward a hull it can still buy is the design working")

	// And the regime it produces is HEAVY, which is what the frame is FOR: this control would be
	// vacuous if the fleet were already in some other pause.
	wave, _, _, err := waveOf(t, sw, res, lanes, peak)
	require.NoError(t, err)
	require.Equal(t, common.WaveHeavy, wave)
}

// EVERY FACT THE PORT COULD NOT READ RELEASES. The hold requires positive evidence, so a blind or
// absent input leaves today's behaviour standing rather than inventing a refusal from an absence.
func TestWavePort_BlindOrAbsentInputsHoldNothing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		break_ func(*fakeSwitch, *fakeReserveTarget, *fakeLanes, *fakePeak)
	}{
		{"no heavy buyer deployed at all — a probe-only deployment", func(sw *fakeSwitch, _ *fakeReserveTarget, _ *fakeLanes, _ *fakePeak) { sw.exists = false }},
		{"the buyer's master switch is off", func(sw *fakeSwitch, _ *fakeReserveTarget, _ *fakeLanes, _ *fakePeak) { sw.enabled = false }},
		{"the lane surface could not be read", func(_ *fakeSwitch, _ *fakeReserveTarget, l *fakeLanes, _ *fakePeak) { l.readable = false }},
		{"no lane is unserved", func(_ *fakeSwitch, _ *fakeReserveTarget, l *fakeLanes, _ *fakePeak) { l.count = 0 }},
		{"no trade pool stands, so there is no hold for depth to exceed", func(_ *fakeSwitch, _ *fakeReserveTarget, l *fakeLanes, _ *fakePeak) { l.hold = 0 }},
		{"the surface is saturated — probing is the right answer", func(_ *fakeSwitch, _ *fakeReserveTarget, l *fakeLanes, _ *fakePeak) { l.saturated = true }},
		{"the cap is not what bars the heavy", func(_ *fakeSwitch, r *fakeReserveTarget, _ *fakeLanes, _ *fakePeak) { r.capBinding = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sw, res, lanes, peak := cappedWorld()
			tc.break_(sw, res, lanes, peak)

			hold, err := holdOf(t, sw, res, lanes, peak)
			require.NoError(t, err)
			require.Equal(t, common.ProbeSpendHoldNone, hold, "%s: a hold needs positive evidence, never an absence", tc.name)
		})
	}
}

// THE DEMONSTRATED-CAPACITY READ IS DELIBERATELY NOT AN INPUT, and this is the case that says so
// out loud rather than leaving it to the absence of a line.
//
// The peak answers AFFORDABILITY — can this fleet plausibly reach an ask — and affordability is
// moot once the cap is what forbids the purchase. Releasing the hold on an unreadable peak would
// resume the exact spending this term exists to stop, on a blind read of a quantity that has no
// bearing on it. Both facts the hold DOES rest on are still positively read here.
func TestWavePort_AnUnreadablePeakIsNotAReasonToResumeSpending(t *testing.T) {
	sw, res, lanes, peak := cappedWorld()
	peak.readable = false

	hold, err := holdOf(t, sw, res, lanes, peak)
	require.NoError(t, err)
	require.Equal(t, common.ProbeSpendHoldHeavyCapped, hold,
		"the cap and the depth were both read; what the treasury once peaked at cannot make a capped fleet able to lift the surface")

	// The regime moves — that is the wave's own blind-read rule — and the hold does not. Two
	// predicates over one set of facts, answering two questions.
	_, reason, _, err := waveOf(t, sw, res, lanes, peak)
	require.NoError(t, err)
	require.Equal(t, common.WaveProbeReasonCapacityUnreadable, reason)
}

// A PORT THAT COULD NOT DERIVE A REGIME REPORTS NO HOLD EITHER. The drain fails the tick closed on
// the error, and a hold carried out beside it would be a verdict from a tick that never completed.
// Every fake is ADVERSARIAL here: each errors while returning the values that would MAKE a hold.
func TestWavePort_AnErroringReadCarriesNoHold(t *testing.T) {
	boom := errors.New("database unreachable")
	for _, tc := range []struct {
		name string
		fail func(*fakeSwitch, *fakeReserveTarget, *fakeLanes, *fakePeak)
	}{
		{"the master switch read", func(sw *fakeSwitch, _ *fakeReserveTarget, _ *fakeLanes, _ *fakePeak) { sw.err = boom }},
		{"the reservation read", func(_ *fakeSwitch, r *fakeReserveTarget, _ *fakeLanes, _ *fakePeak) { r.err = boom }},
		{"the lane surface read", func(_ *fakeSwitch, _ *fakeReserveTarget, l *fakeLanes, _ *fakePeak) { l.err = boom }},
		{"the demonstrated-capacity read", func(_ *fakeSwitch, _ *fakeReserveTarget, _ *fakeLanes, p *fakePeak) { p.err = boom }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sw, res, lanes, peak := cappedWorld()
			tc.fail(sw, res, lanes, peak)

			hold, err := holdOf(t, sw, res, lanes, peak)
			require.Error(t, err, "%s must fail the tick, not guess", tc.name)
			require.Equal(t, common.ProbeSpendHoldNone, hold, "%s: no regime was derived, so no refusal was either", tc.name)
		})
	}
}

// AN UNWIRED PORT HOLDS NOTHING, matching the regime it also refuses to invent. A wiring omission
// must leave probe buying exactly as it is today.
func TestWavePort_UnwiredPortHoldsNothing(t *testing.T) {
	_, _, _, hold, err := NewWavePort(nil, nil, nil, nil, nil).Wave(context.Background(), 1)

	require.Error(t, err)
	require.Equal(t, common.ProbeSpendHoldNone, hold)
}
