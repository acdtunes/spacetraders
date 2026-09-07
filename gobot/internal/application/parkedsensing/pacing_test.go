package parkedsensing

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// pacing_test.go pins the one-way scaling every paced pass reads its budget through. The
// property that matters most is the NEGATIVE one: PacedBudget may open a pass up and never
// close it down, and a regression there would not fail anywhere visible — the fleet would
// simply chart more slowly, which is the failure mode this change exists to end.

// pacedBases is every constant the coordinator scales, so a base added without a test is
// one nobody checked the arithmetic for.
func pacedBases() map[string]int {
	return map[string]int{
		"gate":     MaxGateReads,
		"expand":   MaxExpansionActions,
		"place":    DefaultMaxPlacementActions,
		"yards":    MaxYardCatalogReads,
		"presence": MaxYardPresenceDispatches,
		"reap":     DefaultMaxReaps,
	}
}

// The byte-identical case, and the regime the ceiling-era constants were sized for.
func TestPacedBudget_AFullyBoundBudgetLeavesEveryPassWhereItWas(t *testing.T) {
	for pass, base := range pacedBases() {
		require.Equal(t, base, PacedBudget(base, trading.APISaturationPermilleMax, ExpansionHeadroomMultiple),
			"%s must pace exactly as it always did at full saturation", pass)
	}
}

func TestPacedBudget_AnIdleBudgetOpensThePassToTheHeadroomMultiple(t *testing.T) {
	for pass, base := range pacedBases() {
		require.Equal(t, base*ExpansionHeadroomMultiple, PacedBudget(base, 0, ExpansionHeadroomMultiple),
			"%s must spend its whole headroom against a budget nobody is queued on", pass)
	}
}

// Pinned exactly rather than approximately: "about 3.5x" fits several curves and only one
// is the linear interpolation the knob's description advertises.
func TestPacedBudget_HalfABoundBudgetIsHalfTheHeadroom(t *testing.T) {
	half := trading.APISaturationPermilleMax / 2
	// base * (1 + 5*0.5) = base * 3.5, floored.
	require.Equal(t, 10, PacedBudget(MaxGateReads, half, ExpansionHeadroomMultiple), "3 * 3.5 = 10.5, floored")
	require.Equal(t, 70, PacedBudget(MaxExpansionActions, half, ExpansionHeadroomMultiple), "20 * 3.5")
	require.Equal(t, 35, PacedBudget(DefaultMaxPlacementActions, half, ExpansionHeadroomMultiple), "10 * 3.5")
	require.Equal(t, 28, PacedBudget(MaxYardCatalogReads, half, ExpansionHeadroomMultiple), "8 * 3.5")
}

// THE ARITHMETIC THE CHANGE WAS SIZED ON, so the knob's advertised numbers cannot drift
// from what the helper does.
func TestPacedBudget_TheMeasuredEraReadingIsWhatSizedTheMultiple(t *testing.T) {
	const sixPercent = 60

	require.Equal(t, 17, PacedBudget(MaxGateReads, sixPercent, ExpansionHeadroomMultiple))
	require.Equal(t, 114, PacedBudget(MaxExpansionActions, sixPercent, ExpansionHeadroomMultiple))
	require.Equal(t, 57, PacedBudget(DefaultMaxPlacementActions, sixPercent, ExpansionHeadroomMultiple))
	require.Equal(t, 45, PacedBudget(MaxYardCatalogReads, sixPercent, ExpansionHeadroomMultiple))
	require.Equal(t, 11, PacedBudget(MaxYardPresenceDispatches, sixPercent, ExpansionHeadroomMultiple))
	require.Equal(t, 114, PacedBudget(DefaultMaxReaps, sixPercent, ExpansionHeadroomMultiple))
}

// THE ONE-WAY PROPERTY, swept rather than sampled: no reading and no multiple, including
// the nonsense ones, may take a pass below the constant it shipped with.
func TestPacedBudget_NeverReturnsLessThanTheBase(t *testing.T) {
	for _, permille := range []int{-1 << 20, -1000, -1, 0, 1, 60, 499, 500, 999, 1000, 1001, 1 << 20} {
		for multiple := -3; multiple <= MaxExpansionHeadroomMultiple+5; multiple++ {
			for pass, base := range pacedBases() {
				require.GreaterOrEqual(t, PacedBudget(base, permille, multiple), base,
					"%s dropped below its shipped budget at permille %d, multiple %d",
					pass, permille, multiple)
			}
		}
	}
}

// Below zero is a fully idle budget, above the maximum a fully committed one.
func TestPacedBudget_AnAbsurdPermilleClampsIntoRange(t *testing.T) {
	full := PacedBudget(MaxGateReads, 0, ExpansionHeadroomMultiple)
	require.Equal(t, full, PacedBudget(MaxGateReads, -1, ExpansionHeadroomMultiple))
	require.Equal(t, full, PacedBudget(MaxGateReads, -999_999, ExpansionHeadroomMultiple))

	require.Equal(t, MaxGateReads, PacedBudget(MaxGateReads, trading.APISaturationPermilleMax+1, ExpansionHeadroomMultiple))
	require.Equal(t, MaxGateReads, PacedBudget(MaxGateReads, 999_999, ExpansionHeadroomMultiple))
}

// Every pass resolves its own documented constant from exactly this value.
func TestPacedBudget_ANonPositiveBaseIsReturnedUnchanged(t *testing.T) {
	for _, permille := range []int{0, 500, 1000} {
		require.Equal(t, 0, PacedBudget(0, permille, ExpansionHeadroomMultiple))
		require.Equal(t, -1, PacedBudget(-1, permille, ExpansionHeadroomMultiple))
	}
}

func TestPacedBudget_AMultipleOfOneRestoresTheShippedPacingExactly(t *testing.T) {
	for _, permille := range []int{0, 1, 60, 500, 999, 1000} {
		for pass, base := range pacedBases() {
			require.Equal(t, base, PacedBudget(base, permille, 1),
				"%s at permille %d must be byte-identical to today under a multiple of one", pass, permille)
		}
	}
}

func TestPacedBudget_TheMultipleResolvesAndClamps(t *testing.T) {
	require.Equal(t, PacedBudget(MaxGateReads, 0, ExpansionHeadroomMultiple), PacedBudget(MaxGateReads, 0, 0),
		"an absent multiple is the documented default")
	require.Equal(t, PacedBudget(MaxGateReads, 0, ExpansionHeadroomMultiple), PacedBudget(MaxGateReads, 0, -4),
		"a negative multiple is the documented default")

	capped := PacedBudget(MaxGateReads, 0, MaxExpansionHeadroomMultiple)
	require.Equal(t, capped, PacedBudget(MaxGateReads, 0, MaxExpansionHeadroomMultiple+50),
		"a multiple past the advertised maximum clamps to it")
}

// MaxWalkRings IS NOT PACED AND MUST NOT BE. It bounds how far the foothold may reach to
// take a working probe OFF a market, so its cost is coverage rather than API calls, and
// scaling it with request headroom would strip markets to fill placements further away.
func TestMaxWalkRings_IsNotABurstBudget(t *testing.T) {
	require.Equal(t, 2, MaxWalkRings, "the foothold reach is an economics bound and is deliberately unscaled")

	// The foothold's walker takes the constant itself, at every reading.
	require.Equal(t, MaxWalkRings, newGateReach(nil, nil, MaxWalkRings).maxHops,
		"the foothold walker takes the raw reach, never a scaled one")
}
