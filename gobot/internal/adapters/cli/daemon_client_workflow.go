package cli

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

type BatchContractWorkflowResponse struct {
	ContainerID string
	ShipSymbol  string
	Status      string
}

// BatchContractWorkflow initiates batch contract workflow. loop=false runs a
// single contract (byte-identical to today); loop=true runs the continuous
// single-hull contract loop (sp-ehg9) by sending iterations=-1.
func (c *DaemonClient) BatchContractWorkflow(
	ctx context.Context,
	shipSymbol string,
	playerID int,
	agentSymbol string,
	loop bool,
) (*BatchContractWorkflowResponse, error) {
	iterations := int32(1)
	if loop {
		iterations = -1
	}
	req := &pb.BatchContractWorkflowRequest{
		ShipSymbol: shipSymbol,
		Iterations: iterations,
		PlayerId:   int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.BatchContractWorkflow(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return &BatchContractWorkflowResponse{
		ContainerID: resp.ContainerId,
		ShipSymbol:  resp.ShipSymbol,
		Status:      resp.Status,
	}, nil
}

// StartTradeRouteResult contains the result of starting a trade-route container
type StartTradeRouteResult struct {
	ContainerID  string
	ShipSymbol   string
	SystemSymbol string
	Status       string
	Message      string
}

// StartTradeRoute launches a single-hull pure-arbitrage circuit as a recovery-safe
// daemon container.
func (c *DaemonClient) StartTradeRoute(
	ctx context.Context,
	shipSymbol string,
	systemSymbol string,
	playerID int,
	agentSymbol *string,
	maxVisits *int32,
	destWaypoint *string,
) (*StartTradeRouteResult, error) {
	resp, err := c.client.StartTradeRoute(ctx, &pb.StartTradeRouteRequest{
		PlayerId:     int32(playerID),
		ShipSymbol:   shipSymbol,
		SystemSymbol: systemSymbol,
		AgentSymbol:  agentSymbol,
		MaxVisits:    maxVisits,
		DestWaypoint: destWaypoint,
	})
	if err != nil {
		return nil, err
	}

	return &StartTradeRouteResult{
		ContainerID:  resp.ContainerId,
		ShipSymbol:   resp.ShipSymbol,
		SystemSymbol: resp.SystemSymbol,
		Status:       resp.Status,
		Message:      resp.Message,
	}, nil
}

// StartWarehouseResult reports the container started for a passive inventory
// warehouse (sp-dchv Lane B).
type StartWarehouseResult struct {
	ContainerID    string
	ShipSymbol     string
	WaypointSymbol string
	Status         string
	Message        string
}

// StartWarehouse launches a passive inventory warehouse (sp-dchv Lane B) on an
// idle, dedicated storage hull parked at a home waypoint, as a recovery-safe
// daemon container.
func (c *DaemonClient) StartWarehouse(
	ctx context.Context,
	shipSymbol string,
	waypointSymbol string,
	supportedGoods []string,
	playerID int,
) (*StartWarehouseResult, error) {
	resp, err := c.client.StartWarehouse(ctx, &pb.StartWarehouseRequest{
		PlayerId:       int32(playerID),
		ShipSymbol:     shipSymbol,
		WaypointSymbol: waypointSymbol,
		SupportedGoods: supportedGoods,
	})
	if err != nil {
		return nil, err
	}

	return &StartWarehouseResult{
		ContainerID:    resp.ContainerId,
		ShipSymbol:     resp.ShipSymbol,
		WaypointSymbol: resp.WaypointSymbol,
		Status:         resp.Status,
		Message:        resp.Message,
	}, nil
}

type StartTourRunResult struct {
	ContainerID string
	ShipSymbol  string
	Status      string
	Message     string
}

// StartTourRun asks the daemon to launch a captain-directed, guarded multi-hop trade
// tour as a recovery-safe container (sp-1ek0). maxHops/maxSpend/minMargin/replanLimit/
// workingCapitalReserve/iterations are optional: pass nil to leave each unset (the
// coordinator's own default semantics apply — max_hops→6, max_spend→25% of treasury,
// replan_limit→2, iterations→one tour). iterations=-1 makes it CONTINUOUS:
// tour, re-plan from the new position, tour again until margins die.
func (c *DaemonClient) StartTourRun(
	ctx context.Context,
	shipSymbol string,
	playerID int,
	agentSymbol *string,
	maxHops *int32,
	maxSpend *int64,
	minMargin *int32,
	replanLimit *int32,
	workingCapitalReserve *int64,
	iterations *int32,
) (*StartTourRunResult, error) {
	resp, err := c.client.StartTourRun(ctx, &pb.StartTourRunRequest{
		PlayerId:              int32(playerID),
		ShipSymbol:            shipSymbol,
		AgentSymbol:           agentSymbol,
		MaxHops:               maxHops,
		MaxSpend:              maxSpend,
		MinMargin:             minMargin,
		ReplanLimit:           replanLimit,
		WorkingCapitalReserve: workingCapitalReserve,
		Iterations:            iterations,
	})
	if err != nil {
		return nil, err
	}

	return &StartTourRunResult{
		ContainerID: resp.ContainerId,
		ShipSymbol:  resp.ShipSymbol,
		Status:      resp.Status,
		Message:     resp.Message,
	}, nil
}

// StartArbRunResult reports the container started for a one-shot guarded arb run.
type StartArbRunResult struct {
	ContainerID string
	ShipSymbol  string
	Good        string
	BuyAt       string
	SellAt      string
	Status      string
	Message     string
}

// StartArbRun asks the daemon to launch a one-shot, captain-directed, guarded arbitrage
// run as a recovery-safe container. maxUnits/maxSpend/minMargin/workingCapitalReserve
// are optional guards: pass nil to leave each unset (the coordinator's own default/disabled
// semantics apply per guard).
func (c *DaemonClient) StartArbRun(
	ctx context.Context,
	shipSymbol string,
	good string,
	buyAt string,
	sellAt string,
	playerID int,
	agentSymbol *string,
	maxUnits *int32,
	maxSpend *int32,
	minMargin *int32,
	workingCapitalReserve *int32,
) (*StartArbRunResult, error) {
	resp, err := c.client.StartArbRun(ctx, &pb.StartArbRunRequest{
		PlayerId:              int32(playerID),
		ShipSymbol:            shipSymbol,
		Good:                  good,
		BuyAt:                 buyAt,
		SellAt:                sellAt,
		AgentSymbol:           agentSymbol,
		MaxUnits:              maxUnits,
		MaxSpend:              maxSpend,
		MinMargin:             minMargin,
		WorkingCapitalReserve: workingCapitalReserve,
	})
	if err != nil {
		return nil, err
	}

	return &StartArbRunResult{
		ContainerID: resp.ContainerId,
		ShipSymbol:  resp.ShipSymbol,
		Good:        resp.Good,
		BuyAt:       resp.BuyAt,
		SellAt:      resp.SellAt,
		Status:      resp.Status,
		Message:     resp.Message,
	}, nil
}

// StartStockerResult reports the container started for a stocker loop.
type StartStockerResult struct {
	ContainerID       string
	ShipSymbol        string
	WarehouseWaypoint string
	Status            string
	Message           string
}

// StartStocker asks the daemon to launch the STOCKER LOOP as a recovery-safe
// container: a dedicated hull fills a home warehouse with contract-recurrent goods bought
// cheap at foreign markets, live-verified and fail-closed. budgetPerLeg/workingCapitalReserve/
// iterations/maxMarketAgeMinutes/targetPerGood are optional: pass nil to leave each unset
// (the coordinator's own default semantics apply — no per-leg cap, 50k reserve, one
// round-trip, 75-min freshness, the miner's measured demand target). iterations=-1 makes
// it CONTINUOUS: fill until nothing is left to stock.
func (c *DaemonClient) StartStocker(
	ctx context.Context,
	shipSymbol string,
	warehouseWaypoint string,
	playerID int,
	agentSymbol *string,
	budgetPerLeg *int32,
	workingCapitalReserve *int64,
	iterations *int32,
	maxMarketAgeMinutes *int32,
	targetPerGood *int32,
	standing *bool,
	tickSeconds *int32,
	refillHysteresis *int32,
) (*StartStockerResult, error) {
	resp, err := c.client.StartStocker(ctx, &pb.StartStockerRequest{
		PlayerId:              int32(playerID),
		ShipSymbol:            shipSymbol,
		WarehouseWaypoint:     warehouseWaypoint,
		AgentSymbol:           agentSymbol,
		BudgetPerLeg:          budgetPerLeg,
		WorkingCapitalReserve: workingCapitalReserve,
		Iterations:            iterations,
		MaxMarketAgeMinutes:   maxMarketAgeMinutes,
		TargetPerGood:         targetPerGood,
		Standing:              standing,
		TickSeconds:           tickSeconds,
		RefillHysteresis:      refillHysteresis,
	})
	if err != nil {
		return nil, err
	}

	return &StartStockerResult{
		ContainerID:       resp.ContainerId,
		ShipSymbol:        resp.ShipSymbol,
		WarehouseWaypoint: resp.WarehouseWaypoint,
		Status:            resp.Status,
		Message:           resp.Message,
	}, nil
}

// GasExtractionOperationResponse contains the result of starting a gas extraction operation
type GasExtractionOperationResponse struct {
	ContainerID    string
	GasGiant       string
	SiphonShips    []string
	TransportShips []string
	Status         string
	// Dry-run results
	ShipRoutes []common.ShipRouteDTO
	Errors     []string
}

// GasExtractionOperation starts a gas extraction operation with siphon and transport ships
func (c *DaemonClient) GasExtractionOperation(
	ctx context.Context,
	gasGiant string,
	siphonShips []string,
	transportShips []string,
	force bool,
	dryRun bool,
	maxLegTime int,
	playerID int,
) (*GasExtractionOperationResponse, error) {
	req := &pb.GasExtractionOperationRequest{
		SiphonShips:    siphonShips,
		TransportShips: transportShips,
		PlayerId:       int32(playerID),
		Force:          force,
		DryRun:         dryRun,
		MaxLegTime:     int32(maxLegTime),
	}

	// Only set gas_giant if provided
	if gasGiant != "" {
		req.GasGiant = &gasGiant
	}

	resp, err := c.client.GasExtractionOperation(ctx, req)
	if err != nil {
		return nil, err
	}

	var shipRoutes []common.ShipRouteDTO
	for _, route := range resp.ShipRoutes {
		segments := make([]common.RouteSegmentDTO, len(route.Segments))
		for j, seg := range route.Segments {
			segments[j] = common.RouteSegmentDTO{
				From:       seg.From,
				To:         seg.To,
				FlightMode: seg.FlightMode,
				FuelCost:   int(seg.FuelCost),
				TravelTime: int(seg.TravelTime),
			}
		}
		shipRoutes = append(shipRoutes, common.ShipRouteDTO{
			ShipSymbol: route.ShipSymbol,
			ShipType:   route.ShipType,
			Segments:   segments,
			TotalFuel:  int(route.TotalFuel),
			TotalTime:  int(route.TotalTime),
		})
	}

	return &GasExtractionOperationResponse{
		ContainerID:    resp.ContainerId,
		GasGiant:       resp.GasGiant,
		SiphonShips:    resp.SiphonShips,
		TransportShips: resp.TransportShips,
		Status:         resp.Status,
		ShipRoutes:     shipRoutes,
		Errors:         resp.Errors,
	}, nil
}
