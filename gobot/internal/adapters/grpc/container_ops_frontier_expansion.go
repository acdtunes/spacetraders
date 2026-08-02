package grpc

import (
	"context"
	"fmt"
)

// FrontierExpansionCoordinator is RETIRED: the probe-sensing coordinator
// (probe_sensing_coordinator, boot-standing) owns discovery and the probe
// budget now, and two engines declaring posts against the same budget would
// fight. The verb is kept so the gRPC surface answers honestly instead of
// vanishing; the coordinator's source stays in the tree pending era-5 proof.
// Its container type is no longer in the command registry, so a still-RUNNING
// legacy container fails closed at restart recovery ("unknown command type").
func (s *DaemonServer) FrontierExpansionCoordinator(ctx context.Context, playerID int) (string, error) {
	return "", fmt.Errorf("the frontier expansion coordinator is retired: the probe-sensing coordinator (boot-standing) owns discovery — operate it via `tune --operation sensing`")
}
