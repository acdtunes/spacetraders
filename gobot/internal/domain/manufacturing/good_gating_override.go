package manufacturing

import (
	"encoding/json"
	"strings"
)

// Per-good buy-gating overrides. The supply-chain buy-gating knobs (supply strategy,
// input price ceiling, construction min-supply floor) are all GLOBAL: to unstick ONE bottleneck
// good you had to loosen gating chain-wide, over-buying every good. This type is the surgical
// per-good knob: a map good -> { strategy?, priceCeilingMult?, minSupply? } layered on the three
// global gates. At each gate's decision point the caller looks up the good; if an override is
// present it is used, otherwise the existing GLOBAL default is returned UNCHANGED — so a
// non-overridden good behaves byte-identically to today.
//
// The analyst owns the override VALUES (which goods, what tuning). The plumbing, lookup, and the
// money-integrity guardrail below are the mechanism.
//
// The map lives in the PERSISTED factory / construction-pipeline launch config (Encode/Decode
// below), NOT a launch-frozen-only field, so a daemon bounce keeps the overrides (RULINGS #2).
//
// WHERE EACH FIELD IS CONSUMED:
//   - Strategy         — the SupplyChainResolver's per-node buy-vs-fabricate decision, ctx-stamped
//     on the factory-coordinator engine (WithGoodGatingOverrides).
//   - PriceCeilingMult — the executor's per-tranche ladder-chase ceiling (inputPriceCeilingParked),
//     ctx-stamped on the factory-coordinator engine. HARD-CAPPED (see below).
//   - MinSupply        — the construction pipeline's EXPORT sourcing floor, persisted on the
//     pipeline entity and read by the planner + task activator.
type GoodGatingOverride struct {
	// Strategy overrides the acquisition strategy for this good: "prefer-buy" | "prefer-fabricate"
	// | "smart". Empty = no override (use the resolver's global strategy).
	Strategy string `json:"strategy,omitempty"`
	// PriceCeilingMult overrides the ladder-chase input-price-ceiling multiplier for this good.
	// 0/absent = no override (use the global multiplier). HARD-CAPPED at MaxPriceCeilingMultiplier
	// at the point of use so a fat-finger can never disable the ceiling (RULINGS #4).
	PriceCeilingMult float64 `json:"priceCeilingMult,omitempty"`
	// MinSupply overrides the construction EXPORT sourcing floor for this good: SCARCE | LIMITED |
	// MODERATE | HIGH | ABUNDANT. Empty = no override (use the pipeline's global floor).
	MinSupply string `json:"minSupply,omitempty"`
}

// GoodGatingOverrides maps a good symbol to its per-good gate override.
type GoodGatingOverrides map[string]GoodGatingOverride

// MaxPriceCeilingMultiplier is the ABSOLUTE hard cap on a per-good price-ceiling override
// (RULINGS #4 — a per-good price override is a ladder-chase bleed surface). A per-good
// priceCeilingMult tunes the per-tranche ladder ceiling ONLY, and even a fat-finger value can
// never exceed this cap — so the ladder-chase guard can be LOOSENED for a stuck good but never
// DISABLED. This is a deliberate safety bound (like the immutable 50k working-capital floor),
// NOT an operational knob, so a constant is correct here (RULINGS #5's hard-floor exception).
// It sits comfortably above the expected surgical range while refusing anything that would
// neutralise the guard.
const MaxPriceCeilingMultiplier = 5.0

// StrategyFor returns the acquisition strategy for a good: the per-good override when present and
// non-empty, otherwise the caller's global default unchanged.
func (o GoodGatingOverrides) StrategyFor(good, globalDefault string) string {
	if ov, ok := o[good]; ok && ov.Strategy != "" {
		return ov.Strategy
	}
	return globalDefault
}

// MinSupplyFor returns the EXPORT sourcing floor for a good: the per-good override when present and
// non-empty, otherwise the caller's global floor unchanged.
func (o GoodGatingOverrides) MinSupplyFor(good, globalDefault string) string {
	if ov, ok := o[good]; ok && ov.MinSupply != "" {
		return ov.MinSupply
	}
	return globalDefault
}

// PriceCeilingMultFor returns the input-price-ceiling multiplier for a good. With no override (or a
// zero/absent override value) it returns the caller's global default UNCHANGED and un-clamped — so
// a non-overridden good is byte-identical to today. When a positive per-good override IS present it
// is used, but HARD-CAPPED at MaxPriceCeilingMultiplier so a fat-finger can never disable the
// ladder-chase ceiling (RULINGS #4).
func (o GoodGatingOverrides) PriceCeilingMultFor(good string, globalDefault float64) float64 {
	ov, ok := o[good]
	if !ok || ov.PriceCeilingMult <= 0 {
		return globalDefault
	}
	if ov.PriceCeilingMult > MaxPriceCeilingMultiplier {
		return MaxPriceCeilingMultiplier
	}
	return ov.PriceCeilingMult
}

// Encode serialises the override map to a compact JSON string for persistence in the launch
// config / pipeline row. An empty map encodes to "" so nothing is persisted for the common
// no-override case. A marshal error (which json.Marshal never returns for this value type)
// degrades to "" — the safe, guard-tightening default (no override).
func (o GoodGatingOverrides) Encode() string {
	if len(o) == 0 {
		return ""
	}
	b, err := json.Marshal(o)
	if err != nil {
		return ""
	}
	return string(b)
}

// DecodeGoodGatingOverrides parses a persisted JSON override blob. An empty/whitespace string
// yields a nil map with no error (the no-override case). A malformed blob returns an error so a
// corrupt config surfaces rather than silently dropping a bottleneck's override.
func DecodeGoodGatingOverrides(encoded string) (GoodGatingOverrides, error) {
	if strings.TrimSpace(encoded) == "" {
		return nil, nil
	}
	var o GoodGatingOverrides
	if err := json.Unmarshal([]byte(encoded), &o); err != nil {
		return nil, err
	}
	return o, nil
}
