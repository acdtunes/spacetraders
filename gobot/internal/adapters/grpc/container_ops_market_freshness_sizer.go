package grpc

import (
	"context"
	"fmt"
)

// MarketFreshnessSizerCoordinator is RETIRED: the probe-sensing coordinator
// (probe_sensing_coordinator, boot-standing) owns freshness sizing and the
// probe budget now, and two engines sizing the same posts would fight. The
// verb is kept so any residual caller answers honestly instead of vanishing;
// the coordinator's source stays in the tree pending era-5 proof. Its
// container type is no longer in the command registry, so a still-RUNNING
// legacy container fails closed at restart recovery ("unknown command type").
func (s *DaemonServer) MarketFreshnessSizerCoordinator(ctx context.Context, playerID int) (string, error) {
	return "", fmt.Errorf("the market-freshness sizer is retired: the probe-sensing coordinator (boot-standing) owns freshness sizing — operate it via `tune --operation sensing`")
}
