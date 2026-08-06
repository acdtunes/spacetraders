package commands

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	contractServices "github.com/andrescamacho/spacetraders-go/internal/application/contract/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// TestCoordinator_HomingCarriesTheOperationAcrossTheDetachedGoroutine is the half of the
// attribution defect that a stamp at the coordinator alone does NOT fix.
//
// Homing is dispatched from a goroutine that deliberately starts from a background context so
// the flight outlives the tick that scheduled it. Cancellation is what must not cross that
// boundary — but the operation the work belongs to was being dropped with it, so the fuel the
// homing flight burns booked as though nobody had asked for it. Stamping the coordinator's
// entry point looks like it fixes this and does not: the value never reaches the dispatch.
func TestCoordinator_HomingCarriesTheOperationAcrossTheDetachedGoroutine(t *testing.T) {
	completed := newHomeTestShip(t, "TORWIND-5", "X1-UM5-Z", 0, 0)
	shipRepo := &homeStubShipRepo{ship: completed, fleet: []*navigation.Ship{completed}}
	got := make(chan *HomeShipCommand, 1)
	gotCtx := make(chan context.Context, 1)
	med := &recordingHomeMediator{got: got, gotCtx: gotCtx}
	provider := &stubPlacementProvider{slots: []string{"X1-UM5-G49", "X1-UM5-K83"}}

	handler := newImmediateHomingHandler(med, shipRepo, provider)
	cmd := &RunFleetCoordinatorCommand{PlayerID: shared.MustNewPlayerID(1), ContainerID: "coord-1"}

	// The tick's own context, stamped exactly as the coordinator's entry point stamps it.
	ctx := shared.WithOperationContext(context.Background(),
		shared.NewOperationContext(cmd.ContainerID, fleetCoordinatorOperationType))

	handler.homeCompletedHullToStandby(ctx, cmd, "TORWIND-5")

	select {
	case dispatchedCtx := <-gotCtx:
		// Calibration: without a dispatch there is no context to inspect and the assertions
		// below would pass against a nil for the wrong reason.
		if dispatchedCtx == nil {
			t.Fatal("homing dispatched a nil context, so nothing was captured and this proves nothing")
		}
		opCtx := shared.OperationContextFromContext(dispatchedCtx)
		if opCtx == nil {
			t.Fatal("the homing flight was dispatched with no operation context - the goroutine dropped it, so its fuel books as unpropagated")
		}
		if !opCtx.IsValid() {
			t.Fatalf("operation context incomplete %+v - the readers need both halves", opCtx)
		}
		if opCtx.ContainerID != cmd.ContainerID {
			t.Fatalf("homing booked under container %q, want the coordinator's own %q", opCtx.ContainerID, cmd.ContainerID)
		}
		if opCtx.OperationType != fleetCoordinatorOperationType {
			t.Fatalf("homing booked under operation %q, want %q", opCtx.OperationType, fleetCoordinatorOperationType)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no homing command was dispatched, so the fixture never exercised the path under test")
	}
	_ = appContract.StandbyPlacementProvider(provider)
}

// seedCtxMarker captures the context the tick's first ctx-taking collaborator receives, which
// is what makes the entry-point stamp observable without driving the whole coordinator.
type seedCtxMarker struct{ got context.Context }

func (m *seedCtxMarker) MarkDedicatedShipsSeeded(ctx context.Context, _ string, _ int) error {
	m.got = ctx
	return nil
}

// TestCoordinator_TickStampsTheOperationItsWorkInherits pins the other half. The propagation
// test above hands homing an already-stamped context, so on its own it would still pass if the
// coordinator never stamped one — the two together are what make the path attributable end to
// end.
//
// The tick is driven only as far as its first collaborator; it is expected to stop shortly
// after on unwired dependencies, which is irrelevant to what is being observed here.
func TestCoordinator_TickStampsTheOperationItsWorkInherits(t *testing.T) {
	marker := &seedCtxMarker{}
	handler := &RunFleetCoordinatorHandler{
		fleetPoolManager: contractServices.NewFleetPoolManager(&recordingHomeMediator{got: make(chan *HomeShipCommand, 1)}),
		seedMarker:       marker,
	}
	cmd := &RunFleetCoordinatorCommand{
		PlayerID:       shared.MustNewPlayerID(1),
		ContainerID:    "coord-7",
		DedicatedShips: []string{"TORWIND-1"},
	}

	_, _ = handler.Handle(context.Background(), cmd)

	// Calibration: no collaborator call means no context to inspect, and every assertion below
	// would pass vacuously against a nil.
	if marker.got == nil {
		t.Fatal("the tick never reached a ctx-taking collaborator, so this proves nothing about the stamp")
	}
	opCtx := shared.OperationContextFromContext(marker.got)
	if opCtx == nil {
		t.Fatal("the tick ran with no operation context, so everything it drives books as unpropagated")
	}
	if opCtx.ContainerID != cmd.ContainerID {
		t.Fatalf("stamped container %q, want the tick's own %q", opCtx.ContainerID, cmd.ContainerID)
	}
	if opCtx.OperationType != fleetCoordinatorOperationType {
		t.Fatalf("stamped operation %q, want %q", opCtx.OperationType, fleetCoordinatorOperationType)
	}
}

// repositionCtxMediator captures the dispatch context for EITHER command repositioning can
// send, so one fixture covers both of its branches.
type repositionCtxMediator struct {
	common.Mediator
	gotCtx chan context.Context
}

func (m *repositionCtxMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch request.(type) {
	case *HomeShipCommand:
		m.gotCtx <- ctx
		return &HomeShipResponse{}, nil
	case *BalanceShipPositionCommand:
		m.gotCtx <- ctx
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected mediator command in test: %T", request)
}

// TestCoordinator_RepositioningCarriesTheOperationOnBothBranches covers the other two detached
// goroutines. Repositioning the hull a selection change displaced flies it either way — homed
// to its slot if it belongs to the dedicated fleet, position-balanced if it does not — and both
// branches dispatch from their own goroutine. Covering only one leaves the other free to drop
// the operation silently, which is what the first mutation sweep found.
func TestCoordinator_RepositioningCarriesTheOperationOnBothBranches(t *testing.T) {
	for _, tc := range []struct {
		name      string
		dedicated []string
	}{
		{"dedicated hull is homed to its slot", []string{"TORWIND-5"}},
		{"undedicated hull is position-balanced", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := newHomeTestShip(t, "TORWIND-5", "X1-UM5-Z", 0, 0)
			shipRepo := &homeStubShipRepo{ship: previous, fleet: []*navigation.Ship{previous}}
			gotCtx := make(chan context.Context, 1)
			med := &repositionCtxMediator{gotCtx: gotCtx}

			handler := newImmediateHomingHandler(med, shipRepo, &stubPlacementProvider{slots: []string{"X1-UM5-G49"}})
			cmd := &RunFleetCoordinatorCommand{
				PlayerID:       shared.MustNewPlayerID(1),
				ContainerID:    "coord-1",
				DedicatedShips: tc.dedicated,
			}
			ctx := shared.WithOperationContext(context.Background(),
				shared.NewOperationContext(cmd.ContainerID, fleetCoordinatorOperationType))

			handler.repositionPreviousShip(ctx, cmd, "TORWIND-5", "TORWIND-9")

			select {
			case dispatched := <-gotCtx:
				opCtx := shared.OperationContextFromContext(dispatched)
				if opCtx == nil {
					t.Fatal("the repositioning flight was dispatched with no operation context - the goroutine dropped it, so its fuel books as unpropagated")
				}
				if opCtx.ContainerID != cmd.ContainerID || opCtx.OperationType != fleetCoordinatorOperationType {
					t.Fatalf("dispatched under %+v, want the tick's own container %q / %q", opCtx, cmd.ContainerID, fleetCoordinatorOperationType)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("nothing was dispatched, so this branch of the fixture never exercised the path under test")
			}
		})
	}
}

// TestIdleArbReHome_CarriesTheOperationAcrossItsGoroutine covers the fourth and last detached
// dispatch: the idle-arb re-home. It has its own goroutine, so it drops the operation
// independently of the coordinator's three — the sweep found it still doing so after the others
// were fixed.
func TestIdleArbReHome_CarriesTheOperationAcrossItsGoroutine(t *testing.T) {
	ship := newHomeTestShip(t, "TORWIND-5", "X1-UM5-Z", 0, 0)
	shipRepo := &homeStubShipRepo{ship: ship, fleet: []*navigation.Ship{ship}}
	gotCtx := make(chan context.Context, 1)
	med := &repositionCtxMediator{gotCtx: gotCtx}

	homer := &mediatorShipHomer{
		mediator:          med,
		shipRepo:          shipRepo,
		playerID:          shared.MustNewPlayerID(1),
		fleet:             dedicatedFleetContract,
		placementProvider: &stubPlacementProvider{slots: []string{"X1-UM5-G49"}},
	}

	ctx := shared.WithOperationContext(context.Background(),
		shared.NewOperationContext("coord-1", fleetCoordinatorOperationType))
	if err := homer.HomeShip(ctx, "TORWIND-5", nil); err != nil {
		t.Fatalf("HomeShip: %v", err)
	}

	select {
	case dispatched := <-gotCtx:
		opCtx := shared.OperationContextFromContext(dispatched)
		if opCtx == nil {
			t.Fatal("the idle-arb re-home was dispatched with no operation context - its goroutine dropped it, so the flight's fuel books as unpropagated")
		}
		if opCtx.ContainerID != "coord-1" || opCtx.OperationType != fleetCoordinatorOperationType {
			t.Fatalf("dispatched under %+v, want container %q / %q", opCtx, "coord-1", fleetCoordinatorOperationType)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nothing was dispatched, so the fixture never exercised the path under test")
	}
}
