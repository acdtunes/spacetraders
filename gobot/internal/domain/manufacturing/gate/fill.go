package gate

import (
	"fmt"
	"sort"
	"strings"
)

// gateMaxTranchesPerStop bounds how many trade_volume transactions one stop may perform
// on a single trip, and so bounds how much of the hold one material may take.
//
// It exists because the market exposes NO stock count. It reports a supply LEVEL and a
// per-transaction trade_volume, and nothing else — so there is no quantity to read and
// no available_supply field to consult. The supply level still gates WHETHER we buy, via
// BuyPolicy; this bounds only how much one stop can lift, so a material with a large
// outstanding bill and a small trade volume cannot monopolise a mixed trip and leave the
// other material's factory unvisited.
//
// 4 is the weakest inference in this design and is flagged as such: revisit it against
// live fill data once phase 2 runs.
const gateMaxTranchesPerStop = 4

// Skip reasons, in PRECEDENCE order. hold_full and bill_satisfied are facts independent
// of policy and therefore outrank it: reporting a met bill as "paused" would send an
// operator to tune a knob that changes nothing. hold_full is a real outcome the greedy
// loop produces that none of the spec's three named reasons describes honestly.
const (
	SkipHoldFull      = "hold_full"
	SkipBillSatisfied = "bill_satisfied"
	SkipPaused        = "paused"
	SkipNoSupply      = "no_supply"
)

// Material is one gate material as the fill planner sees it: what the site still needs,
// what the market will sell per transaction, and whether the buy policy has paused it.
//
// It deliberately carries no market quote. The caller holds the quote (it needs the
// waypoint and price to actually buy) and PROJECTS down to this type before planning, so
// the fill arithmetic cannot accidentally depend on a price or a waypoint symbol.
type Material struct {
	Good        string
	Remaining   int
	TradeVolume int
	Paused      bool
}

// Stop is one factory visit on the trip: what to buy and in how many transactions.
type Stop struct {
	Good     string
	Units    int
	Tranches int
}

// Skip is one material the trip did NOT load, and why. This is half the point of the
// type: a trip that loaded one material out of two must be able to say which it left and
// for what reason, or a paused fleet and a finished one look identical.
type Skip struct {
	Good   string
	Reason string
}

// Trip is one hull's planned mixed load.
type Trip struct {
	Capacity int
	Stops    []Stop
	Skips    []Skip
}

// Loaded is the total units the trip plans to carry.
func (t Trip) Loaded() int {
	total := 0
	for _, s := range t.Stops {
		total += s.Units
	}
	return total
}

// LogLine renders the whole fill outcome for the container log: capacity, what was
// loaded, and what was skipped with its reason. All in the MESSAGE — the container log
// renderer drops metadata maps, so a trip reporting itself only in metadata is as
// invisible as one that said nothing.
func (t Trip) LogLine() string {
	loaded := make([]string, 0, len(t.Stops))
	for _, s := range t.Stops {
		loaded = append(loaded, fmt.Sprintf("%s x%d (%d tranche(s))", s.Good, s.Units, s.Tranches))
	}
	skipped := make([]string, 0, len(t.Skips))
	for _, s := range t.Skips {
		skipped = append(skipped, fmt.Sprintf("%s: %s", s.Good, s.Reason))
	}

	loadedText := "nothing"
	if len(loaded) > 0 {
		loadedText = strings.Join(loaded, ", ")
	}
	if len(skipped) == 0 {
		return fmt.Sprintf("Gate delivery trip: %d/%d units of hold loaded — %s", t.Loaded(), t.Capacity, loadedText)
	}
	return fmt.Sprintf("Gate delivery trip: %d/%d units of hold loaded — %s; skipped %s",
		t.Loaded(), t.Capacity, loadedText, strings.Join(skipped, ", "))
}

// blockedWhileStillWanted reports whether the site STILL NEEDS this material but it cannot
// be bought right now, for a reason an operator can act on: the buy policy paused it, or
// the market will not sell it this tick.
//
// It exists to keep those reasons from being masked by hold_full. A blocked material never
// consumes capacity, so whether it is reached before or after the hold fills is decided by
// the sort's tie-break — that is, by the GOOD'S NAME when two bills are equal. Without this
// exemption a paused material reads "paused" or "hold_full" depending on how its name sorts
// against its co-located sibling, and the pause becomes invisible exactly when the fleet is
// running at full capacity, which is when an operator most needs to see it.
//
// A material whose bill is already MET is deliberately NOT exempt: neither hold_full nor
// bill_satisfied is actionable there, so the trip-level fact wins, per the stated precedence.
func blockedWhileStillWanted(m Material) bool {
	return m.Remaining > 0 && (m.Paused || m.TradeVolume <= 0)
}

// PlanFill builds the greedy max-cargo mixed load: fill from eligible factories, by
// remaining bill descending, until the hold is full.
//
// Mixed loads are the default where factories are co-located. Both terminal factories are
// typically in the same system, so one trip amortizes the expensive gate leg across both
// materials instead of paying it twice. Mixed loading is also what gives the pause rule an
// escape valve — with one material paused a single-material hull would idle, whereas a
// hull that fills from any eligible material simply loads the other and departs.
//
// Every material the trip does not load is recorded with a reason. The precedence
// (hold_full, bill_satisfied, paused, no_supply) is deliberate and is asserted by the
// package tests.
func PlanFill(capacity int, materials []Material) Trip {
	trip := Trip{Capacity: capacity}
	if len(materials) == 0 {
		return trip
	}

	// Sort a COPY: the caller reuses its slice to record per-material decisions after
	// planning, and reordering it under them would misattribute those records.
	ordered := make([]Material, len(materials))
	copy(ordered, materials)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Remaining != ordered[j].Remaining {
			return ordered[i].Remaining > ordered[j].Remaining
		}
		return ordered[i].Good < ordered[j].Good // deterministic tie-break
	})

	capacityLeft := capacity
	for _, m := range ordered {
		// <= 0, not == 0 (upheld spec objection). The take below is bounded by capacityLeft
		// so a negative is unreachable, but writing the guard as an equality would be a claim
		// about the arithmetic rather than about the hold, and the next edit could make it false.
		//
		// A material that is still wanted but blocked by policy or by the market is exempt: it
		// could never have consumed the hold, so a full hold is not the reason it was left, and
		// reporting hold_full would hide the one fact an operator can act on. See
		// blockedWhileStillWanted.
		if capacityLeft <= 0 && !blockedWhileStillWanted(m) {
			trip.Skips = append(trip.Skips, Skip{Good: m.Good, Reason: SkipHoldFull})
			continue
		}
		if m.Remaining <= 0 {
			trip.Skips = append(trip.Skips, Skip{Good: m.Good, Reason: SkipBillSatisfied})
			continue
		}
		if m.Paused {
			trip.Skips = append(trip.Skips, Skip{Good: m.Good, Reason: SkipPaused})
			continue
		}
		if m.TradeVolume <= 0 {
			// Nothing buyable per transaction: not a zero-unit stop (which would be a trip leg
			// with no purpose, and a divide by zero in the tranche count).
			trip.Skips = append(trip.Skips, Skip{Good: m.Good, Reason: SkipNoSupply})
			continue
		}

		take := min(m.Remaining, capacityLeft, m.TradeVolume*gateMaxTranchesPerStop)
		if take <= 0 {
			trip.Skips = append(trip.Skips, Skip{Good: m.Good, Reason: SkipNoSupply})
			continue
		}

		trip.Stops = append(trip.Stops, Stop{
			Good:     m.Good,
			Units:    take,
			Tranches: (take + m.TradeVolume - 1) / m.TradeVolume, // ceil: a remainder is its own transaction
		})
		capacityLeft -= take
	}
	return trip
}
