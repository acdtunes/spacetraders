package commands

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	freshnessScoutFleetTag = "scout"

	// Global fallback when a system carries no activity signal.
	defaultSLASeconds = 3600

	// Seeded per-market cycle used until telemetry exists.
	defaultSeedCycleSeconds = 180
)

// FleetReader counts the scout-probe supply (idle + in-flight + manning) and the
// satellite fleet size the cap gates on. Read-only.
type FleetReader interface {
	FindAllByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error)
}
