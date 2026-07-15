// Package buffer holds the SINGLE SOURCE OF TRUTH for deciding WHICH goods a hub's
// contract-goods warehouse buffer may stock (bead sp-rxrg). Both the LIVE destination-depot
// warehouse-cap selector (adapters/grpc depotWarehouseTargetUnits -> PlanReceiptCaps) and the
// dormant reconciler planner (domain/capacity selectBufferGoods) route their candidates through
// this ONE Gate before their own reward / stall-prevention ranking, so the three candidate gates
// can never drift between the two paths.
package buffer

// Gate is the reward-ranking-INDEPENDENT admission policy applied in FRONT of a hub's buffer
// selection. It carries only the one tunable the three gates need — the source-distance floor —
// so it stays a pure value with no I/O.
type Gate struct {
	// MinExternalSourceDistance is the gate-3 floor: a good whose nearest EXTERNAL source sits
	// at or below this distance is too near to be worth a warehouse slot (a homed hauler buys it
	// almost as cheaply as the buffer would pre-stage it).
	MinExternalSourceDistance float64
}

// Facts are the three per-(good, hub) inputs the gates decide on. A caller normalizes its own
// data model into these before asking Admits — that normalization is the ONLY per-path code; the
// decision itself is shared.
type Facts struct {
	// Good is the trade symbol (for diagnostics; the decision never depends on it).
	Good string
	// HubContractFrequency is how often the good is delivered TO THIS hub by contracts (gate 1).
	// > 0 means it is a hub contract good; 0 means never contracted to this hub.
	HubContractFrequency float64
	// HubProducesLocally is true when the hub's OWN market EXPORTS or EXCHANGES the good (gate 2):
	// the delivery hull buys it on-site, so warehousing it wastes the slot.
	HubProducesLocally bool
	// ExternalSourceDistance is the distance to the good's nearest EXTERNAL source (gate 3).
	ExternalSourceDistance float64
	// ExternalSourceDistanceKnown is false when the distance could not be resolved (an uncached /
	// TTL-expired source waypoint). Gate 3 fails OPEN on an unknown distance so a legitimately far
	// good is never wrongly excluded just because its coordinates are momentarily unavailable.
	ExternalSourceDistanceKnown bool
}

// Admits reports whether a good may be buffered at a hub — it must clear ALL THREE candidate gates.
func (g Gate) Admits(f Facts) bool {
	// Gate 1 — CONTRACT-MEMBERSHIP: only buffer a good the hub's contracts actually deliver TO it.
	if f.HubContractFrequency <= 0 {
		return false
	}
	// Gate 2 — LOCAL-PRODUCTION EXCLUSION: never buffer a good the hub's own market makes; the
	// delivery hull buys it on-site instantly.
	if f.HubProducesLocally {
		return false
	}
	// Gate 3 — SOURCE-DISTANCE: exclude only on POSITIVE evidence the nearest external source is
	// too near to be worth a warehouse slot. An unknown distance fails OPEN (keeps the good) so an
	// uncached far good is never wrongly excluded.
	if f.ExternalSourceDistanceKnown && f.ExternalSourceDistance <= g.MinExternalSourceDistance {
		return false
	}
	return true
}
