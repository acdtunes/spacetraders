package services

import (
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// DeliveryExecutorOption configures optional collaborators without breaking the
// positional constructor the existing tests use.
type DeliveryExecutorOption func(*DeliveryExecutor)

// WithInventorySource enables inventory-first contract sourcing (sp-dchv Lane D):
// before each market buy the executor withdraws the good from an in-system
// warehouse at zero ask when one holds it. A nil finder is a no-op (market-only),
// so callers may forward optional wiring unconditionally.
func WithInventorySource(finder appContract.InventorySourceFinder, coordinator storage.StorageCoordinator, apiClient domainPorts.APIClient) DeliveryExecutorOption {
	return func(e *DeliveryExecutor) {
		e.invFinder = finder
		e.storageCoordinator = coordinator
		e.apiClient = apiClient
	}
}

// WithWithdrawalRecorder wires the warehouse-withdrawal event recorder:
// on each successful warehouse→hauler buffer draw the executor emits a structured
// storage.WithdrawalEvent (good, units, waypoint, hauler, contract id, timestamp)
// so downstream analysis can measure warehouse ROI. A nil recorder is a no-op, so
// callers may forward the wiring unconditionally. The clock stamps the event's
// WithdrawnAt; a nil clock defaults to shared.RealClock.
func WithWithdrawalRecorder(recorder storage.WithdrawalRecorder, clock shared.Clock) DeliveryExecutorOption {
	return func(e *DeliveryExecutor) {
		e.withdrawalRecorder = recorder
		if clock == nil {
			clock = shared.NewRealClock()
		}
		e.withdrawalClock = clock
	}
}

// WithSourceBuyFloor arms the proactive working-capital reserve floor on the market
// source-buy (sp-zq635 §4b): before a buy the executor reads live treasury and HOLDS
// (parks, resuming when treasury recovers) any buy that would drop it below the flat,
// immutable reserve floor (common.ImmutableReserveFloor).
// Fail-closed: an unreadable treasury parks the buy. A DeliveryExecutor built without
// this option has reactive 4600 handling only.
func WithSourceBuyFloor() DeliveryExecutorOption {
	return func(e *DeliveryExecutor) {
		e.enforceSourceBuyFloor = true
	}
}

// WithConcurrentSpendCap wires the CROSS-OPERATION concurrent spend cap onto the contract
// source-buy (sp-ps2oc acceptance 4).
//
// WithSourceBuyFloor above is a PER-BUY guard: it reads live treasury and sizes this lot
// against the reserve. It cannot see a sibling's spend that has not committed yet, and a
// contract hauler does not buy alone — construction_supply, contract and tour all draw on ONE
// treasury (in the incident window, PURCHASE_CARGO totalled -761,919 against SELL_CARGO
// +326,938 with no arbitration between them). Capping construction against itself would leave
// a contract buy free to race the same float.
//
// The ledger passed here must be THE SAME one the construction executor holds. Two ledgers are
// two budgets, and the aggregate breach simply reappears one level up.
//
// A nil ledger is a no-op, so callers may forward optional wiring unconditionally and every
// existing test constructing an executor without it is byte-identical.
func WithConcurrentSpendCap(ledger ConcurrentSpendLedger) DeliveryExecutorOption {
	return func(e *DeliveryExecutor) {
		e.spendLedger = ledger
	}
}
