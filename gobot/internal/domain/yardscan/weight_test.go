package yardscan

// Unit tests for the shipyard value term (sp-mb0er). The budget arithmetic these
// weights feed is marketscan's and is tested there; what is proven here is the
// ORDERING — that the yards the fleet is about to spend at outrank the ones it is
// not, which is the property whose absence cost the incident this package
// documents.

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

const testClampR = 8

// THE INCIDENT, AS AN ORDERING. 84 yards were known to sell SHIP_HEAVY_FREIGHTER
// and 4 had a price; the 80 unpriced ones sat 21-24 hours while the fleet bought
// at prices it could no longer compare. Under a cadence floor every one of those
// yards was worth the same — that is what a floor means. Here the unpriced ones
// must strictly outrank both the priced ones and the yards selling nothing wanted,
// so the rotation drains the backlog instead of sampling it uniformly.
func TestWeight_UnpricedWantedYardOutranksEveryNonTargetedYard(t *testing.T) {
	unpricedWanted := Weight(Facts{SellsWanted: true, Priced: false}, testClampR)
	pricedWanted := Weight(Facts{SellsWanted: true, Priced: true}, testClampR)
	unknown := Weight(Facts{Unknown: true}, testClampR)
	dull := Weight(Facts{}, testClampR)

	require.Greater(t, unpricedWanted, pricedWanted,
		"a yard we know sells a wanted hull but have never priced is the most valuable read there is")
	require.Greater(t, unpricedWanted, unknown,
		"a confirmed unpriced lead outranks a yard whose catalogue we have never opened")
	require.Greater(t, pricedWanted, dull,
		"a priced wanted yard still outranks a yard selling nothing we want")
	require.Equal(t, Baseline, dull,
		"a scanned yard selling nothing wanted sits at the floor of the weight domain")
}

// "About to buy HERE" is the strongest signal the weighting has, and it must beat
// every type-level signal: the rotation has to keep the counter we are mid-purchase
// at fresh, not merely the class of yard it belongs to.
func TestWeight_TargetedYardIsTheTop(t *testing.T) {
	targeted := Weight(Facts{Targeted: true}, testClampR)

	require.Equal(t, float64(testClampR), targeted,
		"a yard a money guard just priced at sits at the clamp")
	for name, other := range map[string]Facts{
		"unpriced wanted": {SellsWanted: true},
		"priced wanted":   {SellsWanted: true, Priced: true},
		"unknown":         {Unknown: true},
		"dull":            {},
	} {
		require.GreaterOrEqual(t, targeted, Weight(other, testClampR),
			"targeted must not be outranked by %s", name)
	}
}

// Targeting is read BEFORE the catalogue, so a yard we are buying at is top-weight
// even when its own rows say it sells nothing we asked for — which is exactly the
// state a yard is in the first time we price an unlisted hull at it.
func TestWeight_TargetedBeatsAnEmptyCatalogue(t *testing.T) {
	require.Equal(t, float64(testClampR), Weight(Facts{Targeted: true, SellsWanted: false, Priced: true}, testClampR))
}

// Every weight must lie inside [Baseline, clampR]. That is not decoration: the
// anti-starvation bound marketscan proves is only sound because no yard can weigh
// less than the baseline, and the interval is only bounded because none can weigh
// more than the clamp.
func TestWeight_StaysInsideTheClampDomain(t *testing.T) {
	for _, f := range []Facts{
		{}, {Unknown: true}, {SellsWanted: true}, {SellsWanted: true, Priced: true},
		{Targeted: true}, {Unknown: true, Targeted: true}, {SellsWanted: true, Priced: true, Targeted: true},
	} {
		w := Weight(f, testClampR)
		require.GreaterOrEqual(t, w, Baseline, "facts %+v fell below the baseline", f)
		require.LessOrEqual(t, w, float64(testClampR), "facts %+v exceeded the clamp", f)
	}
}

// The middle tier is the GEOMETRIC mean, not the arithmetic one — weights divide a
// fixed allowance, so they compose multiplicatively and the honest midpoint is the
// one that stays a midpoint as the clamp moves. Pinned because an arithmetic
// midpoint would drift toward the top at large clamps and quietly flatten the
// distinction between "confirmed unpriced lead" and "might be anything".
func TestWeight_MiddleTierIsGeometric(t *testing.T) {
	for _, clamp := range []int{2, 4, 8, 16} {
		mid := Weight(Facts{Unknown: true}, clamp)
		require.InDelta(t, math.Sqrt(float64(clamp)), mid, 1e-9)
		require.InDelta(t, mid, Weight(Facts{SellsWanted: true, Priced: true}, clamp), 1e-9,
			"an unopened catalogue and a priced-but-drifting lead are worth the same")
		// The defining property: the ratio top:mid equals mid:baseline.
		require.InDelta(t, float64(clamp)/mid, mid/Baseline, 1e-9)
	}
}

// A clamp of 1 is the documented way to switch demand weighting off: admission
// then turns purely on dueness and token availability. A clamp below 1 is
// nonsense and must collapse the same way rather than inverting the ordering.
func TestWeight_ClampOfOneOrLessFlattensEveryTier(t *testing.T) {
	for _, clamp := range []int{1, 0, -3} {
		for _, f := range []Facts{
			{}, {Unknown: true}, {SellsWanted: true}, {Targeted: true},
		} {
			require.Equal(t, Baseline, Weight(f, clamp),
				"clamp %d must flatten facts %+v to the baseline", clamp, f)
		}
	}
}

// PriorWeight is what an unopened yard is worth, and it must sit strictly above
// the baseline: discovery is funded out of the ordinary rotation rather than
// needing an allowance of its own, which only works if an unseen yard is looked at
// sooner than one seen and found dull.
func TestPriorWeight_IsTheUnknownTierAndBeatsTheBaseline(t *testing.T) {
	require.Equal(t, Weight(Facts{Unknown: true}, testClampR), PriorWeight(testClampR))
	require.Greater(t, PriorWeight(testClampR), Baseline)
	require.Less(t, PriorWeight(testClampR), float64(testClampR),
		"an unopened catalogue is speculative and must not outrank a confirmed unpriced lead")
}
