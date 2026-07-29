package grpc

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// shipPositionReanchorObserver publishes a corrected ship position on the SAME two durable
// surfaces the coordinator stall escalator uses (internal/application/health/stall.go):
// the Prometheus series and the captain_events outbox. The WARN log is emitted by the
// repository itself, so the signal is never silent even before this is wired.
//
// It is stateless and holds no references: both surfaces resolve their globals lazily on
// every call, because the ship repository is wired before NewDaemonServer builds the
// collectors and installs the captain recorder. A captured reference would be permanently
// nil — an alarm wired to nothing, which is the exact failure mode this signal exists to
// end.
//
// Every emission is fire-and-forget. An observability write must never break the sync it
// is reporting on (RULINGS #4), and both helpers below already swallow their own failures.
type shipPositionReanchorObserver struct{}

// ShipPositionReanchored implements api.PositionReanchorObserver. It returns nothing: this
// is observation, and no decision path may ever branch on it.
func (shipPositionReanchorObserver) ShipPositionReanchored(_ context.Context, reanchor api.PositionReanchor) {
	metrics.RecordPositionReanchor(reanchor.BelievedSystem, reanchor.ActualSystem)
	recordCaptainEvent(
		captain.EventShipPositionReanchored,
		reanchor.ShipSymbol,
		reanchor.PlayerID,
		map[string]any{
			"ship_symbol":       reanchor.ShipSymbol,
			"believed_system":   reanchor.BelievedSystem,
			"actual_system":     reanchor.ActualSystem,
			"believed_waypoint": reanchor.BelievedWaypoint,
			"actual_waypoint":   reanchor.ActualWaypoint,
			"cause":             "a completed cross-system move was never persisted; every tick since planned from the believed system",
		},
	)
}
