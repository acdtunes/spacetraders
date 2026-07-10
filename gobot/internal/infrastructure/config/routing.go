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
