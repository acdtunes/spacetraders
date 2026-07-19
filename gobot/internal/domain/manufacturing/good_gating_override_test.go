package manufacturing

import "testing"

// Per-good buy-gating overrides. This suite pins the pure resolution + guardrail
// behaviour of the shared override type: an overridden good uses its override, a
// non-overridden good is BYTE-IDENTICAL to the global default, and the per-good
// price-ceiling multiplier is HARD-CAPPED at the absolute maximum so a fat-finger can
// never disable the ceiling (RULINGS #4).

func TestStrategyFor_OverrideWinsElseGlobalDefault(t *testing.T) {
	o := GoodGatingOverrides{"SILICON_CRYSTALS": {Strategy: "prefer-buy"}}

	if got := o.StrategyFor("SILICON_CRYSTALS", "smart"); got != "prefer-buy" {
		t.Fatalf("overridden good must use its strategy override, got %q", got)
	}
	// Non-overridden good: byte-identical to the global default.
	if got := o.StrategyFor("COPPER", "smart"); got != "smart" {
		t.Fatalf("a non-overridden good must fall back to the global default, got %q", got)
	}
	// A good present in the map but with no strategy field set also falls back.
	o2 := GoodGatingOverrides{"COPPER": {MinSupply: "SCARCE"}}
	if got := o2.StrategyFor("COPPER", "smart"); got != "smart" {
		t.Fatalf("a good with no strategy override must fall back to the global default, got %q", got)
	}
}

func TestMinSupplyFor_OverrideWinsElseGlobalDefault(t *testing.T) {
	o := GoodGatingOverrides{"SILICON_CRYSTALS": {MinSupply: "SCARCE"}}

	if got := o.MinSupplyFor("SILICON_CRYSTALS", "MODERATE"); got != "SCARCE" {
		t.Fatalf("overridden good must use its min-supply override, got %q", got)
	}
	if got := o.MinSupplyFor("COPPER", "MODERATE"); got != "MODERATE" {
		t.Fatalf("a non-overridden good must fall back to the global floor, got %q", got)
	}
	// Empty override map (the common case) never changes the global floor.
	var none GoodGatingOverrides
	if got := none.MinSupplyFor("COPPER", ""); got != "" {
		t.Fatalf("an empty override map must return the global floor unchanged, got %q", got)
	}
}

func TestPriceCeilingMultFor_OverrideWinsElseGlobalDefault(t *testing.T) {
	o := GoodGatingOverrides{"SILICON_CRYSTALS": {PriceCeilingMult: 3.0}}

	if got := o.PriceCeilingMultFor("SILICON_CRYSTALS", 1.5); got != 3.0 {
		t.Fatalf("overridden good must use its price-ceiling multiplier, got %v", got)
	}
	// Non-overridden good: byte-identical to the global multiplier, uncapped.
	if got := o.PriceCeilingMultFor("COPPER", 1.5); got != 1.5 {
		t.Fatalf("a non-overridden good must fall back to the global multiplier, got %v", got)
	}
	// A zero/absent override multiplier defers to the global default (0 means "unset").
	o2 := GoodGatingOverrides{"COPPER": {Strategy: "prefer-buy"}}
	if got := o2.PriceCeilingMultFor("COPPER", 1.5); got != 1.5 {
		t.Fatalf("a zero override multiplier must defer to the global default, got %v", got)
	}
}

// GUARDRAIL (RULINGS #4): a per-good override multiplier is HARD-CAPPED at MaxPriceCeilingMultiplier
// so a fat-finger (e.g. 1000x) can never effectively disable the ladder-chase ceiling.
func TestPriceCeilingMultFor_HardCapsFatFingerOverride(t *testing.T) {
	o := GoodGatingOverrides{"SILICON_CRYSTALS": {PriceCeilingMult: 1000.0}}

	got := o.PriceCeilingMultFor("SILICON_CRYSTALS", 1.5)
	if got != MaxPriceCeilingMultiplier {
		t.Fatalf("a fat-finger override %v must be clamped to the absolute cap %v, got %v", 1000.0, MaxPriceCeilingMultiplier, got)
	}
	// The global default is never clamped (regression: only the override branch is capped),
	// so a good with no override keeps whatever global multiplier was configured.
	if got := o.PriceCeilingMultFor("COPPER", 100.0); got != 100.0 {
		t.Fatalf("the global multiplier must pass through un-clamped for a non-overridden good, got %v", got)
	}
}

// Persistence (RULINGS #2): the map round-trips through its JSON codec so it survives a daemon
// bounce. An empty map encodes to "" (nothing persisted) and "" decodes back to an empty map.
func TestGoodGatingOverrides_JSONRoundTrip(t *testing.T) {
	original := GoodGatingOverrides{
		"SILICON_CRYSTALS": {Strategy: "prefer-buy", PriceCeilingMult: 3.0, MinSupply: "SCARCE"},
		"COPPER":           {MinSupply: "LIMITED"},
	}

	encoded := original.Encode()
	if encoded == "" {
		t.Fatalf("a non-empty override map must encode to a non-empty JSON string")
	}

	decoded, err := DecodeGoodGatingOverrides(encoded)
	if err != nil {
		t.Fatalf("decode returned error: %v", err)
	}
	if len(decoded) != 2 {
		t.Fatalf("round-trip must preserve every good, got %d entries", len(decoded))
	}
	si := decoded["SILICON_CRYSTALS"]
	if si.Strategy != "prefer-buy" || si.PriceCeilingMult != 3.0 || si.MinSupply != "SCARCE" {
		t.Fatalf("round-trip must preserve every field, got %+v", si)
	}

	// Empty encodes to "" and "" decodes to an empty (nil) map with no error.
	var empty GoodGatingOverrides
	if empty.Encode() != "" {
		t.Fatalf("an empty override map must encode to the empty string, got %q", empty.Encode())
	}
	back, err := DecodeGoodGatingOverrides("")
	if err != nil || len(back) != 0 {
		t.Fatalf("decoding the empty string must yield an empty map with no error, got %v / %v", back, err)
	}
}

// A malformed persisted blob must surface an error rather than silently returning a partial map,
// so a corrupt config is visible rather than quietly dropping a bottleneck's override.
func TestDecodeGoodGatingOverrides_MalformedErrors(t *testing.T) {
	if _, err := DecodeGoodGatingOverrides("{not json"); err == nil {
		t.Fatalf("a malformed override blob must return an error")
	}
}
