package config_test

import (
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// An absent/zero [trade_impact] section resolves to the analyst's era-3 defaults, so the
// daemon runs the fitted model with no config at all.
func TestTradeImpactConfig_ZeroValueResolvesEra3Defaults(t *testing.T) {
	var c config.TradeImpactConfig // all zero (absent section)

	if got := c.ResolvedBuyImpact(); got != trading.DefaultBuyImpactCoefficient {
		t.Fatalf("unset buy impact: got %v, want era-3 default %v", got, trading.DefaultBuyImpactCoefficient)
	}
	if got := c.ResolvedSellImpact(); got != trading.DefaultSellImpactCoefficient {
		t.Fatalf("unset sell impact: got %v, want era-3 default %v", got, trading.DefaultSellImpactCoefficient)
	}
	if got := c.ResolvedCooldownTau(); got != trading.DefaultCooldownTau {
		t.Fatalf("unset cooldown tau: got %v, want era-3 default %v", got, trading.DefaultCooldownTau)
	}
}

// An explicitly-set knob (a next-era refit) overrides the default — proving the
// coefficients are live config, not baked-in constants.
func TestTradeImpactConfig_ExplicitValuesOverrideDefaults(t *testing.T) {
	c := config.TradeImpactConfig{BuyImpact: 0.070, SellImpact: 0.022, CooldownTauMinutes: 900}

	if got := c.ResolvedBuyImpact(); got != 0.070 {
		t.Fatalf("refit buy impact: got %v, want 0.070", got)
	}
	if got := c.ResolvedSellImpact(); got != 0.022 {
		t.Fatalf("refit sell impact: got %v, want 0.022", got)
	}
	if got := c.ResolvedCooldownTau(); got != 900*time.Minute {
		t.Fatalf("refit cooldown tau: got %v, want 900m", got)
	}
}

// sp-v34b: the scan-load knobs resolve to their operational defaults when unset, honor an
// explicit dial (up before a refit, down to save API), and clamp a >1 rate to full
// collection.
func TestTradeImpactConfig_ScanPolicyResolution(t *testing.T) {
	var zero config.TradeImpactConfig
	if got := zero.ResolvedScanMaxAge(); got != 75*time.Second {
		t.Fatalf("unset scan max age: got %v, want 75s default", got)
	}
	if got := zero.ResolvedImpactSampleRate(); got != 0.15 {
		t.Fatalf("unset impact sample rate: got %v, want 0.15 default", got)
	}
	policy := zero.ResolvedScanPolicy()
	if policy.MaxScanAge != 75*time.Second || policy.ImpactSampleRate != 0.15 {
		t.Fatalf("default scan policy: got %+v, want {75s, 0.15}", policy)
	}

	// Explicit dial: raise the sample rate before an era refit, tighten the freshness window.
	dialed := config.TradeImpactConfig{ScanMaxAgeSeconds: 120, ImpactSampleRate: 0.5}
	if got := dialed.ResolvedScanMaxAge(); got != 120*time.Second {
		t.Fatalf("dialed scan max age: got %v, want 120s", got)
	}
	if got := dialed.ResolvedImpactSampleRate(); got != 0.5 {
		t.Fatalf("dialed impact sample rate: got %v, want 0.5", got)
	}

	// A rate over 1 clamps to full collection (never > 1).
	if got := (config.TradeImpactConfig{ImpactSampleRate: 2.0}).ResolvedImpactSampleRate(); got != 1.0 {
		t.Fatalf("over-unit sample rate must clamp to 1.0, got %v", got)
	}
}

// sp-0dat: impact_sampling_disabled zeroes the deliberate post-trade impact instrumentation
// (behavior 2) while the recent-scan freshness gate (behavior 1) stays fully ON. This is the
// distinct middle ground the rate knob alone can't express: impact_sample_rate follows the
// struct-wide "0 → era-3 default 0.15" convention, so it can never resolve to a literal 0 —
// an operator asking for "instrumentation to 0" flips this switch instead.
func TestTradeImpactConfig_ImpactSamplingDisabled_KeepsFreshnessGate(t *testing.T) {
	// Switch alone: policy is still STAMPED (freshness gate governs), but the
	// impact-sample rate is a hard 0 — sampleImpact(_, 0) never fires an instrumentation scan.
	off := config.TradeImpactConfig{ImpactSamplingDisabled: true}
	policy := off.ResolvedScanPolicy()
	if policy.ImpactSampleRate != 0 {
		t.Fatalf("impact_sampling_disabled must zero the sample rate: got %v, want 0", policy.ImpactSampleRate)
	}
	if policy.MaxScanAge != 75*time.Second {
		t.Fatalf("impact_sampling_disabled must LEAVE the freshness window intact: got %v, want 75s", policy.MaxScanAge)
	}

	// Composes with an explicit freshness window: the operator can zero instrumentation AND
	// tune the dedup window in the same section.
	tuned := config.TradeImpactConfig{ImpactSamplingDisabled: true, ScanMaxAgeSeconds: 120}
	tp := tuned.ResolvedScanPolicy()
	if tp.ImpactSampleRate != 0 || tp.MaxScanAge != 120*time.Second {
		t.Fatalf("disabled+tuned: got %+v, want {120s, 0}", tp)
	}

	// A non-zero configured rate is OVERRIDDEN by the switch — the kill switch wins, so an
	// operator can hold a refit rate in config yet still cut instrumentation instantly.
	both := config.TradeImpactConfig{ImpactSamplingDisabled: true, ImpactSampleRate: 0.5}
	if bp := both.ResolvedScanPolicy(); bp.ImpactSampleRate != 0 {
		t.Fatalf("kill switch must override a configured rate: got %v, want 0", bp.ImpactSampleRate)
	}
}
