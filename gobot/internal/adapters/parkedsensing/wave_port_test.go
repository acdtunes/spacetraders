package parkedsensing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/fleetgrowth"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// --- fakes -------------------------------------------------------------------
//
// Every fail-closed fake returns its error ALONGSIDE the value that would be most DANGEROUS if the
// caller read it anyway: an ENABLED buyer, a large REACHABLE peak and a positive lane count all
// point at HEAVY, so a port that leaks an error does not merely misreport — it pauses probe buying.

type fakeSwitch struct {
	enabled bool
	exists  bool
	err     error
	calls   int
}

func (f *fakeSwitch) GrowthEnabled(_ context.Context, _ int) (bool, bool, error) {
	f.calls++
	if f.err != nil {
		return true, true, f.err
	}
	return f.enabled, f.exists, nil
}

type fakeReserveTarget struct {
	target common.HeavyReserveTarget
	err    error
	calls  int
}

func (f *fakeReserveTarget) Reserve(_ context.Context, _ int) (common.HeavyReserveTarget, error) {
	f.calls++
	if f.err != nil {
		return 1_916_613, f.err
	}
	return f.target, nil
}

type fakeLanes struct {
	count    int
	readable bool
	err      error
	calls    int
}

func (f *fakeLanes) UnservedLaneCount(_ context.Context, _ int) (int, bool, error) {
	f.calls++
	if f.err != nil {
		return 7, true, f.err
	}
	return f.count, f.readable, nil
}

// fakePeak records the WINDOW it was asked for, because the window is half the contract: a peak
// read over the wrong span is a different measurement wearing the right name.
type fakePeak struct {
	peak     int64
	readable bool
	err      error
	since    time.Time
	player   shared.PlayerID
	calls    int
}

func (f *fakePeak) TreasuryHighWaterSince(_ context.Context, playerID shared.PlayerID, since time.Time) (int64, bool, error) {
	f.calls++
	f.since = since
	f.player = playerID
	if f.err != nil {
		return 9_999_999, true, f.err
	}
	return f.peak, f.readable, nil
}

type stoppedClock struct{ now time.Time }

func (c stoppedClock) Now() time.Time        { return c.now }
func (c stoppedClock) Sleep(_ time.Duration) {}

// reachableWorld is a fully-readable HEAVY world: a live buyer, unserved lanes, and a peak well
// past the ask's entry share. Every test below varies ONE fact off it, so a test that passes for
// the wrong reason has nowhere to hide.
func reachableWorld() (*fakeSwitch, *fakeReserveTarget, *fakeLanes, *fakePeak) {
	return &fakeSwitch{enabled: true, exists: true},
		&fakeReserveTarget{target: 1_000_000},
		&fakeLanes{count: 7, readable: true},
		&fakePeak{peak: 4_000_000, readable: true}
}

func waveOf(t *testing.T, sw *fakeSwitch, res *fakeReserveTarget, lanes *fakeLanes, peak *fakePeak) (common.Wave, common.WaveProbeReason, common.HeavyReserveTarget, error) {
	t.Helper()
	return NewWavePort(sw, res, lanes, peak, stoppedClock{time.Unix(1_700_000_000, 0)}).
		Wave(context.Background(), 1)
}

// --- the assembly ------------------------------------------------------------

// The happy path, and the calibration for every negative below it: with every fact readable and a
// reachable ask, the drain's port derives HEAVY.
func TestWavePort_ReadableReachableWorldIsHeavy(t *testing.T) {
	sw, res, lanes, peak := reachableWorld()
	wave, reason, target, err := waveOf(t, sw, res, lanes, peak)

	require.NoError(t, err)
	require.Equal(t, common.WaveHeavy, wave)
	require.Equal(t, common.WaveProbeReasonNone, reason)
	require.Equal(t, common.HeavyReserveTarget(1_000_000), target, "the ask must be carried out for the heartbeat: it is what the pause is FOR")
}

// NO HEAVY BUYER DEPLOYED is quiet and yields PROBE — a probe-only deployment has nothing to save
// for. It short-circuits: the reads below it exist to price a purchase nobody is going to make.
func TestWavePort_NoBuyerIsTheProbeWaveAndReadsNothingElse(t *testing.T) {
	sw, res, lanes, peak := reachableWorld()
	sw.exists = false

	wave, reason, target, err := waveOf(t, sw, res, lanes, peak)

	require.NoError(t, err)
	require.Equal(t, common.WaveProbe, wave)
	require.Equal(t, common.WaveProbeReasonGrowthDisabled, reason)
	require.Zero(t, target)
	require.Zero(t, res.calls+lanes.calls+peak.calls, "no buyer means no question to answer; the remaining reads must not run")
}

// THE MASTER SWITCH IS THE FIRST CLAUSE. A switched-off buyer with a perfectly reachable ask must
// release probe buying, or the drain pauses forever toward a purchase nothing can make.
func TestWavePort_SwitchedOffBuyerIsTheProbeWave(t *testing.T) {
	sw, res, lanes, peak := reachableWorld()
	sw.enabled = false

	wave, reason, _, err := waveOf(t, sw, res, lanes, peak)

	require.NoError(t, err)
	require.Equal(t, common.WaveProbe, wave)
	require.Equal(t, common.WaveProbeReasonGrowthDisabled, reason)
}

// THE PEAK IS READ OVER THE SHARED WINDOW, ending at this tick. A window of a different length is a
// different measurement of the same fleet, and the growth coordinator measures it over exactly this
// one — which is what keeps the two consumers on one answer.
func TestWavePort_PeakIsReadOverTheSharedTradeCycleWindow(t *testing.T) {
	sw, res, lanes, peak := reachableWorld()
	now := time.Unix(1_700_000_000, 0)

	_, _, _, err := NewWavePort(sw, res, lanes, peak, stoppedClock{now}).Wave(context.Background(), 1)

	require.NoError(t, err)
	require.Equal(t, 1, peak.calls)
	require.True(t, peak.since.Equal(now.Add(-fleetgrowth.TradeCycleWindow)),
		"the peak was read from %v, want %v — a shorter window starts oscillating with the trade cycle, which is the flapping the peak measure exists to remove", peak.since, now.Add(-fleetgrowth.TradeCycleWindow))
	require.Equal(t, 1, peak.player.Value(), "the peak must be scoped to the player whose regime is being derived")
}

// AN UNREADABLE WINDOW IS NOT A ZERO PEAK. Zero is the strong claim "this fleet has never held
// money"; unreadable is "we could not see". Both yield PROBE, but only the reason tells them apart,
// and an operator cannot act on the wrong one.
func TestWavePort_UnreadablePeakIsCapacityUnreadableNotUnreachable(t *testing.T) {
	sw, res, lanes, peak := reachableWorld()
	peak.readable = false

	_, reason, _, err := waveOf(t, sw, res, lanes, peak)
	require.NoError(t, err)
	require.Equal(t, common.WaveProbeReasonCapacityUnreadable, reason)

	sw2, res2, lanes2, peak2 := reachableWorld()
	peak2.peak = 0
	_, zeroReason, _, err := waveOf(t, sw2, res2, lanes2, peak2)
	require.NoError(t, err)
	require.Equal(t, common.WaveProbeReasonUnreachable, zeroReason, "a genuine zero peak is a verdict, not a blind read")
}

// An unreadable lane surface releases probe buying rather than guessing, and says which clause did
// it — "the fleet cannot plausibly reach a hull" and "the lane surface is down" call for different
// operator actions.
func TestWavePort_UnreadableLanesAreTheirOwnReason(t *testing.T) {
	sw, res, lanes, peak := reachableWorld()
	lanes.readable = false

	wave, reason, _, err := waveOf(t, sw, res, lanes, peak)

	require.NoError(t, err)
	require.Equal(t, common.WaveProbe, wave)
	require.Equal(t, common.WaveProbeReasonLanesUnreadable, reason)
}

// EVERY READ FAILURE IS AN ERROR, NOT A REGIME. The drain fails the tick closed on it. A soft
// failure here would RELEASE probe spending on a signal nobody could read, which is the blind spend
// RULINGS #4 forbids — and the fakes above hand back HEAVY-pointing values alongside their errors,
// so a leak shows up as a wave rather than as a silence.
func TestWavePort_EveryUnreadableInputErrorsAndPublishesNoRegime(t *testing.T) {
	boom := errors.New("db down")

	t.Run("switch", func(t *testing.T) {
		sw, res, lanes, peak := reachableWorld()
		sw.err = boom
		wave, reason, target, err := waveOf(t, sw, res, lanes, peak)
		require.Error(t, err)
		require.Empty(t, string(wave))
		require.Empty(t, string(reason))
		require.Zero(t, target)
	})
	t.Run("reserve", func(t *testing.T) {
		sw, res, lanes, peak := reachableWorld()
		res.err = boom
		wave, _, target, err := waveOf(t, sw, res, lanes, peak)
		require.Error(t, err)
		require.Empty(t, string(wave))
		require.Zero(t, target)
	})
	t.Run("lanes", func(t *testing.T) {
		sw, res, lanes, peak := reachableWorld()
		lanes.err = boom
		wave, _, _, err := waveOf(t, sw, res, lanes, peak)
		require.Error(t, err)
		require.Empty(t, string(wave))
	})
	t.Run("peak", func(t *testing.T) {
		sw, res, lanes, peak := reachableWorld()
		peak.err = boom
		wave, _, _, err := waveOf(t, sw, res, lanes, peak)
		require.Error(t, err)
		require.Empty(t, string(wave))
	})
}

// AN UNWIRED PORT IS AN ERROR, NOT A PROBE. An unwired reader cannot be told from one whose reads
// all happen to return zero, and a zero peak beside a zero lane count is a perfectly
// plausible-looking PROBE — so the omission would ship silently as a permanent release.
func TestWavePort_UnwiredCollaboratorErrors(t *testing.T) {
	sw, res, lanes, peak := reachableWorld()

	for name, port := range map[string]*WavePort{
		"no switch":  NewWavePort(nil, res, lanes, peak, nil),
		"no reserve": NewWavePort(sw, nil, lanes, peak, nil),
		"no lanes":   NewWavePort(sw, res, nil, peak, nil),
		"no peak":    NewWavePort(sw, res, lanes, nil, nil),
	} {
		wave, _, _, err := port.Wave(context.Background(), 1)
		require.Error(t, err, "%s must fail closed", name)
		require.Empty(t, string(wave), "%s must publish no regime", name)
	}
}

// --- the demonstrated-capacity wiring ----------------------------------------

// THE DEMONSTRATED-CAPACITY PORT IS THE PEAK-OVER-WINDOW READ, AND NOTHING ELSE. Reachability
// judged on a LIVE balance compiles, passes every test of the predicate, and silently restores the
// flapping regime — the property is carried by which port lands in this field. It is asserted on
// the constructed port because a field holding the wrong reader is invisible from every behavioural
// test that supplies its own fake.
func TestWavePort_HighWaterFieldHoldsTheLedgersPeakReader(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	repo := persistence.NewGormTransactionRepository(db)

	sw, res, lanes, _ := reachableWorld()
	port := NewWavePort(sw, res, lanes, repo, nil)

	require.Same(t, repo, port.highWater, "the drain must read the ledger's own peak port, not an adapter over a point read")
	var _ ledger.TreasuryHighWaterReader = port.highWater
}

// A LIVE-TREASURY READER MUST NOT FIT THE DEMONSTRATED-CAPACITY SLOT. The two questions differ —
// "how much may I withhold right now" versus "is this ask reachable by this fleet" — and the type
// system is what keeps the answer to one from being handed to the other.
func TestWavePort_TheDrainsLiveTreasuryReaderDoesNotSatisfyThePeakPort(t *testing.T) {
	var live interface{} = &TreasuryPort{}
	if _, ok := live.(treasuryPeakSource); ok {
		t.Fatal("the LIVE treasury reader satisfies the demonstrated-capacity port — a live balance could be wired into the wave")
	}
}
