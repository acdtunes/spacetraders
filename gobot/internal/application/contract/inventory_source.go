package contract

import "context"

// Inventory-first contract sourcing (sp-dchv Lane D).
//
// The parallel trade engine pre-positions cheap cross-system goods into a HOME
// warehouse (sp-dchv Lanes A-C). This file adds the CONTRACT side: a seam the
// sourcing optimizer consults BEFORE the market candidate so a contract whose
// delivery system holds the good in inventory withdraws it at zero marginal ask
// instead of buying it. Withdrawal is a single-system, ship-to-ship transfer at
// the warehouse waypoint (RULINGS #14) — the contract worker never leaves its
// system and never claims the warehouse hull (RULINGS #7); it only transfers.

// SourcingSource marks where a SourcingPlan sources its good from.
type SourcingSource string

const (
	// SourceMarket is the default: buy the good at a market waypoint at UnitAsk.
	SourceMarket SourcingSource = "MARKET"

	// SourceInventory sources the good from an in-system warehouse at zero
	// marginal ask — the units were already paid for by the trade engine's
	// deposit, so the cost basis is sunk (sp-dchv). The plan's Market is the
	// storage waypoint and UnitAsk is 0; the contract worker WITHDRAWS via a
	// ship-to-ship transfer rather than a market purchase.
	SourceInventory SourcingSource = "INVENTORY"
)

// InventorySource describes an in-system warehouse holding a contract good that
// a contract worker can withdraw from instead of buying at market. Every field
// is a primitive so package contract needs NO dependency on the storage domain —
// the concrete finder (which does depend on storage) lives in the services layer
// and satisfies InventorySourceFinder.
type InventorySource struct {
	// OperationID is the warehouse storage operation to withdraw from (the
	// coordinator key the executor reserves + transfers against).
	OperationID string

	// StorageWaypoint is where the warehouse hull is parked — in the delivery
	// system (RULINGS #14). The worker flies here in-system to withdraw.
	StorageWaypoint string

	// UnitsAvailable is the unreserved units of the good held as of the read. It
	// is a SNAPSHOT: the executor re-reads and reserves atomically at withdrawal
	// time, so a positive value only means "inventory-first is worth trying", it
	// is never a guarantee (another worker may drain it first — that falls
	// through to the market path, never a skip).
	UnitsAvailable int
}

// InventorySourceFinder locates in-system warehouse inventory for a contract
// good. It is the seam sp-dchv Lane D slots ahead of the market candidate in the
// sourcing optimizer, and that a contract worker re-consults at withdrawal time.
//
// Fail-open contract (RULINGS #1, never-skip): an implementation returns nil for
// "no inventory here" for ANY reason — no warehouse in the system, none holding
// the good, zero unreserved units, a dead warehouse container, OR any read
// error. A nil finder (feature not wired) is likewise "no inventory". Callers
// therefore fall through to the pre-existing market path unchanged; inventory
// only ever ADDS a cheaper source, it never parks or skips a contract.
type InventorySourceFinder interface {
	FindInSystemInventory(ctx context.Context, playerID int, systemSymbol, good string) *InventorySource
}

// SourcingOption configures optional inputs to PlanSourcing / PlanDeliverySourcing
// without breaking the existing positional callers — they pass no options and get
// byte-identical market-only behavior (the sp-9hu8 pins and the defer math are
// unaffected).
type SourcingOption func(*sourcingConfig)

type sourcingConfig struct {
	inventory InventorySourceFinder
}

// WithInventoryFinder enables inventory-first sourcing: the planner consults the
// finder for in-system warehouse stock BEFORE the market candidate and, when
// stock exists, emits a zero-ask INVENTORY plan. A nil finder is a no-op
// (market-only), so callers may pass WithInventoryFinder(maybeNil) unconditionally.
func WithInventoryFinder(f InventorySourceFinder) SourcingOption {
	return func(c *sourcingConfig) { c.inventory = f }
}

func newSourcingConfig(opts []SourcingOption) sourcingConfig {
	var c sourcingConfig
	for _, opt := range opts {
		opt(&c)
	}
	return c
}
