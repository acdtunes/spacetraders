package grpc

// Duty is the standing fleet-wide responsibility a container type OWNS, held by exactly ONE owner:
// two engines answering one question bid against each other over one treasury and one API budget,
// neither seeing the other's spend. A successor REPLACES an owner, or declares the overlap below.
type Duty string

const (
	DutyUnspecified Duty = "" // never filled in: not a value, so a forgotten field is loud

	DutyNone Duty = "none" // a verb, worker or per-hull run: it executes an order someone else decided

	DutyMarketFreshness Duty = "market freshness"

	DutyTradeFleetSizing    Duty = "trade-fleet hull sizing"
	DutyContractFleetSizing Duty = "contract-fleet hull sizing"
	DutyHullOutfitting      Duty = "hull outfitting"

	DutyContractExecution   Duty = "contract execution"
	DutyTradeDispatch       Duty = "trade-tour dispatch"
	DutyLongHaulDispatch    Duty = "long-haul arb dispatch"
	DutyHullRepositioning   Duty = "idle-hull repositioning"
	DutyConstructionSupply  Duty = "construction supply"
	DutyGasSupply           Duty = "gas supply"
	DutyColdStartSequencing Duty = "cold-start sequencing"
)

// dutyOverlap is a dated, attributed exception; Types must match the duplicate EXACTLY, and a stale entry fails.
type dutyOverlap struct {
	Types      []string // the exact set sharing the duty — not a prefix, not a minimum
	Since      string
	Retirement string // the bead that ends it; an overlap with no bead is a permanent one
	Why        string
}

var declaredDutyOverlaps = map[Duty]dutyOverlap{} // duties owned by >1 type ON PURPOSE; empty is healthy
