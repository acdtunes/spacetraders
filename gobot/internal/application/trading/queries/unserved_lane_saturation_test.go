package queries

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/fleetgrowth"
)

// movingClock is the saturation dwell's only dependency on time. The window is WALL-CLOCK rather
// than a count of ticks because the two wave consumers poll at different rates, so a test that
// could not advance time could not witness the window at all.
type movingClock struct{ at time.Time }

func (c *movingClock) Now() time.Time          { return c.at }
func (c *movingClock) Sleep(d time.Duration)   { c.at = c.at.Add(d) }
func (c *movingClock) advance(d time.Duration) { c.at = c.at.Add(d) }

func newMovingClock() *movingClock { return &movingClock{at: time.Unix(1_700_000_000, 0).UTC()} }

// saturationRig builds the reader over a fake fleet and a fake census with the dwell wired to a
// clock the test drives. The margin is left on the resolved default unless a case names one.
func saturationRig(t *testing.T, census LaneCensus, hulls ...[]hullSpec) (*UnservedLaneReader, *fakeLaneCounter, *movingClock) {
	t.Helper()
	lanes := &fakeLaneCounter{census: census, readable: true}
	clock := newMovingClock()
	r := NewUnservedLaneReader(fakeShipRepoWith(t, hulls...), lanes)
	r.SetClock(clock)
	return r, lanes, clock
}

// holdingHulls places n trade-dedicated hulls each carrying `hold` units of cargo capacity — the
// fleet-view side of the saturation comparison.
func holdingHulls(n, hold int) []hullSpec {
	out := make([]hullSpec, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, hullSpec{fleet: tradeFleetTag, system: "X1-AA", hold: hold})
	}
	return out
}

// THE PIVOTAL FIXTURE — the live frame of 2026-08-08 01:51, reassembled from the census terms the
// daemon actually published: 24 profitable lanes absorbing 741 units in one trip, a trade pool of
// nine hulls holding ~820 units between them, every one of them empty. The lane count says fifteen
// hulls short; the DEPTH says the fleet already outgrew the surface. The reader must report both,
// and the saturation verdict is the one the wave switches back on.
func TestLaneDemand_TheLiveFrameReadsSaturatedWithUnservedLanesRemaining(t *testing.T) {
	r, _, _ := saturationRig(t, LaneCensus{Profitable: 24, AbsorbableUnits: 741}, holdingHulls(9, 91))

	got, readable, err := r.LaneDemand(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, readable)

	require.Equal(t, 15, got.UnservedLanes, "the lane count is unchanged — the term is added beside it, not instead of it")
	require.Equal(t, 741, got.AbsorbableUnits)
	require.Equal(t, 819, got.TradeHoldCapacity, "nine hulls of 91 units is the trade pool's hold")
	require.True(t, got.Saturated, "741 units of reachable depth against 819 units of hold is a saturated surface")
}

// THE ANTI-VACUITY CONTROL. A surface deeper than the fleet's hold is genuinely capacity-short, and
// the reader must report exactly what it reported before this term existed — that state is the only
// one in which a heavy is bought at all.
func TestLaneDemand_ADeeperSurfaceIsNotSaturated(t *testing.T) {
	r, _, _ := saturationRig(t, LaneCensus{Profitable: 24, AbsorbableUnits: 900}, holdingHulls(9, 91))

	got, readable, err := r.LaneDemand(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, readable)
	require.False(t, got.Saturated, "900 units of depth outruns 819 units of hold — this fleet IS capacity-short")
	require.Equal(t, 15, got.UnservedLanes)
}

// THE CAPACITY SIDE IS THE TRADE POOL'S HOLD, NOT THE FLEET'S. Hulls dedicated elsewhere are flying
// contracts, mining or standing as probes; counting their holds would declare a trade surface
// saturated on capacity no lane will ever see. The fixture is built so the answer INVERTS if the
// wrong hulls are summed, which a fixture of identically-tagged hulls could not witness.
func TestLaneDemand_OnlyTheTradePoolsHoldCounts(t *testing.T) {
	r, _, _ := saturationRig(t,
		LaneCensus{Profitable: 24, AbsorbableUnits: 500},
		holdingHulls(2, 100),                                        // 200 units of TRADE hold
		[]hullSpec{{fleet: "contract", system: "X1-AA", hold: 800}}, // invisible to this comparison
	)

	got, readable, err := r.LaneDemand(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, 200, got.TradeHoldCapacity, "only the trade-dedicated hulls' holds may count")
	require.False(t, got.Saturated, "500 units of depth against 200 units of TRADE hold is capacity-short")
}

// THE COLD FLEET MUST STAY BUYABLE. With no trade hull standing there is no hold to compare the
// surface against, and a reader that called that saturation would make the FIRST trade hull
// unbuyable forever — a deadlock no spender could clear.
func TestLaneDemand_AnEmptyTradePoolIsNeverSaturated(t *testing.T) {
	r, _, _ := saturationRig(t, LaneCensus{Profitable: 24, AbsorbableUnits: 741}, otherHulls(3))

	got, readable, err := r.LaneDemand(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, 0, got.TradeHoldCapacity)
	require.False(t, got.Saturated, "a fleet with no trade hull has not saturated anything")
}

// FAIL CLOSED, AND LEAVE THE LATCH ALONE. An unreadable tick is not an observation of "unsaturated"
// — treating it as one would let an outage debounce the fleet back into HEAVY buying, which is the
// spend direction. The wave turns readable=false into PROBE, so the tick costs a deferred purchase
// and nothing else.
func TestLaneDemand_FailsClosedWithoutDisturbingTheLatch(t *testing.T) {
	t.Run("ship read", func(t *testing.T) {
		r := NewUnservedLaneReader(erroringShipRepo(errors.New("db down")), &fakeLaneCounter{readable: true})
		_, readable, err := r.LaneDemand(context.Background(), 1)
		require.Error(t, err)
		require.False(t, readable)
	})
	t.Run("lane surface", func(t *testing.T) {
		r, lanes, clock := saturationRig(t, LaneCensus{Profitable: 24, AbsorbableUnits: 741}, holdingHulls(9, 91))
		first, readable, err := r.LaneDemand(context.Background(), 1)
		require.NoError(t, err)
		require.True(t, readable)
		require.True(t, first.Saturated)

		// A blind stretch longer than the dwell, then the surface comes back deep.
		lanes.readable = false
		for i := 0; i < 4; i++ {
			clock.advance(fleetgrowth.DefaultSaturationDwell)
			_, readable, _ := r.LaneDemand(context.Background(), 1)
			require.False(t, readable, "an unreadable surface must stay unreadable")
		}
		lanes.readable = true
		lanes.census = LaneCensus{Profitable: 24, AbsorbableUnits: 900}
		got, readable, err := r.LaneDemand(context.Background(), 1)
		require.NoError(t, err)
		require.True(t, readable)
		require.True(t, got.Saturated, "the blind stretch must not have counted toward un-latching")
	})
}

// --- the anti-thrash window ---------------------------------------------------------------------

// THE FAILURE MODE THIS WINDOW EXISTS FOR. A depth reading that jitters across the boundary — market
// volumes move every scan — would otherwise flip the regime on every tick, whipsawing the heavy
// pricing errand and the probe queue between two plans neither of which ever runs. The published
// verdict must be unmoved by an oscillation shorter than the dwell, however many times it swings.
func TestLaneDemand_AnOscillatingSurfaceNeverFlipsTheVerdict(t *testing.T) {
	r, lanes, clock := saturationRig(t, LaneCensus{Profitable: 24, AbsorbableUnits: 900}, holdingHulls(9, 91))

	first, _, err := r.LaneDemand(context.Background(), 1)
	require.NoError(t, err)
	require.False(t, first.Saturated, "the fixture starts UNSATURATED, or this test witnesses nothing")

	step := fleetgrowth.DefaultSaturationDwell / 4
	for i := 0; i < 40; i++ {
		if i%2 == 0 {
			lanes.census = LaneCensus{Profitable: 24, AbsorbableUnits: 741} // dips below the threshold
		} else {
			lanes.census = LaneCensus{Profitable: 24, AbsorbableUnits: 900} // and back above it
		}
		clock.advance(step)
		got, _, err := r.LaneDemand(context.Background(), 1)
		require.NoError(t, err)
		require.False(t, got.Saturated, "swing %d flipped the published verdict — the dwell is not holding", i)
	}
}

// A CHANGE THAT PERSISTS IS PUBLISHED, and not one moment before the window closes. Without this the
// dwell could be satisfied by a latch that simply never changes, which would be a different bug
// wearing the same green.
func TestLaneDemand_APersistentChangeIsPublishedWhenTheDwellCloses(t *testing.T) {
	r, lanes, clock := saturationRig(t, LaneCensus{Profitable: 24, AbsorbableUnits: 900}, holdingHulls(9, 91))

	first, _, _ := r.LaneDemand(context.Background(), 1)
	require.False(t, first.Saturated)

	lanes.census = LaneCensus{Profitable: 24, AbsorbableUnits: 741}
	clock.advance(fleetgrowth.DefaultSaturationDwell - time.Second)
	held, _, _ := r.LaneDemand(context.Background(), 1)
	require.False(t, held.Saturated, "one second short of the window must not publish the change")

	clock.advance(2 * time.Second)
	flipped, _, _ := r.LaneDemand(context.Background(), 1)
	require.True(t, flipped.Saturated, "a change that held for the whole window must be published")

	// And the window is SYMMETRIC: coming back is debounced exactly as going was, so a fleet is not
	// released into heavy buying by one deep scan.
	lanes.census = LaneCensus{Profitable: 24, AbsorbableUnits: 900}
	clock.advance(fleetgrowth.DefaultSaturationDwell - time.Second)
	stillSaturated, _, _ := r.LaneDemand(context.Background(), 1)
	require.True(t, stillSaturated.Saturated, "the return trip must be debounced too")

	clock.advance(2 * time.Second)
	released, _, _ := r.LaneDemand(context.Background(), 1)
	require.False(t, released.Saturated)
}

// THE FIRST READING IS ADOPTED WHOLE. The latch is in-memory, so a daemon restart starts with no
// history — and a restart that spent its first dwell reporting "not saturated" would resume heavy
// buying against a surface it had already outgrown, which is the spend direction and exactly the
// defect (RULINGS #2: the state re-derives from durable facts rather than being persisted).
func TestLaneDemand_TheFirstReadingIsAdoptedImmediately(t *testing.T) {
	r, _, _ := saturationRig(t, LaneCensus{Profitable: 24, AbsorbableUnits: 741}, holdingHulls(9, 91))

	got, readable, err := r.LaneDemand(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, readable)
	require.True(t, got.Saturated, "with no history the reader reports what it sees, not a default")
}

// ONE LATCH, WHICHEVER DOOR THE CALLER CAME THROUGH. Both wave consumers share ONE reader precisely
// so the spender and the withholder cannot disagree, and the older UnservedLaneCount entry point
// must therefore advance the SAME window rather than a private one — two windows advancing at two
// rates is the split-brain the shared instance exists to prevent.
func TestLaneDemand_BothEntryPointsAdvanceOneWindow(t *testing.T) {
	r, lanes, clock := saturationRig(t, LaneCensus{Profitable: 24, AbsorbableUnits: 900}, holdingHulls(9, 91))
	_, _, _ = r.LaneDemand(context.Background(), 1)

	lanes.census = LaneCensus{Profitable: 24, AbsorbableUnits: 741}
	// The whole window elapses with only the COUNT entry point being read.
	clock.advance(fleetgrowth.DefaultSaturationDwell + time.Second)
	count, readable, err := r.UnservedLaneCount(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, 15, count)

	got, _, _ := r.LaneDemand(context.Background(), 1)
	require.True(t, got.Saturated, "the count entry point must advance the window the demand read publishes")
}

// THE LATCH IS PER PLAYER. One agent's saturated surface must not debounce another's.
func TestLaneDemand_TheWindowIsKeptPerPlayer(t *testing.T) {
	r, lanes, clock := saturationRig(t, LaneCensus{Profitable: 24, AbsorbableUnits: 900}, holdingHulls(9, 91))
	_, _, _ = r.LaneDemand(context.Background(), 1)

	lanes.census = LaneCensus{Profitable: 24, AbsorbableUnits: 741}
	clock.advance(fleetgrowth.DefaultSaturationDwell + time.Second)

	other, _, err := r.LaneDemand(context.Background(), 2)
	require.NoError(t, err)
	require.True(t, other.Saturated, "player 2 has no history, so it adopts what it sees")

	same, _, _ := r.LaneDemand(context.Background(), 1)
	require.True(t, same.Saturated)
}

// --- the margin knob ----------------------------------------------------------------------------

// THE MARGIN IS OPERATIONAL (RULINGS #5) and reaches the reader through the composition root, the
// same way the ranker's freshness table does. A reader left on the default must run the documented
// default, and a tuned one must run the tune — in the documented direction.
func TestLaneDemand_TheMarginIsHonouredAndDefaulted(t *testing.T) {
	t.Run("raising it declares saturation earlier", func(t *testing.T) {
		r, _, _ := saturationRig(t, LaneCensus{Profitable: 24, AbsorbableUnits: 900}, holdingHulls(9, 91))
		r.SetSaturation(125, 0)
		got, _, _ := r.LaneDemand(context.Background(), 1)
		require.True(t, got.Saturated, "at 125%% of 819 units of hold a 900-unit surface is saturated")
	})
	t.Run("lowering it permits more heavies", func(t *testing.T) {
		r, _, _ := saturationRig(t, LaneCensus{Profitable: 24, AbsorbableUnits: 741}, holdingHulls(9, 91))
		r.SetSaturation(50, 0)
		got, _, _ := r.LaneDemand(context.Background(), 1)
		require.False(t, got.Saturated, "at 50%% the live frame is no longer saturated")
	})
	t.Run("an unset knob takes the documented default", func(t *testing.T) {
		r, _, _ := saturationRig(t, LaneCensus{Profitable: 24, AbsorbableUnits: 741}, holdingHulls(9, 91))
		r.SetSaturation(0, 0)
		got, _, _ := r.LaneDemand(context.Background(), 1)
		require.True(t, got.Saturated, "`tune <key> 0` deletes a key; zero must mean the default, not a 0%% margin")
	})
	t.Run("an unset dwell takes the documented default", func(t *testing.T) {
		r, lanes, clock := saturationRig(t, LaneCensus{Profitable: 24, AbsorbableUnits: 900}, holdingHulls(9, 91))
		r.SetSaturation(0, 0)
		_, _, _ = r.LaneDemand(context.Background(), 1)

		lanes.census = LaneCensus{Profitable: 24, AbsorbableUnits: 741}
		clock.advance(fleetgrowth.DefaultSaturationDwell - time.Second)
		held, _, _ := r.LaneDemand(context.Background(), 1)
		require.False(t, held.Saturated, "a zero dwell must resolve to the documented window, never to no window at all")
	})
}

// --- the published sides ------------------------------------------------------------------------

// BOTH SIDES OF THE COMPARISON ARE PUBLISHED, so a flip is explicable from metrics alone. The depth
// term already existed and was consumed by nothing; the fleet's hold and the threshold it is judged
// against are what made the wave's verdict unreadable from outside.
func TestLaneDemand_PublishesBothSidesOfTheComparison(t *testing.T) {
	component := laneSurfaceRig(t)
	r, _, _ := saturationRig(t, LaneCensus{Profitable: 24, AbsorbableUnits: 741}, holdingHulls(9, 91))

	_, readable, err := r.LaneDemand(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, readable)

	require.Equal(t, float64(741), component("absorbable_units"), "the depth side")
	require.Equal(t, float64(819), component("trade_hold_capacity"), "the fleet-hold side")
	require.Equal(t, float64(819), component("saturation_threshold"), "the number depth is actually compared against")
}

// THE PUBLISHED THRESHOLD MOVES WITH THE KNOB, or an operator reading two series would compute a
// verdict the daemon never reached.
func TestLaneDemand_ThePublishedThresholdCarriesTheMargin(t *testing.T) {
	component := laneSurfaceRig(t)
	r, _, _ := saturationRig(t, LaneCensus{Profitable: 24, AbsorbableUnits: 741}, holdingHulls(9, 91))
	r.SetSaturation(50, 0)

	_, _, err := r.LaneDemand(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, float64(409), component("saturation_threshold"), "half of 819 units of hold")
	require.Equal(t, float64(819), component("trade_hold_capacity"), "the raw hold is published untouched")
}

// AN UNREADABLE SURFACE PUBLISHES A BLIND READ, never a plausible-looking zero threshold beside a
// real hold — the lane surface's own convention, extended to the two new terms.
func TestLaneDemand_ABlindReadPublishesZeroedSides(t *testing.T) {
	component := laneSurfaceRig(t)
	r, lanes, _ := saturationRig(t, LaneCensus{Profitable: 24, AbsorbableUnits: 741}, holdingHulls(9, 91))
	lanes.readable = false

	_, readable, _ := r.LaneDemand(context.Background(), 1)
	require.False(t, readable)
	require.Equal(t, float64(0), component("readable"))
	require.Equal(t, float64(0), component("absorbable_units"))
	require.Equal(t, float64(0), component("saturation_threshold"))
}
