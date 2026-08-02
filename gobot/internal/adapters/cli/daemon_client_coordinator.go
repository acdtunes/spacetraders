package cli

import (
	"context"
	"fmt"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

type ContractFleetCoordinatorResponse struct {
	ContainerID string
	ShipSymbols []string
	Status      string
}

// ContractFleetCoordinator starts a contract fleet coordinator
// Uses all available idle light hauler ships (no pre-assignment needed).
//
// dedicatedShips/standbyStations carry the operator's optional
// --dedicated-ships/--standby-stations CLI flags through to the daemon. Both
// are nil for a plain, non-dedicated coordinator - the feature is opt-in.
func (c *DaemonClient) ContractFleetCoordinator(
	ctx context.Context,
	shipSymbols []string, // Deprecated: kept for backward compatibility, ignored by server
	playerID int,
	agentSymbol string,
	dedicatedShips []string,
	standbyStations []string,
) (*ContractFleetCoordinatorResponse, error) {
	req := &pb.ContractFleetCoordinatorRequest{
		PlayerId:        int32(playerID),
		DedicatedShips:  dedicatedShips,
		StandbyStations: standbyStations,
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.ContractFleetCoordinator(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return &ContractFleetCoordinatorResponse{
		ContainerID: resp.ContainerId,
		ShipSymbols: shipSymbols,
		Status:      resp.Status,
	}, nil
}

// ScoutPostCoordinator starts the standing scout-post coordinator (sp-cxpq).
func (c *DaemonClient) ScoutPostCoordinator(ctx context.Context, playerID int, agentSymbol string, tickIntervalSecs int) (string, error) {
	req := &pb.ScoutPostCoordinatorRequest{
		PlayerId:         int32(playerID),
		TickIntervalSecs: int32(tickIntervalSecs),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	resp, err := c.client.ScoutPostCoordinator(ctx, req)
	if err != nil {
		return "", fmt.Errorf(grpcCallFailed, err)
	}
	return resp.ContainerId, nil
}

// TradeFleetCoordinator starts the standing trade-fleet coordinator (sp-1278): it keeps
// continuous tours alive on 'trade'-dedicated hulls, relaunching on honest exit after a
// cooldown. All tuning lives in config.yaml's [trade_fleet] section; this call only
// names the player/agent. Returns the coordinator container id.
func (c *DaemonClient) TradeFleetCoordinator(ctx context.Context, playerID int, agentSymbol string) (string, error) {
	req := &pb.TradeFleetCoordinatorRequest{
		PlayerId: int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	resp, err := c.client.TradeFleetCoordinator(ctx, req)
	if err != nil {
		return "", fmt.Errorf(grpcCallFailed, err)
	}
	return resp.ContainerId, nil
}

// FleetAutosizerCoordinator starts the standing fleet capacity autosizer (sp-1txd): sizes the hull
// pool to demand and auto-buys hulls behind the fail-closed money-guard stack. Identity-only launch
// — all [fleet_autosizer] tuning resolves live from config.yaml.
func (c *DaemonClient) FleetAutosizerCoordinator(ctx context.Context, playerID int, agentSymbol string) (string, error) {
	req := &pb.FleetAutosizerCoordinatorRequest{
		PlayerId: int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	resp, err := c.client.FleetAutosizerCoordinator(ctx, req)
	if err != nil {
		return "", fmt.Errorf(grpcCallFailed, err)
	}
	return resp.ContainerId, nil
}

// LongHaulArbCoordinator starts the standing long-haul arb fleet coordinator (sp-mepj): the
// out-of-horizon single-good arb engine that launches a per-hull worker on every idle
// long-haul-tagged hull. Identity-only launch; ARMED on deploy but inert until an operator tags
// a hull `fleet add --operation long-haul --ship X`.
func (c *DaemonClient) LongHaulArbCoordinator(ctx context.Context, playerID int, agentSymbol string) (string, error) {
	req := &pb.LongHaulArbCoordinatorRequest{
		PlayerId: int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	resp, err := c.client.LongHaulArbCoordinator(ctx, req)
	if err != nil {
		return "", fmt.Errorf(grpcCallFailed, err)
	}
	return resp.ContainerId, nil
}

// AutoOutfitCoordinator starts the standing guarded auto-outfit coordinator (sp-buyd): the
// module analogue of hull acquisition. Identity-only launch — all knobs default and are
// live-tunable via `tune --operation autooutfit`. dryRun (the CLI --dry-run) launches it in
// observe mode: it evaluates + logs every WOULD-install but installs nothing.
func (c *DaemonClient) AutoOutfitCoordinator(ctx context.Context, playerID int, agentSymbol string, dryRun bool) (string, error) {
	req := &pb.AutoOutfitCoordinatorRequest{
		PlayerId: int32(playerID),
		DryRun:   dryRun,
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	resp, err := c.client.AutoOutfitCoordinator(ctx, req)
	if err != nil {
		return "", fmt.Errorf(grpcCallFailed, err)
	}
	return resp.ContainerId, nil
}

// BootstrapCoordinator starts the standing captain bootstrap coordinator (sp-3nbe): a reconciler
// that drives a cold agent through the cold-start arc to the jump gate. Identity-only launch — the
// [bootstrap] boot-gate and cadence resolve live from config.yaml.
func (c *DaemonClient) BootstrapCoordinator(ctx context.Context, playerID int, agentSymbol string) (string, error) {
	req := &pb.BootstrapCoordinatorRequest{
		PlayerId: int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	resp, err := c.client.BootstrapCoordinator(ctx, req)
	if err != nil {
		return "", fmt.Errorf(grpcCallFailed, err)
	}
	return resp.ContainerId, nil
}

// FrontierExpansionCoordinatorParams carries the launch knobs for the frontier
// expansion coordinator (sp-8w89). All are optional; a 0/false value uses the
// coordinator's documented default (RULINGS #5).
type FrontierExpansionCoordinatorParams struct {
	TickIntervalSecs int
	DryRun           bool
	MaxProbeFleet    int
	ExpansionMaxHops int
}

// FrontierExpansionCoordinator starts the standing frontier expansion coordinator (sp-8w89).
func (c *DaemonClient) FrontierExpansionCoordinator(ctx context.Context, playerID int, agentSymbol string, p FrontierExpansionCoordinatorParams) (string, error) {
	req := &pb.FrontierExpansionCoordinatorRequest{
		PlayerId:         int32(playerID),
		TickIntervalSecs: int32(p.TickIntervalSecs),
		DryRun:           p.DryRun,
		MaxProbeFleet:    int32(p.MaxProbeFleet),
		ExpansionMaxHops: int32(p.ExpansionMaxHops),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	resp, err := c.client.FrontierExpansionCoordinator(ctx, req)
	if err != nil {
		return "", fmt.Errorf(grpcCallFailed, err)
	}
	return resp.ContainerId, nil
}

// ShipyardBackfillCoordinatorParams carries the launch knobs for the shipyard-backfill
// sweep (sp-s1ek). All are optional; a 0 value uses the coordinator's documented default
// (RULINGS #5). The engine has no dry-run mode.
type ShipyardBackfillCoordinatorParams struct {
	TickIntervalSecs      int
	MaxDispatchesPerCycle int
}

// ShipyardBackfillCoordinator starts the standing shipyard-backfill sweep (sp-s1ek).
func (c *DaemonClient) ShipyardBackfillCoordinator(ctx context.Context, playerID int, agentSymbol string, p ShipyardBackfillCoordinatorParams) (string, error) {
	req := &pb.ShipyardBackfillCoordinatorRequest{
		PlayerId:              int32(playerID),
		TickIntervalSecs:      int32(p.TickIntervalSecs),
		MaxDispatchesPerCycle: int32(p.MaxDispatchesPerCycle),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	resp, err := c.client.ShipyardBackfillCoordinator(ctx, req)
	if err != nil {
		return "", fmt.Errorf(grpcCallFailed, err)
	}
	return resp.ContainerId, nil
}

// GetFrontierStatus returns the frontier coordinator's live state in one view (sp-pvw3 `frontier
// status`): the effective discovery/scan split, discovery frontier depth, honest dark-market backlog,
// probe allocation, last probe buy, and current blockers.
func (c *DaemonClient) GetFrontierStatus(ctx context.Context, playerIdent *PlayerIdentifier) (*pb.GetFrontierStatusResponse, error) {
	playerID, agentSymbol := playerPointers(playerIdent)
	req := &pb.GetFrontierStatusRequest{
		PlayerId:    playerID,
		AgentSymbol: agentSymbol,
	}

	resp, err := c.client.GetFrontierStatus(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}
