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
	// Default posture is model-ON (the whole point of the bead): an absent section is NOT
	// disabled.
	if c.Disabled {
		t.Fatalf("absent [trade_impact] section must default to model ON (Disabled=false)")
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
