package commands

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// TestBootstrap_TickStampsTheOperationItsPortsInherit is the upstream half of the
// attribution defect. Bootstrap ferries hulls — to a hub after buying one, to a shipyard to
// make its prices readable — and each flight burns fuel whose ledger row reads its operation
// off the context. The ports that fly those legs are registered singletons shared by every
// container and player, so they hold no identity to stamp with; the tick is the only place
// the running operation is known. Unstamped, the fuel books as though nobody had asked for
// it, and the cost of standing a fleet up cannot be attributed to standing it up.
//
// A port is used as the probe deliberately: asserting on the tick's own ctx would pass
// against a stamp that never reaches anything it drives.
func TestBootstrap_TickStampsTheOperationItsPortsInherit(t *testing.T) {
	obs := tradeReleaseObs(
		GateWorkerSnapshot{Symbol: "MFG-9", Idle: true},
	)
	h, rel := releasedToTrade(obs, &fakeHandoff{})

	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}

	// Calibration: with no port call there is no ctx to inspect, and every assertion below
	// would pass vacuously against a nil.
	if rel.gotCtx == nil {
		t.Fatal("no port was driven this tick, so nothing inherited a context and this proves nothing")
	}

	opCtx := shared.OperationContextFromContext(rel.gotCtx)
	if opCtx == nil {
		t.Fatal("the tick drove a port with no operation context, so everything it spends books as unpropagated")
	}
	if !opCtx.IsValid() {
		t.Fatalf("operation context incomplete %+v - the readers require both halves, so a partial one attributes nothing", opCtx)
	}
	if opCtx.ContainerID != baseCmd().ContainerID {
		t.Fatalf("stamped container %q, want the tick's own %q", opCtx.ContainerID, baseCmd().ContainerID)
	}
	if opCtx.OperationType != bootstrapOperationType {
		t.Fatalf("stamped operation %q, want %q", opCtx.OperationType, bootstrapOperationType)
	}
}
