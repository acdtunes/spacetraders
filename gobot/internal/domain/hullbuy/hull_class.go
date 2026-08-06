// Package hullbuy holds the hull-purchase vocabulary: which POOL a purchase is for (HullClass),
// which fleet the bought hull lands DEDICATED to (DedicatedFleet), the order/result that cross the
// buy port, and the yard price read an order is sized from.
//
// It is owned by NO coordinator, which is the whole point. The fleet autosizer and the dedicated
// contract scaler both buy through this vocabulary, so it sits BENEATH both: retiring one cannot
// take the classes, the dedicate-at-purchase policy or the order/result shapes with it. Pure values
// plus one policy switch — no database, no mediator, no API — so any layer may import it.
package hullbuy

// HullClass identifies a hull pool a purchase can be made for: what the hull is bought TO BE, which
// is what fixes its dedicate-at-purchase tag. The demand model, ceiling and ship type a class also
// carries are the buying coordinator's business, not the vocabulary's.
type HullClass string

const (
	// HullClassLight is the factory-worker pool (HAULER role).
	HullClassLight HullClass = "light"
	// HullClassHeavy is the trade-tour pool (DedicatedFleet "trade").
	HullClassHeavy HullClass = "heavy"
	// HullClassExplorer is the off-gate warp-exploration pool (DedicatedFleet "explorer"). An
	// explorer buys REACH — it charts new systems so the cheap probe frontier resumes — not income.
	HullClassExplorer HullClass = "explorer"
	// HullClassContractDelivery is the contract-delivery capital pool: hulls stamped EXCLUSIVE to
	// the "contract" fleet at purchase, which is what killed the churn class the shared reuse pool
	// created.
	HullClassContractDelivery HullClass = "contract_delivery"
)

// DedicatedFleet maps a hull class to the permanent dedicated-fleet tag a bought hull is stamped
// with IN THE SAME BREATH as the purchase, before any coordinator tick can see an undedicated idle
// hull.
//
// THE ASYMMETRY IS LOAD-BEARING, and it is the reason this mapping is a named policy rather than an
// argument at the call site. Heavy, explorer and contract hulls MUST be tagged at purchase so no
// coordinator poaches them before they reach their role (the 3-of-5-absorbed lesson). Lights get NO
// tag deliberately: a light hauler IS a HAULER worker the moment it is bought, and being adopted by
// a factory chain — or re-dedicated by the depot grower — is the intended outcome, not the
// absorption hazard. A hull that lands tagged when it should be untagged, or the reverse, is a
// fleet-assignment bug that surfaces far from the buy.
//
// An unknown class is untagged, the same as a light: the caller is buying a plain worker.
func DedicatedFleet(class HullClass) string {
	switch class {
	case HullClassHeavy:
		return "trade"
	case HullClassExplorer:
		return "explorer"
	case HullClassContractDelivery:
		return "contract"
	default:
		return ""
	}
}

// DefaultHeavyCap bounds CAPITAL EXPOSURE in heavy hulls, counted fleet-wide regardless of
// dedicated_fleet tag. 5 per the Admiral.
//
// It lives here because TWO readers must never disagree about it: the buyer resolving its own cap,
// and sensing's heavy reservation deciding how much treasury to withhold toward the next heavy.
// Both fall back to this value when the operator knob is unset or unreadable, so one compile-time
// source is what keeps the withholder and the spender saving toward the same cap.
const DefaultHeavyCap = 5
