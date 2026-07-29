package persistence

// sp-brr68 — the EXECUTED-shadow hard cap's CALIBRATION, pinned.
//
// This value had no test. That is exactly how it drifted: sp-ircfy re-fitted the recovery
// half-lives 4-6x upward (RESTRICTED 6.9h -> 39.4h, WEAK -> 26.6h) and the 12h cap stayed put,
// even though its own doc comment derived 12h as "~two half-lives of the slowest TAGGED tier
// (RESTRICTED, ~6.9h)". The derivation was falsified by the re-fit; the constant was not.
// A test that pins the NUMBER and names its DERIVATION fails loudly the next time the fit moves.
//
// WHY 4h, measured on live era-5 telemetry (player 5, 2026-07-29) rather than re-derived from the
// half-lives — see the doc comment on DefaultExecutedHardCap for the full argument:
//   * repeat sales into the same (sink, good) run at MEDIAN 1.15h apart, and the realized price is
//     99.2% of the previous sale at <2h, 100.3% at 2-6h, 100.9% at 6-12h — recovery completes ~2h;
//   * 255 of 386 revisits went into sinks that HELD an active EXECUTED shadow and returned 99.7%
//     of the previous price, against 99.6% for unshadowed sinks — the shadow tracks no
//     economically real depletion at current throughput (this is the de-confounder: the price
//     evidence is NOT merely a selection effect of the embargo working);
//   * realized sell price is 101.6% of the market's currently-quoted bid — no persistent depression.
// 4h is ~2x the ~2h full-recovery time and ~3.5x the median revisit gap: margin for a genuinely
// large sale (a bigger fleet will displace more than today's ~1%) without re-creating the embargo.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The recalibrated default. Pinned as a NUMBER on purpose: the previous value survived its own
// rationale being invalidated precisely because nothing asserted it.
func TestDefaultExecutedHardCap_IsTheRecalibratedFourHours(t *testing.T) {
	require.Equal(t, 4*time.Hour, DefaultExecutedHardCap,
		"the EXECUTED-shadow hard cap is calibrated against MEASURED sink recovery (~2h, "+
			"median revisit 1.15h at 99.2% of the previous price), not against the fitted "+
			"half-lives (26-39h) which describe the decay RATE of a displacement we barely create")
}

// RULINGS #4, the fail-CLOSED direction: an absent or nonsensical cap must fall back to the
// default, NEVER to zero. expires_at is written as now+ExecutedHardCap, so a zero cap expires
// every shadow the instant it is written — the ledger would still record rows while embargoing
// nothing, which is the guard silently disappearing rather than failing loudly.
func TestAbsorptionLedgerConfig_ZeroOrNegativeHardCap_FallsBackToDefault_NeverZero(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   time.Duration
	}{
		{"absent/zero", 0},
		{"negative", -3 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := AbsorptionLedgerConfig{ExecutedHardCap: tc.in}.withDefaults()
			require.Equal(t, DefaultExecutedHardCap, got.ExecutedHardCap)
			require.NotZero(t, got.ExecutedHardCap,
				"a zero cap expires every shadow on write — the embargo would vanish silently")
		})
	}
}

// An explicitly configured cap still wins: config.yaml's absorption.executed_hard_cap remains the
// operator override, so a captain can retune without a rebuild.
func TestAbsorptionLedgerConfig_ExplicitHardCap_OverridesTheDefault(t *testing.T) {
	got := AbsorptionLedgerConfig{ExecutedHardCap: 90 * time.Minute}.withDefaults()
	require.Equal(t, 90*time.Minute, got.ExecutedHardCap)
}
