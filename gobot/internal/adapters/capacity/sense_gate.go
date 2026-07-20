package capacity

import (
	"context"
	"sort"

	domainCapacity "github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
)

// GateShortfallReader is the sensor's SECOND live-API boundary (sp-3idiw): the jump-gate
// construction site's REMAINING haul demand (required − fulfilled per material). Gate
// progress is API-only — the daemon DB the DB-sensor reads does not hold construction
// required/fulfilled — so, like TreasuryReader, it is a live touch. A nil reader
// (unwired) yields NO gate demand: the reconciler is byte-identical to its contract-only
// self and the gate depot is emergency-disabled. Production satisfies it with
// GateShortfallAPIReader; tests double it.
type GateShortfallReader interface {
	GateShortfall(ctx context.Context, playerID int) (GateShortfall, error)
}

// GateShortfall is one jump-gate construction site's outstanding material demand. An
// empty waypoint or no unfulfilled materials ⇒ no gate demand ⇒ the depot dissolves.
type GateShortfall struct {
	// GateWaypoint is the JUMP_GATE waypoint symbol the depot buffers toward.
	GateWaypoint string
	// Materials is the per-material remaining (required − fulfilled) shortfall.
	Materials []GateMaterialShortfall
}

// GateMaterialShortfall is one construction material's outstanding quantity.
type GateMaterialShortfall struct {
	TradeSymbol string
	Remaining   int
}

const (
	// gateSyntheticFrequencyPerHour models the gate fill as one continuous contract
	// stream: a single high-priority haul, not a recurring hub. A fixed positive cadence
	// makes the hub coverage score depend on the remaining VOLUME (the payment term)
	// rather than a fabricated frequency. Analyst-tunable (the exact curve is
	// economy-analyst-owned).
	gateSyntheticFrequencyPerHour = 1.0
	// gateValuePerRemainingUnit maps each outstanding material unit to a synthetic
	// per-contract payment, so the gate coverage score (frequency × payment) scales with
	// remaining volume and clears the planner's cold-start floor during an active fill (a
	// real gate needs thousands of units). Analyst-tunable.
	gateValuePerRemainingUnit = 1000.0
	// gateBufferWorkingSetUnits caps each material's buffered WORKING SET so the depot
	// warehouse holds a haulable lot (stockers continuously refill it), not the entire
	// outstanding order — the total remaining drives the hub RANK, the working set drives
	// buffer/count sizing. One freighter-class hold. Analyst-tunable.
	gateBufferWorkingSetUnits = 120.0
)

// senseGateDemand reads the live jump-gate shortfall and shapes it as gate demand. Nil
// reader (unwired) or a read error ⇒ no gate demand (byte-identical / fail-closed): a
// live-API hiccup degrades the gate signal, logged, but never blocks the tick nor
// fabricates a depot.
func (s *Sensor) senseGateDemand(ctx context.Context, playerID int) []domainCapacity.HubDemand {
	if s.gateShortfall == nil {
		return nil
	}
	shortfall, err := s.gateShortfall.GateShortfall(ctx, playerID)
	if err != nil {
		s.note("demand.gate", err)
		return nil
	}
	return gateHubDemand(shortfall)
}

// gateHubDemand shapes a gate construction shortfall into a single gate HubDemand. The
// hub RANK (frequency × payment) encodes the TOTAL remaining volume, so the gate ranks
// against recurring contract hubs during the fill and collapses to nothing as the fill
// completes (no unfulfilled materials ⇒ nil hub ⇒ the depot dissolves via the existing
// shrink path). Each material is buffered as a bounded working-set lot (AvgUnits), the
// buffer scorer's per-good distance reward supplies the source-haul compression value —
// so gate value ≈ remaining-units × source-distance across the two existing scorers.
func gateHubDemand(shortfall GateShortfall) []domainCapacity.HubDemand {
	if shortfall.GateWaypoint == "" {
		return nil
	}
	totalRemaining := 0
	goodMix := make([]domainCapacity.GoodDemand, 0, len(shortfall.Materials))
	for _, mat := range shortfall.Materials {
		if mat.Remaining <= 0 || mat.TradeSymbol == "" {
			continue
		}
		totalRemaining += mat.Remaining
		avgUnits := float64(mat.Remaining)
		if avgUnits > gateBufferWorkingSetUnits {
			avgUnits = gateBufferWorkingSetUnits
		}
		goodMix = append(goodMix, domainCapacity.GoodDemand{
			Good:      mat.TradeSymbol,
			Frequency: gateSyntheticFrequencyPerHour,
			AvgUnits:  avgUnits,
		})
	}
	if totalRemaining == 0 {
		return nil
	}
	sort.Slice(goodMix, func(i, j int) bool { return goodMix[i].Good < goodMix[j].Good })
	return []domainCapacity.HubDemand{{
		HubSymbol:         shortfall.GateWaypoint,
		Kind:              domainCapacity.HubKindGate,
		ContractFrequency: gateSyntheticFrequencyPerHour,
		AvgPaymentCredits: gateValuePerRemainingUnit * float64(totalRemaining),
		GoodMix:           goodMix,
	}}
}
