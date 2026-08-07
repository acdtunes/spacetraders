package parkedsensing

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/fleetgrowth"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// WavePort is the drain's read of the regime, and the SECOND of the predicate's two consumers.
//
// It assembles common.DeriveWave's inputs and calls it once. It never re-derives any of them: the
// heavy target comes from the same reservation the coordinator computes, the lane count from the
// same shared reader instance, and the cap and the master switch from the heavy buyer's own
// container row. A second assembly of these facts is how two consumers of one pure function still
// end up disagreeing, which is the whole failure this design exists to prevent.
//
// EVERY READ BEHIND IT IS LOCAL. That is not incidental — it is what lets the drain read the wave
// AHEAD of its cheapest-first gate order, preserving the property that a tick with nothing to buy
// costs no API call. The demonstrated-capacity read is a ledger query over a trailing window, not a
// live balance; a regime derived from a live balance would have put an API call on every sensing
// tick and made the regime a function of where in a trade cycle the tick landed.
type WavePort struct {
	switches  growthSwitchSource
	reserve   heavyReserveSource
	lanes     UnservedLaneCounter
	highWater treasuryPeakSource
	clock     shared.Clock
}

// growthSwitchSource reports the heavy buyer's master switch and whether a buyer exists at all.
// Satisfied by *HeavyBuyerCapPort, which resolves it off the same row as the cap.
type growthSwitchSource interface {
	GrowthEnabled(ctx context.Context, playerID int) (enabled bool, buyerExists bool, err error)
}

// heavyReserveSource reports the ask the fleet is saving toward. Satisfied by *HeavyReservePort —
// the SAME reservation, over the same shared heavy-target finder, that the coordinator sizes its
// buy against.
type heavyReserveSource interface {
	Reserve(ctx context.Context, playerID int) (common.HeavyReserveTarget, error)
}

// UnservedLaneCounter reports the capacity-short signal. Satisfied by the ONE
// queries.UnservedLaneReader instance the growth coordinator also holds: two readers of one
// quantity is how the spender and the withholder end up disagreeing about whether the fleet is
// capacity-short.
//
// EXPORTED so the composition root can hold one as a genuinely nil interface. A typed-nil pointer
// assigned straight into this field is NOT nil to the port's own fail-closed guard, and would panic
// mid-tick instead of refusing the tick.
type UnservedLaneCounter interface {
	UnservedLaneCount(ctx context.Context, playerID int) (int, bool, error)
}

// treasuryPeakSource is the DEMONSTRATED-CAPACITY read: the PEAK balance held across a trailing
// trade cycle, never a point read of the live balance.
//
// The distinction is carried by the type and by nothing else, which is why the port is named here
// rather than taken as a bare function: substituting a live-balance reader compiles, passes every
// test of the predicate, and silently restores the flapping regime the peak measure exists to
// remove. Satisfied by ledger.TreasuryHighWaterReader.
type treasuryPeakSource interface {
	TreasuryHighWaterSince(ctx context.Context, playerID shared.PlayerID, since time.Time) (int64, bool, error)
}

// NewWavePort wires the regime's four reads. A nil clock takes the real one.
func NewWavePort(
	switches growthSwitchSource,
	reserve heavyReserveSource,
	lanes UnservedLaneCounter,
	highWater treasuryPeakSource,
	clock shared.Clock,
) *WavePort {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &WavePort{switches: switches, reserve: reserve, lanes: lanes, highWater: highWater, clock: clock}
}

// Wave derives the regime from the same facts the growth coordinator reads, through the same shared
// instances.
//
// EVERY FAILURE IS AN ERROR, NOT A GUESS, and the drain fails the tick CLOSED on it. That is the
// one place this port deliberately differs from the coordinator, which fails soft to PROBE: the
// coordinator's soft failure withholds nothing and buys nothing, while a soft failure here would
// RELEASE probe spending on a signal nobody could read (RULINGS #4). The two still never disagree
// about a regime — on these paths this port reports no regime at all.
//
// A missing wiring is one of those failures. An unwired port cannot be told from one whose reads
// all happen to return zero, and a zero high-water beside a zero lane count is a perfectly
// plausible-looking PROBE.
func (p *WavePort) Wave(ctx context.Context, playerID int) (common.Wave, common.WaveProbeReason, common.HeavyReserveTarget, error) {
	if p.switches == nil || p.reserve == nil || p.lanes == nil || p.highWater == nil {
		return "", "", 0, fmt.Errorf("sensing wave port is not fully wired, so no regime can be derived")
	}

	enabled, buyerExists, err := p.switches.GrowthEnabled(ctx, playerID)
	if err != nil {
		return "", "", 0, fmt.Errorf("heavy buyer switch unreadable: %w", err)
	}
	// No heavy buyer deployed at all is a quiet, expected configuration — a probe-only deployment —
	// and it means there is nothing to save for. It is the same rung the cap read already treats as
	// silent, applied to the container instead of the knob.
	if !buyerExists {
		return common.WaveProbe, common.WaveProbeReasonGrowthDisabled, 0, nil
	}

	target, err := p.reserve.Reserve(ctx, playerID)
	if err != nil {
		return "", "", 0, fmt.Errorf("heavy reserve target unreadable: %w", err)
	}

	lanes, lanesOK, err := p.lanes.UnservedLaneCount(ctx, playerID)
	if err != nil {
		return "", "", 0, fmt.Errorf("unserved lane count unreadable: %w", err)
	}

	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return "", "", 0, fmt.Errorf("wave read needs a valid player: %w", err)
	}
	// Demonstrated capacity, over the SAME window the growth coordinator measures. The two ports
	// read one ledger with one window, which is what keeps the two consumers on one answer.
	highWater, highWaterOK, err := p.highWater.TreasuryHighWaterSince(ctx, pid, p.clock.Now().Add(-fleetgrowth.TradeCycleWindow))
	if err != nil {
		return "", "", 0, fmt.Errorf("demonstrated capacity unreadable: %w", err)
	}

	wave, reason := common.DeriveWave(common.WaveInputs{
		GrowthEnabled:         enabled,
		UnservedLanes:         lanes,
		UnservedLanesReadable: lanesOK,
		Target:                target,
		HighWaterTreasury:     highWater,
		HighWaterReadable:     highWaterOK,
	})
	return wave, reason, target, nil
}
