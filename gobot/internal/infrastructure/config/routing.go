package config

import "time"

// RoutingConfig holds routing service (OR-Tools) configuration
type RoutingConfig struct {
	// gRPC service address (host:port)
	Address string `mapstructure:"address" validate:"required"`

	// Timeout settings for different operations
	Timeout RoutingTimeoutConfig `mapstructure:"timeout"`
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
