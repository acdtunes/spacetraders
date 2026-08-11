package api

import (
	"context"

	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// gateProbeAPI is the slice of the API the gate graph drives: the topology fetch,
// the per-gate construction probe, and the public chart. Declared here so the
// decorator composes with the same narrow surface its consumer expects.
type gateProbeAPI interface {
	GetJumpGate(ctx context.Context, systemSymbol, waypointSymbol, token string) (*domainPorts.JumpGateData, error)
	GetWaypoint(ctx context.Context, systemSymbol, waypointSymbol, token string) (*domainPorts.WaypointDetail, error)
	CreateChart(ctx context.Context, shipSymbol, token string) (*domainPorts.ChartResult, error)
}

// BuiltGateStore answers whether a gate waypoint is already recorded as finished
// building, within the era and freshness window the routing cache already trusts.
// It reports false for every uncertain case so the probe falls through to the API.
type BuiltGateStore interface {
	RecordedBuiltGate(ctx context.Context, gateWaypoint string) (bool, error)
}

// GateConstructionProbe wraps the gate-graph API surface so a construction probe
// for an ALREADY-BUILT gate is answered from the store instead of the network.
//
// The gate graph re-reads every connected gate's build state whenever a system's
// edge set is refreshed, and a set expires as a whole — so one neighbour still
// under construction pulls its healthy siblings onto the short window and re-probes
// gates whose verdict cannot have changed. Construction is monotone (a gate goes
// under-construction → complete and never back), so a stored built verdict is the
// same answer the API would give, and the probe is pure rate-limit cost.
//
// Only the built verdict is served from the store. An under-construction gate, an
// unknown gate, a stale record, a dead era, and any store failure all fall through
// to the live read, so a completing build is still noticed on the normal cycle and
// the caller's fail-closed handling is untouched.
type GateConstructionProbe struct {
	inner gateProbeAPI
	store BuiltGateStore
}

// NewGateConstructionProbe decorates inner with store-served construction verdicts.
// A nil store degrades to a plain pass-through.
func NewGateConstructionProbe(inner gateProbeAPI, store BuiltGateStore) *GateConstructionProbe {
	return &GateConstructionProbe{inner: inner, store: store}
}

// GetJumpGate passes through — the topology fetch is not cached here.
func (p *GateConstructionProbe) GetJumpGate(ctx context.Context, systemSymbol, waypointSymbol, token string) (*domainPorts.JumpGateData, error) {
	return p.inner.GetJumpGate(ctx, systemSymbol, waypointSymbol, token)
}

// CreateChart passes through — charting is a write and always goes live. The reward it
// returns is recorded by the caller, so this decorator must not swallow the result.
func (p *GateConstructionProbe) CreateChart(ctx context.Context, shipSymbol, token string) (*domainPorts.ChartResult, error) {
	return p.inner.CreateChart(ctx, shipSymbol, token)
}

// GetWaypoint answers from the store only when the gate is already recorded built;
// everything else, including a store failure, is read live.
func (p *GateConstructionProbe) GetWaypoint(ctx context.Context, systemSymbol, waypointSymbol, token string) (*domainPorts.WaypointDetail, error) {
	if p.store != nil {
		if built, err := p.store.RecordedBuiltGate(ctx, waypointSymbol); err == nil && built {
			return &domainPorts.WaypointDetail{Symbol: waypointSymbol, IsUnderConstruction: false}, nil
		}
	}
	return p.inner.GetWaypoint(ctx, systemSymbol, waypointSymbol, token)
}
