package commands

import (
	"context"
	"reflect"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	storageApp "github.com/andrescamacho/spacetraders-go/internal/application/storage"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// gasWorkerOpCtxMediator records the operation_type carried on the ctx of the
// positioning NavigateRouteCommand each gas worker fires to reach the gas giant.
// That navigate is the refuel carrier (route_executor propagates its ctx verbatim to
// every RefuelShipCommand), so its ctx decides whether the worker's positioning
// refuels attribute to the operation or fall through to operation_type='manual'
// (sp-zc8i). It returns a NavigateRouteResponse carrying the same ship the repo
// served so the workers' `ship = navResp.Ship` post-navigate step stays non-nil.
type gasWorkerOpCtxMediator struct {
	ship      *navigation.Ship
	sawNav    bool
	navOpType string
}

// capturedGasOpType is what the cargo/refuel ledger recorders read for a command
// dispatched under ctx (shared.OperationContextFromContext(...).NormalizedOperationType()),
// or "" when no operation context rides the ctx (the 'manual' fallback).
func capturedGasOpType(ctx context.Context) string {
	if oc := shared.OperationContextFromContext(ctx); oc != nil {
		return oc.NormalizedOperationType()
	}
	return ""
}

func (m *gasWorkerOpCtxMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch request.(type) {
	case *shipNav.NavigateRouteCommand:
		m.sawNav = true
		m.navOpType = capturedGasOpType(ctx)
		return &shipNav.NavigateRouteResponse{Ship: m.ship}, nil
	default:
		return nil, nil // siphon/transfer/jettison succeed silently; not reached once ctx is cancelled
	}
}

func (m *gasWorkerOpCtxMediator) Register(reflect.Type, common.RequestHandler) error { return nil }
func (m *gasWorkerOpCtxMediator) RegisterMiddleware(common.Middleware)               {}

// preCancelledCtx returns a context already cancelled so a worker's indefinite
// siphon/hold loop returns at its first ctx.Done() check — right AFTER the one-time
// positioning navigate, which is all these tests observe.
func preCancelledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// The context-less siphon positioning (sp-zc8i). The siphon worker navigates its hull
// to the gas giant on a bare ctx, so the refuels that hop fires landed
// operation_type='manual'. This asserts at the NavigateRouteCommand boundary that a
// worker bearing a CoordinatorID runs its positioning navigate — and thus the refuels
// it drives — under operation_type='gas_siphon'.
func TestRunSiphonWorker_Positioning_CarriesGasSiphonOperationContext(t *testing.T) {
	ship := newSpawnTestShip(t, "AGENT-SIPHON-OPTYPE") // parked at X1-TEST-A1, not the gas giant
	med := &gasWorkerOpCtxMediator{ship: ship}
	h := NewRunSiphonWorkerHandler(med, &spawnFakeShipRepo{ship: ship}, nil, shared.NewRealClock())

	_, _ = h.Handle(preCancelledCtx(), &RunSiphonWorkerCommand{
		ShipSymbol:         "AGENT-SIPHON-OPTYPE",
		PlayerID:           shared.MustNewPlayerID(1),
		GasGiant:           "X1-TEST-GG",
		CoordinatorID:      "gas-coordinator-1",
		StorageOperationID: "gas-op-1",
	})

	if !med.sawNav {
		t.Fatal("expected the siphon worker to navigate to the gas giant, no NavigateRouteCommand seen")
	}
	if med.navOpType != "gas_siphon" {
		t.Errorf("positioning navigate (the refuel carrier) ran under operation_type=%q, want \"gas_siphon\" (was the 'manual' leak)", med.navOpType)
	}
}

// The context-less storage positioning (sp-zc8i). The storage-ship worker likewise
// navigates its hull to the gas giant on a bare ctx before registering as a buffer, so
// that hop's refuels landed 'manual'. This asserts the positioning navigate runs under
// operation_type='storage'.
func TestRunStorageShipWorker_Positioning_CarriesStorageOperationContext(t *testing.T) {
	ship := newSpawnTestShip(t, "AGENT-STORAGE-OPTYPE") // parked at X1-TEST-A1, not the gas giant
	med := &gasWorkerOpCtxMediator{ship: ship}
	h := NewRunStorageShipWorkerHandler(med, &spawnFakeShipRepo{ship: ship}, storageApp.NewInMemoryStorageCoordinator())

	_, _ = h.Handle(preCancelledCtx(), &RunStorageShipWorkerCommand{
		ShipSymbol:         "AGENT-STORAGE-OPTYPE",
		PlayerID:           shared.MustNewPlayerID(1),
		GasGiant:           "X1-TEST-GG",
		CoordinatorID:      "gas-coordinator-1",
		StorageOperationID: "gas-op-1",
	})

	if !med.sawNav {
		t.Fatal("expected the storage worker to navigate to the gas giant, no NavigateRouteCommand seen")
	}
	if med.navOpType != "storage" {
		t.Errorf("positioning navigate (the refuel carrier) ran under operation_type=%q, want \"storage\" (was the 'manual' leak)", med.navOpType)
	}
}
