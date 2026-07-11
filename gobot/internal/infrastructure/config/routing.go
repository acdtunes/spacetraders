package config

import "time"

// RoutingConfig holds routing service (OR-Tools) configuration
type RoutingConfig struct {
	// gRPC service address (host:port)
	Address string `mapstructure:"address" validate:"required"`

	// Timeout settings for different operations
	Timeout RoutingTimeoutConfig `mapstructure:"timeout"`

	// ModelArtifactPath is the filesystem path to the fitted market-model artifact
	// (sp-1ek0) the tour executor reads at launch to bind the planner's model version.
	// Resolved to an ABSOLUTE path at config load (sp-wj0h): empty → the config file's
	// dir + services/routing-service/model_artifacts/market_model.json; a relative value
	// → resolved against the config file's dir; absolute → as-is. This is what makes the
	// tour engine work regardless of the daemon's cwd (the launchd daemon's cwd is not
	// the repo root, which DOA'd the first tour on the old cwd-relative constant).
	ModelArtifactPath string `mapstructure:"model_artifact_path"`

	// GateBackoff tunes the gate-graph negative-result backoff (sp-ikx1): how long an
	// UNREADABLE jump gate (one whose live fetch 400s, "no ship present") waits before it
	// is re-probed, so a doomed frontier gate is not re-fetched every reconcile tick.
	GateBackoff GateBackoffConfig `mapstructure:"gate_backoff"`
}

// GateBackoffConfig is the exponential schedule for re-probing an unreadable jump gate
// (sp-ikx1). The nth consecutive failure waits Initial * Multiplier^(n-1), capped at Max.
// The ruled defaults (Initial 5m, Multiplier 6, Max 2h) yield exactly 5m → 30m → 2h → 2h…
// — RULINGS #5: these are config, not constants, so the Admiral can retune the API-spend
// vs. staleness trade-off without a rebuild.
type GateBackoffConfig struct {
	// Initial is the first backoff window (after the first failed probe).
	Initial time.Duration `mapstructure:"initial"`
	// Multiplier grows the window on each subsequent consecutive failure.
	Multiplier float64 `mapstructure:"multiplier"`
	// Max caps the window — the longest an unreadable gate ever waits between probes.
	Max time.Duration `mapstructure:"max"`
}

// RoutingTimeoutConfig holds timeout configuration for routing operations
type RoutingTimeoutConfig struct {
	// Connection timeout
	Connect time.Duration `mapstructure:"connect" validate:"required"`

	// Dijkstra pathfinding timeout
	Dijkstra time.Duration `mapstructure:"dijkstra" validate:"required"`

	// TSP (Traveling Salesman Problem) timeout
	TSP time.Duration `mapstructure:"tsp" validate:"required"`

	// VRP (Vehicle Routing Problem) timeout
	VRP time.Duration `mapstructure:"vrp" validate:"required"`
}
