// Package parkedsensing plans the parked-probe sensing model (sp-k6v8z): which
// systems are worth watching, and which waypoints inside them earn a standing
// probe.
package parkedsensing

// The canonical state vocabulary of the parked-probe sensing ledger. These are
// the exact strings the persistence layer stores in sensing_systems.verdict,
// sensing_slots.slot_kind and sensing_slots.state.
//
// They live here, exported, because the database does NOT validate them: the
// columns are plain sized strings. A typo'd state is therefore not a loud error
// but a silent one — a slot written as "PARKED " or "IN-TRANSIT" simply stops
// matching the probe-cap count (which selects on BOUGHT|IN_TRANSIT|PARKED),
// under-reporting how many hulls we own and authorising the purchase of probes
// we already paid for. That is a money-unsafe direction, so every producer and
// consumer of these strings imports them from here rather than spelling them.
const (
	// VerdictPending is a system not yet judged: still charting, or charted with
	// no whitelisted market found YET.
	VerdictPending = "PENDING"
	// VerdictInScope is a system with at least one KNOWN whitelisted market —
	// place slots here.
	VerdictInScope = "IN_SCOPE"
	// VerdictNoWhitelist is a fully charted system whose markets deal in nothing
	// we want. Screened and rejected.
	VerdictNoWhitelist = "NO_WHITELIST"
)

const (
	// SlotKindMarket is a market we want watched.
	SlotKindMarket = "MARKET"
	// SlotKindYard is a shipyard we want watched.
	//
	// A slot's KIND is not a reliable index of what a waypoint is. When a
	// probe-selling yard is also a whitelisted market it is slotted as MARKET,
	// because that kind carries the goods list and YARD does not — so a yard we
	// are watching may hold a slot of either kind. Anything asking "is a probe
	// present at this yard?" must therefore match on waypoint + PARKED state and
	// ignore slot_kind; a kind-filtered lookup would miss a probe already parked
	// there and authorise buying a second one for the same waypoint.
	SlotKindYard = "YARD"
	// SlotKindSpare is a parked reserve hull — still a probe we paid for.
	SlotKindSpare = "SPARE"
)

// The charting-seed lifecycle, stored in sensing_systems.seed_state. A seed is
// one probe on a one-off errand: fly to a system with uncharted waypoints, chart
// them, and then either fill a placement there or move on.
//
// The seed's TARGET is the row it is written on — sensing_systems has no target
// column, so "this system's row names a hull" IS the mission. Retargeting a seed
// is therefore two writes (clear the old row, stamp the new one) and never one.
//
// A hull on an errand is named by its system row and by NO slot row, so it is
// invisible to the probe-cap count for the length of the errand. That undercount
// is accepted deliberately and is bounded — see the DeleteSlot call site in
// expansion.go for why it is the safe direction and how it heals.
const (
	// SeedStateDispatched is a seed that has not reached its target system yet.
	SeedStateDispatched = "DISPATCHED"
	// SeedStateCharting is a seed working through its target's uncharted
	// waypoints.
	SeedStateCharting = "CHARTING"
	// SeedStateDone is a seed that has finished and been stood down. It is
	// terminal: the expansion engine never acts on a DONE row again.
	SeedStateDone = "DONE"
)

// The slot lifecycle. WANTED and QUEUED are intents with no hull behind them;
// from BOUGHT onwards a hull exists and counts against the probe cap.
const (
	// SlotStateWanted is a placement we want but have not yet acted on.
	SlotStateWanted = "WANTED"
	// SlotStateQueued is a placement chosen for purchase.
	SlotStateQueued = "QUEUED"
	// SlotStateBought is a placement whose hull has been paid for.
	SlotStateBought = "BOUGHT"
	// SlotStateInTransit is a placement whose hull is flying to the waypoint.
	SlotStateInTransit = "IN_TRANSIT"
	// SlotStateParked is a placement whose hull is on station and scanning.
	SlotStateParked = "PARKED"
)
