package config

import (
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// TradeImpactConfig holds the era-3 price-impact + weak-recovery coefficients the
// trade-route coordinator's lane ranker uses (sp-tl68). The economy analyst fit these
// on era-3 telemetry — a per-trade price-impact + slow-recovery model that beats
// snapshot pricing 40-49% out-of-sample on next-leg price MAE (a real edge on the
// income stream, ~80% of income).
//
// Every field is REFIT PER ERA: the model's FORM (linear price impact in
// x = units/tradeVolume, exponential compression-debt decay) is stable, but the NUMBERS
// are re-fitted each era, so they live here as config — a captain re-tunes by editing
// config.yaml and restarting the daemon (RULINGS #5, no code redeploy) — never as
// hardcoded constants. A zero/absent value defers to its era-3 default (the
// trading.Default* constants), so an absent [trade_impact] section runs the analyst's
// era-3 fit unchanged.
type TradeImpactConfig struct {
	// BuyImpact is the fractional ask rise per full tradeVolume bought (dP/P(buy) =
	// +BuyImpact·x). Era-3 fit ~0.0499, rounded operational default 0.050. 0 → default.
	BuyImpact float64 `mapstructure:"buy_impact"`
	// SellImpact is the fractional bid fall per full tradeVolume sold (dP/P(sell) =
	// -SellImpact·x). Era-3 fit ~0.0152, rounded operational default 0.015. 0 → default.
	SellImpact float64 `mapstructure:"sell_impact"`
	// CooldownTauMinutes is the compression-debt decay constant τ in MINUTES: accrued
	// debt decays as exp(-dt/τ), giving a half-life of τ·ln2. Era-3 pooled default 750
	// (half-life ≈ 520 min) — organic recovery is WEAK, compressed lanes stay compressed
	// for HOURS. 0 → default. Minutes (not a raw Duration) because the analyst's fit and
	// the refit knob are naturally expressed in minutes.
	CooldownTauMinutes int `mapstructure:"cooldown_tau_minutes"`

	// Disabled turns the WHOLE sp-tl68 impact+cooldown model OFF: lane ranking reverts to
	// the snapshot spread (pre-sp-tl68 behavior, byte-for-byte) and the cooldown ledger is
	// left unwired. This is the operator's instant revert for a LIVE ranking change — flip
	// it and restart, no code redeploy — following the same kill-switch convention as
	// AbsorptionConfig.TradeRouteConsultDisabled. Absent/false = model ON (the analyst's
	// era-3 fit is the intended default posture, the whole point of the bead).
	Disabled bool `mapstructure:"disabled"`

	// ScanMaxAgeSeconds is the sp-v34b recent-scan freshness window: an arrival or
	// post-trade decision scan whose cached market was refreshed within this many seconds
	// reuses the cache instead of re-calling GetMarket — the redundant re-scan killer that
	// takes tour market scanning off the ~80%-of-API wall. 0 → the 75s default. Seconds
	// because the operational window is tens of seconds (a hull's scan→buy→scan round trip).
	// This governs COLLECTION load only; the sp-tl68 ranker reads whatever cached data
	// exists (slightly-older-but-fresh-enough), so guards/capital/ranking are untouched.
	ScanMaxAgeSeconds int `mapstructure:"scan_max_age_seconds"`
	// ImpactSampleRate is the FRACTION of trades on which the deliberate post-trade impact
	// scan (the paired before/after that records dP/P) still fires, so the analyst keeps
	// enough consecutive-leg pairs to REFIT the model per era (~1 day at 0.15 suffices). A
	// non-sampled trade falls back to the freshness gate (one fresh decision scan, no extra
	// measurement scan). 0 → the 0.15 default; DIAL UP toward 1.0 to gather a fresh refit
	// corpus before an era transition, DOWN to shed more API. Clamped to [0,1].
	ImpactSampleRate float64 `mapstructure:"impact_sample_rate"`
	// ScanSamplingDisabled reverts sp-v34b entirely: the trade coordinators stamp NO scan
	// policy, so every arrival and post-trade scan is unconditional (pre-sp-v34b behavior,
	// byte-for-byte). The operator's instant revert for the scan-load change — flip it and
	// restart — mirroring Disabled's kill-switch convention. Absent/false = sp-v34b ON.
	ScanSamplingDisabled bool `mapstructure:"scan_sampling_disabled"`
	// ImpactSamplingDisabled zeroes JUST the deliberate post-trade impact instrumentation
	// (sp-v34b behavior 2 — the paired before/after scans the analyst refits from) while the
	// recent-scan freshness gate (behavior 1) stays fully live. This is the middle ground the
	// ImpactSampleRate knob alone cannot express (sp-0dat): that field follows the struct-wide
	// "0 → era-3 default 0.15" convention, so it can never resolve to a literal 0 — an operator
	// who wants instrumentation OFF but the redundant-scan dedup kept ON flips this switch. It
	// differs from ScanSamplingDisabled, which reverts BOTH behaviors (unconditional scanning).
	// Absent/false = the resolved ImpactSampleRate governs (sp-v34b unchanged).
	ImpactSamplingDisabled bool `mapstructure:"impact_sampling_disabled"`
}

// sp-v34b scan-load defaults (config package locals, not domain constants — they govern
// telemetry COLLECTION cadence, not the model's numeric form).
const (
	defaultScanMaxAgeSeconds = 75
	defaultImpactSampleRate  = 0.15
)

// ResolvedBuyImpact returns the configured buy-impact coefficient or the era-3 default
// when unset (non-positive). Centralizes default resolution so the ranker and the
// cooldown ledger agree on the same effective value.
func (c TradeImpactConfig) ResolvedBuyImpact() float64 {
	if c.BuyImpact > 0 {
		return c.BuyImpact
	}
	return trading.DefaultBuyImpactCoefficient
}

// ResolvedSellImpact returns the configured sell-impact coefficient or the era-3 default.
func (c TradeImpactConfig) ResolvedSellImpact() float64 {
	if c.SellImpact > 0 {
		return c.SellImpact
	}
	return trading.DefaultSellImpactCoefficient
}

// ResolvedCooldownTau returns the configured decay constant as a Duration, or the era-3
// default when unset (non-positive).
func (c TradeImpactConfig) ResolvedCooldownTau() time.Duration {
	if c.CooldownTauMinutes > 0 {
		return time.Duration(c.CooldownTauMinutes) * time.Minute
	}
	return trading.DefaultCooldownTau
}

// ResolvedScanMaxAge returns the sp-v34b recent-scan freshness window as a Duration, or
// the 75s default when unset (non-positive).
func (c TradeImpactConfig) ResolvedScanMaxAge() time.Duration {
	if c.ScanMaxAgeSeconds > 0 {
		return time.Duration(c.ScanMaxAgeSeconds) * time.Second
	}
	return defaultScanMaxAgeSeconds * time.Second
}

// ResolvedImpactSampleRate returns the configured impact-sample fraction clamped to
// [0,1], or the 0.15 default when unset (non-positive). A value >1 clamps to 1 (every
// trade instrumented — the pre-sp-v34b full-collection posture).
func (c TradeImpactConfig) ResolvedImpactSampleRate() float64 {
	if c.ImpactSampleRate <= 0 {
		return defaultImpactSampleRate
	}
	if c.ImpactSampleRate > 1 {
		return 1
	}
	return c.ImpactSampleRate
}

// ResolvedScanPolicy bundles the two sp-v34b knobs into the ctx policy a trade
// coordinator stamps, or reports ok=false when ScanSamplingDisabled reverts the feature
// (the coordinator then stamps nothing → pre-sp-v34b full-scan behavior everywhere).
func (c TradeImpactConfig) ResolvedScanPolicy() (shared.ScanPolicy, bool) {
	if c.ScanSamplingDisabled {
		return shared.ScanPolicy{}, false
	}
	// The impact-sampling kill switch (sp-0dat) forces the rate to a hard 0 — sampleImpact
	// never fires an instrumentation scan, so the freshness gate (MaxScanAge, untouched)
	// governs every trade. The policy is still STAMPED (ok=true) so behavior 1 stays live.
	sampleRate := c.ResolvedImpactSampleRate()
	if c.ImpactSamplingDisabled {
		sampleRate = 0
	}
	return shared.ScanPolicy{
		MaxScanAge:       c.ResolvedScanMaxAge(),
		ImpactSampleRate: sampleRate,
	}, true
}
