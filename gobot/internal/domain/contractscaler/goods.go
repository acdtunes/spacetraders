package contractscaler

// FarSourceGoods is the FIXED far-source whitelist the contract depot warehouse stocks — the goods the
// stocker sources far and the warehouse pre-stages so a contract-delivery hull draws them centrally
// instead of making the long far-source haul. It is the economy-analyst's AUTHORITATIVE definition
// (st-wisp-2h6r5): the universe-invariant ores / precious metals+stones / drugs — IDENTICAL every era,
// so it is a CONSTANT, not a computation. It is deliberately NOT demand-mined, NOT derived from this
// era's exports, and NOT value-ranked (sp-9le3x: no runtime solver, no re-sensing, no recompute). A
// good not contracted this era simply stays inert in the whitelist (harmless — the stocker only fills
// what a live contract draws down). The per-era resolution is WHERE only (the far-source export
// waypoints + the central hub, RULINGS #14 home-system scoped); the good SYMBOLS are this constant.
var FarSourceGoods = []string{
	"COPPER_ORE",
	"IRON_ORE",
	"ALUMINUM_ORE",
	"GOLD",
	"SILVER",
	"DIAMONDS",
	"PRECIOUS_STONES",
	"DRUGS",
}

// DepotUnitsPerGood is the flat per-good buffer cap the depot warehouse stocks each FarSourceGoods
// symbol to — ~2× the typical ~70-unit contract quantity, so a single contract draw never stocks the
// good out (40/good would stock out every draw, making the buffer useless). A NAMED CONSTANT, not a
// live knob (the Admiral wants minimal knobs; the category is universe-invariant, so it never needs
// per-op tuning).
const DepotUnitsPerGood = 140

// DepotTargetUnits builds the per-good cap map pinned onto a scaler-grown depot warehouse — every
// FarSourceGoods symbol at DepotUnitsPerGood. A FRESH map each call so a consumer that mutates its caps
// can never corrupt the fixed definition. Fixed at arm, never recomputed.
func DepotTargetUnits() map[string]int {
	caps := make(map[string]int, len(FarSourceGoods))
	for _, good := range FarSourceGoods {
		caps[good] = DepotUnitsPerGood
	}
	return caps
}
