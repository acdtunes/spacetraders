package capacity

// DesiredTopology is the PLAN phase's output: the capacity topology the
// contract-delivery machine SHOULD have right now, recomputed from live
// Signals every tick (spec: PLAN). The heuristic planner lane computes it;
// the DIFF lane compares it against TopologySignals.Clusters.
//
// An empty DesiredTopology (zero hubs) is the valid "want nothing" plan — the
// foundation's NoOpPlanner emits it, and DIFF over it must produce zero
// actions.
type DesiredTopology struct {
	Hubs []DesiredHub
}

// IsEmpty reports whether the plan wants no capacity at all.
func (t DesiredTopology) IsEmpty() bool {
	return len(t.Hubs) == 0
}

// DesiredHub is one covered hub with its buffered-good whitelist + caps and
// its warehouse/stocker/worker counts + positions (spec PLAN output fields).
type DesiredHub struct {
	// HubSymbol is the covered hub's waypoint symbol.
	HubSymbol string

	// BufferedGoods is the buffer whitelist for this hub with per-good caps
	// (cap ≈ avg_units + margin — an uncapped whitelist over-fills the first
	// good and starves the rest).
	BufferedGoods []DesiredBufferedGood

	// Warehouse/stocker/worker counts, sized to buffered volume + restock
	// cadence (restock_throughput ≥ consumption_rate).
	WarehouseCount int
	StockerCount   int
	WorkerCount    int

	// Positions. Defaults to the hub itself when empty — co-location is the
	// cycle-time lever. (Stockers roam to sources; StockerWaypoint is their
	// home/anchor position.)
	WarehouseWaypoint string
	StockerWaypoint   string
	WorkerWaypoint    string
}

// DesiredBufferedGood is one whitelisted buffer good and its unit cap.
type DesiredBufferedGood struct {
	Good     string
	UnitsCap int
}
