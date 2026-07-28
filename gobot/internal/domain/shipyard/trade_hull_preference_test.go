package shipyard

import "testing"

// TestTradeHullPreferenceOrderRanksHeaviesAboveTheLightFreighter pins the ORDER, which is
// the whole contract: a consumer walks this list and takes the first priceable type, so
// heavies-first is what makes the trade-hull fallback self-correcting — the moment a heavy
// yard is discovered the preferred hull wins back with no config change. A list that put
// the light freighter first would be a permanent downgrade wearing a fallback's name.
func TestTradeHullPreferenceOrderRanksHeaviesAboveTheLightFreighter(t *testing.T) {
	if len(TradeHullPreferenceOrder) != len(heavyHullClasses)+1 {
		t.Fatalf("TradeHullPreferenceOrder = %v, want every heavy class plus the light freighter", TradeHullPreferenceOrder)
	}
	for i, c := range heavyHullClasses {
		if TradeHullPreferenceOrder[i] != c.ShipType {
			t.Fatalf("TradeHullPreferenceOrder[%d] = %s, want the heavy class %s — heavies must rank first",
				i, TradeHullPreferenceOrder[i], c.ShipType)
		}
	}
	if last := TradeHullPreferenceOrder[len(TradeHullPreferenceOrder)-1]; last != lightFreightClass.ShipType {
		t.Fatalf("last preference = %s, want the light freighter %s", last, lightFreightClass.ShipType)
	}
}

// TestTradeHullPreferenceOrderExcludesNonFreightHulls is the guard that matters most for
// spending. Every type below is priceable at a yard the fleet has already discovered, so
// if any leaked into this list the autosizer would substitute it into a trade lane — and
// the realized-rate guards judge the FLEET's rate, not the candidate's capability, so they
// could not tell that the hull is too small to earn. The exclusion is the only thing
// standing between the fallback and buying a probe to fly a freight lane.
func TestTradeHullPreferenceOrderExcludesNonFreightHulls(t *testing.T) {
	for _, notFreight := range []string{
		"SHIP_LIGHT_SHUTTLE", "SHIP_PROBE", "SHIP_MINING_DRONE",
		"SHIP_SIPHON_DRONE", "SHIP_SURVEYOR", "SHIP_EXPLORER",
	} {
		for _, t2 := range TradeHullPreferenceOrder {
			if t2 == notFreight {
				t.Fatalf("%s is in TradeHullPreferenceOrder — it carries no useful freight and the rate guards cannot catch an undersized substitute", notFreight)
			}
		}
	}
}

// TestTradeFallbackHullIsNeverCountedAsHeavy ties the fallback to the census's honesty.
// HeaviesOwned bounds CAPITAL EXPOSURE in large hulls and feeds HeavyCap; the hull the
// fallback actually buys today is a light freighter, and counting it as heavy would both
// overstate that exposure and close the heavy capability against hulls that are not
// heavies. Verified live: the fleet holds 13 light freighters and the census reads 0.
func TestTradeFallbackHullIsNeverCountedAsHeavy(t *testing.T) {
	heavy, unrecognised := IsHeavyHull(lightFreightClass.FrameSymbol, 80)
	if heavy {
		t.Fatalf("IsHeavyHull(%q, 80) = heavy, want NOT heavy — the fallback hull must not inflate the heavy census", lightFreightClass.FrameSymbol)
	}
	if unrecognised {
		t.Fatalf("IsHeavyHull(%q, 80) flagged an unrecognised frame — the light freighter is a known, observed class", lightFreightClass.FrameSymbol)
	}
}
