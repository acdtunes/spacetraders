package commands

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// ContainerStatusQuery reads container lifecycle state so the reconciler can tell
// a live tour from a dead or completed one. Satisfied by the GORM container
// repository (ListByStatusSimple + ContainerStatus).
type ContainerStatusQuery interface {
	ListByStatusSimple(ctx context.Context, status string, playerID *int) ([]persistence.ContainerSummary, error)

	// ContainerStatus resolves a SINGLE container's status and existence, so the orphan
	// sweep can ask IsClaimOrphaned about the exact container a scout hull claims.
	// found=false means the row is gone. Satisfied by the GORM container repository's
	// ContainerStatus (the same per-ID read refresh_ship's stale-claim reconciler uses).
	ContainerStatus(ctx context.Context, containerID string, playerID shared.PlayerID) (string, bool, error)

	// ListRunningScoutWorkers returns the player's RUNNING scout_tour /
	// scout_reposition containers with each one's persisted coordinator_id ("" for
	// a manual tour) — the zombie sweep's container-side view, since a worker whose
	// post was removed is referenced by no slot and invisible to every post-driven
	// pass. Satisfied by the GORM container repository.
	ListRunningScoutWorkers(ctx context.Context, playerID shared.PlayerID) ([]persistence.ScoutWorkerSummary, error)
}

// MarketWaypointProvider lists the marketplace waypoints in a system — the tour a
// post's hull flies. Satisfied by the GORM waypoint repository
// (ListBySystemWithTrait).
type MarketWaypointProvider interface {
	ListBySystemWithTrait(ctx context.Context, systemSymbol, trait string) ([]*shared.Waypoint, error)
}

// GateGraph resolves multi-jump routes over the persisted cross-system gate graph. The
// coordinator BFS-walks it to pick the FLEET-WIDE nearest idle satellite (fewest jump
// hops) to reposition to an unmanned post, and to fail closed when no satellite can
// reach it. Optional: nil disables repositioning entirely — the coordinator then parks
// a satellite-less post instead.
type GateGraph interface {
	// RepositionPath resolves the fleet-wide reposition route over the PERSISTED stored
	// adjacency bounded to maxJumps: it routes PAST an unreadable frontier gate instead
	// of dead-ending on it, reaching posts the strict fetch-through MaxJumpPath=5
	// rejects. Safe for the expendable scout class only; every arrival re-reads its gate
	// (chart-on-arrival), so the relaxation retires itself.
	RepositionPath(ctx context.Context, fromSystem, toSystem string, maxJumps int) ([]string, error)
	// Adjacency returns every gate-CHARTED system's stored neighbor edges (era-scoped, a
	// pure store read — no live fetch). The gate-reconcile sweep reads its KEY SET as
	// "systems whose jump gate is already charted", and enumerates the retroactive
	// backlog as the market-known systems MINUS this set. *gategraph.Service satisfies it.
	Adjacency(ctx context.Context) (map[string][]system.GateEdge, error)
}

// MarketFreshnessProvider computes, per POSTED system, the worst-case cached
// market-data staleness — MAX(now - last_updated) across that system's markets —
// backing the scout_freshness_actual_seconds gauge. One call per sweep covers every
// system for the player in a single query. Satisfied by the GORM market repository
// (MarketRepositoryGORM.MaxAgeSecondsBySystem). Optional: nil disables the gauge
// entirely (pure OBSERVATION, RULINGS #4).
type MarketFreshnessProvider interface {
	MaxAgeSecondsBySystem(ctx context.Context, playerID int) (map[string]float64, error)
}

// UnreadableGateProvider enumerates the persisted negative-result backoff markers:
// every era-scoped UNCHARTED gate a hull's live GetJumpGate 400'd on, mapped to the
// gate waypoint the marker recorded. A marker row exists ONLY because fleet traffic
// actually tried to route THROUGH that gate, so the set is intrinsically bounded to
// traffic-touched gates. This is the "an active route traverses this uncharted gate"
// signal the gate-reconcile sweep widens onto: it does not need the target to bear a
// market. Satisfied by the GORM gate-edge repository (GormGateEdgeRepository.
// UnreadableGates). Optional: nil leaves the sweep market-only.
type UnreadableGateProvider interface {
	UnreadableGates(ctx context.Context) (map[string]string, error)
}

// SystemProbeDemandReader answers a system's freshsizer probe DEMAND — the minimum
// scout-probe count that system needs to hold its markets within the freshness SLA.
// The cross-system reuse relay reads it to find OVER-COVERED source systems (manning
// supply > demand) it may borrow ONE surplus probe from, NEVER stripping a system
// below its need. Optional (SetProbeDemandReader): nil disables the cross-system relay
// entirely, so no probe is ever pulled off a manning tour. Production-backed by the
// SAME SystemsFreshness census the manning watchdog reads (CensusProbeDemandReader),
// so demand HONORS the freshsizer's age-driven raises: a system BREACHING its SLA
// reads a raised demand and is never raided.
type SystemProbeDemandReader interface {
	ProbeDemand(ctx context.Context, playerID int, systemSymbol string) (int, error)
}

// SetGateGraph wires the multi-jump gate-graph resolver. The daemon injects the same
// persisted, fetch-through gategraph.Service the trade-route circuit uses, so the
// reposition BFS and the circuit's travel() share one cache/graph. Optional-injection:
// nil (the default) leaves repositioning disabled and posts park instead.
func (h *RunScoutPostCoordinatorHandler) SetGateGraph(g GateGraph) {
	h.gateGraph = g
}

// SetGraphProvider wires the presence-free waypoint discoverer for virgin reposition
// targets and the coordinate source for the VRP partitioner. The daemon injects the
// same graphService the `waypoint` verb and the scout-markets planner use, so
// discovery shares one cache/graph and persists era-scoped exactly as every other
// charting path. Optional-injection: nil (the default) leaves posts parked instead.
func (h *RunScoutPostCoordinatorHandler) SetGraphProvider(g system.ISystemGraphProvider) {
	h.graphProvider = g
}

// SetRoutingClient wires the VRP fleet partitioner. The daemon injects the SAME
// routing client the scout-markets verb uses, so a multi-probe post's disjoint
// partition is solved by the routing service that already solves it.
// Optional-injection: nil leaves partitioning disabled, so single-hull posts are
// unaffected and a multi-probe post parks fail-closed until a client is wired.
func (h *RunScoutPostCoordinatorHandler) SetRoutingClient(c routing.RoutingClient) {
	h.routingClient = c
}

// SetEventStore wires the captain event outbox for the undersized-post warning
// (layer 1). The daemon injects the SAME store the watchkeeper reads, so a warning
// rides the next wake as a deferred event. Optional-injection: nil (the default)
// leaves the warning disabled.
func (h *RunScoutPostCoordinatorHandler) SetEventStore(s captain.EventStore) {
	h.eventStore = s
}

// SetMarketFreshnessProvider wires the scout_freshness_actual_seconds gauge's data
// source. The daemon injects the same GORM market repository the rest of the
// coordinator already reads through. Optional-injection: nil (the default) leaves the
// gauge unrecorded.
func (h *RunScoutPostCoordinatorHandler) SetMarketFreshnessProvider(p MarketFreshnessProvider) {
	h.marketFreshnessProvider = p
}

// SetUnreadableGateProvider wires the traffic-marker enumeration that widens the
// gate-reconcile sweep onto marketless transit gates. The daemon injects the SAME GORM
// gate-edge repository the gate graph reads through — one store, era-scoped.
// Optional-injection: nil (the default) leaves the sweep market-only.
func (h *RunScoutPostCoordinatorHandler) SetUnreadableGateProvider(p UnreadableGateProvider) {
	h.unreadableGateProvider = p
}

// SetSystemFreshnessReader wires the manning watchdog's per-system freshness census
// (SystemsFreshness). The daemon injects the SAME GORM market repository the freshness
// sizer reconciles against, so the watchdog and the sizer see one consistent census.
// Optional-injection: nil (the default) disables the watchdog.
func (h *RunScoutPostCoordinatorHandler) SetSystemFreshnessReader(r domainScouting.SystemFreshnessReader) {
	h.systemFreshnessReader = r
}

// SetProbeDemandReader wires the per-system freshsizer-demand source the cross-system
// reuse relay checks over-coverage against. The daemon injects CensusProbeDemandReader
// over the SAME SystemsFreshness census the watchdog reads. Optional-injection: nil
// (the default) disables the cross-system relay entirely — no probe is borrowed off a
// manning tour.
func (h *RunScoutPostCoordinatorHandler) SetProbeDemandReader(r SystemProbeDemandReader) {
	h.probeDemandReader = r
}

// SetLiveConfigReader wires the per-tick live-config snapshot source so the manning
// watchdog's manning_stall_* knobs honor `spacetraders tune` on the next tick. The
// daemon injects the SAME container-config-backed reader the freshness sizer uses.
// Optional-injection: nil (the default) leaves those knobs launch-frozen (read from
// the command).
func (h *RunScoutPostCoordinatorHandler) SetLiveConfigReader(r liveconfig.Reader) {
	h.liveConfig = r
}
