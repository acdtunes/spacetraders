package commands

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
)

// staticLiveConfig serves a fixed snapshot of the container's persisted config, so a test can
// pin a tunable-only knob the launch command has no field for.
type staticLiveConfig liveconfig.Snapshot

func (s staticLiveConfig) Snapshot(_ context.Context, _ string, _ int) (liveconfig.Snapshot, error) {
	return liveconfig.Snapshot(s), nil
}
