package config

import (
	"time"

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
}

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
