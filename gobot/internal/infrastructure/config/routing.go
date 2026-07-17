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

	// GateCacheTTL bounds how long a stored jump-gate edge is trusted before a routing
	// lookup treats it as stale and triggers a live re-fetch (sp-jgcache). The gate-graph
	// topology is effectively static within an era (a gate's connection set does not churn
	// hour-to-hour), so the default is a comfortable 24h — long enough that the per-tick
	// lane/reposition neighbor scan is a cache hit (0 API) yet short enough that the graph
	// self-heals across a long-running daemon. Zero => the 24h default (SetDefaults). Wired
	// into the gate-edge repository's healthy-edge freshness window; the SHORTER
	// under-construction window is a separate correctness bound and is not tuned here.
	GateCacheTTL time.Duration `mapstructure:"gate_cache_ttl"`

	// SkipUnchartedGateFetch is the doomed-call precondition switch (sp-jgcache, default ON).
	// A remote (no-ship) gate read whose own gate is still UNCHARTED is guaranteed to 400
	// ("uncharted, no ship present"); ON, the gate graph reads the UNCHARTED trait off the
	// system graph it already holds and SKIPS that live call entirely (0 API), entering the
	// sp-ikx1 backoff exactly as a real 400 would. A *bool so an absent [routing] section
	// defaults ON while an explicit false — the staged-rollout reversibility switch that
	// restores the pre-fix probe-then-backoff behaviour — is preserved.
	SkipUnchartedGateFetch *bool `mapstructure:"skip_uncharted_gate_fetch"`

	// ChartGateOnArrival is the sp-bcsu chart-on-gate-arrival switch (default ON). A hull
	// jumping into a system lands on that system's jump gate — the ONE moment its outbound
	// edges are readable (a remote read with no ship present 400s) — so the gate-crosser
	// charts+persists gate_edges there, before flying to market. Without it a market-swept
	// system's gate stays empty, the strict pathfinder fails closed on it, and hulls routed
	// through it strand. Charting is best-effort (never fails a leg) and idempotent (a
	// charted system is a store hit, zero API), so it is safe on by default; this knob is
	// the reversibility switch (set false to restore the pre-sp-bcsu hot path exactly). A
	// *bool so an absent [routing] section defaults ON while an explicit false is preserved.
	ChartGateOnArrival *bool `mapstructure:"chart_gate_on_arrival"`
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
