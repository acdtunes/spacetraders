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
