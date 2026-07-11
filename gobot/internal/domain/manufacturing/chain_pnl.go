package manufacturing

// Chain P&L raw ledger types (sp-rh2z, analyst redesign C2). These are the raw per-good
// realized cashflow aggregates the chain kill-switch judges, read straight from the ledger.
// They live in the domain package (not the application service that computes on them) for the
// same reason market.MarketPriceHistory does: the persistence adapter that reads them must
// produce them WITHOUT importing the application service — otherwise the adapter, which is
// imported by the tests of packages the service transitively depends on, forms an import
// cycle. The application interface (ChainPnLReader) is satisfied structurally against these
// domain types, exactly as GormMarketPriceHistoryRepository satisfies InputPriceHistoryReader.
//
// Signs match the transactions table: spend negative, income positive.

// ChainGoodFlow is one good's raw realized cashflow over the window.
type ChainGoodFlow struct {
	Good string
	// FactoryCost is SUM(amount) FILTER (transaction_type='PURCHASE_CARGO') over
	// operation_type IN ('manufacturing','factory_workflow') for this good_symbol — NEGATIVE
	// (spend). Input buys are tagged with the input's OWN symbol (sp-i0hl atomic attribution),
	// never rolled up to the output good the schema has no linkage for.
	FactoryCost int
	// FactorySell is SUM(amount) FILTER (transaction_type='SELL_CARGO') for this good_symbol —
	// POSITIVE (income). The factory's own local sells of the good.
	FactorySell int
	// TourNet is the tour/manual realized net for this good from tour_leg_telemetry:
	// SUM(sign(is_buy) * realized_units * realized_unit_price) — SIGNED (sells +, buys −).
	TourNet int
}

// ChainPnLRaw is the whole operation's realized cashflow over the window: the per-good flows
// plus the single refuel pool. Refuel rows carry no good_symbol so they cannot be grouped by
// good — the pool is attributed per-good by the application service (proportional to input
// spend, a documented approximation).
type ChainPnLRaw struct {
	Goods []ChainGoodFlow
	// RefuelPool is SUM(amount) FILTER (transaction_type='REFUEL') over operation_type IN
	// ('manufacturing','factory_workflow') across the window — NEGATIVE (spend).
	RefuelPool int
}
