package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/fleetgrowth"
)

// AN ABSENT SECTION RUNS THE FITTED TERM. The switch-back is ARMED on deploy, so a daemon whose
// config never mentions [trading].trade_saturation must run the documented default — not a 0%
// margin and no window, which are the two values that switch the term off.
func TestTradeSaturationConfig_AbsentSectionResolvesToTheDocumentedDefaults(t *testing.T) {
	marginPct, dwell := TradeSaturationConfig{}.Resolved()

	require.Equal(t, fleetgrowth.DefaultSaturationMarginPct, marginPct)
	require.Equal(t, fleetgrowth.DefaultSaturationDwell, dwell)
}

// A TUNE IS HONOURED, IN SECONDS FOR THE WINDOW. The knob is operational (RULINGS #5) and a
// captain's retune of a window sized to a coordinator tick is expressed in seconds, so the
// conversion is part of the contract rather than an implementation detail.
func TestTradeSaturationConfig_ATuneIsHonoured(t *testing.T) {
	marginPct, dwell := TradeSaturationConfig{MarginPct: 125, DwellSeconds: 300}.Resolved()

	require.Equal(t, 125, marginPct)
	require.Equal(t, 300*time.Second, dwell)
}

// ZERO IS AN UNSET KNOB, NOT A SETTING. `tune <key> 0` deletes a key rather than setting it, so
// zero — and any negative left by a hand-edited file — must mean "the documented default". A 0%
// margin would saturate nothing but an empty surface and a zero dwell would make the anti-thrash
// window a no-op: the two ways an operator meaning to RESET this term would instead disable it.
func TestTradeSaturationConfig_NonPositiveKnobsAreUnsetNotOff(t *testing.T) {
	for _, c := range []TradeSaturationConfig{
		{MarginPct: 0, DwellSeconds: 0},
		{MarginPct: -1, DwellSeconds: -1},
		{MarginPct: 125, DwellSeconds: 0},
		{MarginPct: 0, DwellSeconds: 300},
	} {
		marginPct, dwell := c.Resolved()
		require.Positive(t, marginPct, "%+v resolved to a non-positive margin", c)
		require.Positive(t, dwell, "%+v resolved to a non-positive dwell", c)
	}
}

// THE SECTION IS REACHABLE UNDER ITS DOCUMENTED KEYS. A resolver that is correct but wired to a
// mapstructure tag nobody writes is a knob that silently never applies.
func TestTradingConfig_CarriesTheSaturationSection(t *testing.T) {
	cfg := TradingConfig{TradeSaturation: TradeSaturationConfig{MarginPct: 80, DwellSeconds: 60}}

	marginPct, dwell := cfg.TradeSaturation.Resolved()

	require.Equal(t, 80, marginPct)
	require.Equal(t, time.Minute, dwell)
}
