package grpc

import (
	"context"
	"fmt"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// StartTradeRoute implements the StartTradeRoute RPC: it launches a single-hull
// pure-arbitrage circuit as a recovery-safe daemon container, delegating to
// DaemonServer.StartTradeRoute which enforces the idle-gap discipline and owns the
// container lifecycle.
func (s *daemonServiceImpl) StartTradeRoute(ctx context.Context, req *pb.StartTradeRouteRequest) (*pb.StartTradeRouteResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}
	if req.ShipSymbol == "" {
		return nil, fmt.Errorf("ship_symbol is required")
	}
	if req.SystemSymbol == "" {
		return nil, fmt.Errorf("system_symbol is required")
	}

	maxVisits := 0
	if req.MaxVisits != nil {
		maxVisits = int(*req.MaxVisits)
	}

	destWaypoint := ""
	if req.DestWaypoint != nil {
		destWaypoint = *req.DestWaypoint
	}

	result, err := s.daemon.StartTradeRoute(ctx, req.ShipSymbol, req.SystemSymbol, maxVisits, playerID, destWaypoint)
	if err != nil {
		return nil, fmt.Errorf("failed to start trade-route: %w", err)
	}

	return &pb.StartTradeRouteResponse{
		ContainerId:  result.ContainerID,
		ShipSymbol:   result.ShipSymbol,
		SystemSymbol: result.SystemSymbol,
		Status:       "RUNNING",
		Message:      fmt.Sprintf("Trade-route circuit started for %s in %s", req.ShipSymbol, req.SystemSymbol),
	}, nil
}

// StartWarehouse implements the StartWarehouse RPC: it launches a passive
// inventory warehouse (sp-dchv Lane B) on a dedicated storage hull as a
// recovery-safe daemon container, delegating to DaemonServer.StartWarehouse
// which enforces the idle-gap discipline and owns the container lifecycle.
func (s *daemonServiceImpl) StartWarehouse(ctx context.Context, req *pb.StartWarehouseRequest) (*pb.StartWarehouseResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}
	if req.ShipSymbol == "" {
		return nil, fmt.Errorf("ship_symbol is required")
	}
	if req.WaypointSymbol == "" {
		return nil, fmt.Errorf("waypoint_symbol is required")
	}
	if len(req.SupportedGoods) == 0 {
		return nil, fmt.Errorf("supported_goods is required (at least one good)")
	}

	result, err := s.daemon.StartWarehouse(ctx, req.ShipSymbol, req.WaypointSymbol, req.SupportedGoods, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to start warehouse: %w", err)
	}

	return &pb.StartWarehouseResponse{
		ContainerId:    result.ContainerID,
		ShipSymbol:     result.ShipSymbol,
		WaypointSymbol: result.WaypointSymbol,
		Status:         "RUNNING",
		Message:        fmt.Sprintf("Warehouse started for %s at %s", req.ShipSymbol, req.WaypointSymbol),
	}, nil
}

// StartArbRun implements the StartArbRun RPC: it launches a one-shot, captain-directed,
// guarded arbitrage run as a recovery-safe daemon container, delegating to
// DaemonServer.StartArbRun which enforces the idle-gap discipline and owns the container
// lifecycle.
func (s *daemonServiceImpl) StartArbRun(ctx context.Context, req *pb.StartArbRunRequest) (*pb.StartArbRunResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}
	if req.ShipSymbol == "" {
		return nil, fmt.Errorf("ship_symbol is required")
	}
	if req.Good == "" {
		return nil, fmt.Errorf("good is required")
	}
	if req.BuyAt == "" {
		return nil, fmt.Errorf("buy_at is required")
	}
	if req.SellAt == "" {
		return nil, fmt.Errorf("sell_at is required")
	}

	maxUnits := 0
	if req.MaxUnits != nil {
		maxUnits = int(*req.MaxUnits)
	}
	maxSpend := 0
	if req.MaxSpend != nil {
		maxSpend = int(*req.MaxSpend)
	}
	minMargin := 0
	if req.MinMargin != nil {
		minMargin = int(*req.MinMargin)
	}
	workingCapitalReserve := 0
	if req.WorkingCapitalReserve != nil {
		workingCapitalReserve = int(*req.WorkingCapitalReserve)
	}

	result, err := s.daemon.StartArbRun(ctx, req.ShipSymbol, req.Good, req.BuyAt, req.SellAt, maxUnits, maxSpend, minMargin, workingCapitalReserve, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to start arb-run: %w", err)
	}

	return &pb.StartArbRunResponse{
		ContainerId: result.ContainerID,
		ShipSymbol:  result.ShipSymbol,
		Good:        result.Good,
		BuyAt:       result.BuyAt,
		SellAt:      result.SellAt,
		Status:      "RUNNING",
		Message:     fmt.Sprintf("Arb-run started for %s: buy %s at %s, sell at %s", req.ShipSymbol, req.Good, req.BuyAt, req.SellAt),
	}, nil
}

func (s *daemonServiceImpl) StartTourRun(ctx context.Context, req *pb.StartTourRunRequest) (*pb.StartTourRunResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}
	if req.ShipSymbol == "" {
		return nil, fmt.Errorf("ship_symbol is required")
	}

	maxHops := 0
	if req.MaxHops != nil {
		maxHops = int(*req.MaxHops)
	}
	var maxSpend int64
	if req.MaxSpend != nil {
		maxSpend = *req.MaxSpend
	}
	minMargin := 0
	if req.MinMargin != nil {
		minMargin = int(*req.MinMargin)
	}
	replanLimit := 0
	if req.ReplanLimit != nil {
		replanLimit = int(*req.ReplanLimit)
	}
	var workingCapitalReserve int64
	if req.WorkingCapitalReserve != nil {
		workingCapitalReserve = *req.WorkingCapitalReserve
	}
	agentSymbol := ""
	if req.AgentSymbol != nil {
		agentSymbol = *req.AgentSymbol
	}
	// iterations: -1 = continuous, N>0 = N tours, unset/0 = one tour. Passed
	// through verbatim; the coordinator normalizes 0 → one tour.
	iterations := 0
	if req.Iterations != nil {
		iterations = int(*req.Iterations)
	}

	// sp-nxrt: the captain CLI / gRPC tour-run has no per-launch escalation — pass nil
	// overrides so this path is byte-identical (reposition-reach follows the global config).
	result, err := s.daemon.StartTourRun(ctx, req.ShipSymbol, maxHops, maxSpend, minMargin, replanLimit, workingCapitalReserve, agentSymbol, iterations, playerID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to start tour-run: %w", err)
	}

	return &pb.StartTourRunResponse{
		ContainerId: result.ContainerID,
		ShipSymbol:  result.ShipSymbol,
		Status:      "RUNNING",
		Message:     fmt.Sprintf("Tour-run started for %s", req.ShipSymbol),
	}, nil
}

func (s *daemonServiceImpl) StartStocker(ctx context.Context, req *pb.StartStockerRequest) (*pb.StartStockerResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}
	if req.ShipSymbol == "" {
		return nil, fmt.Errorf("ship_symbol is required")
	}
	if req.WarehouseWaypoint == "" {
		return nil, fmt.Errorf("warehouse_waypoint is required")
	}

	budgetPerLeg := 0
	if req.BudgetPerLeg != nil {
		budgetPerLeg = int(*req.BudgetPerLeg)
	}
	var workingCapitalReserve int64
	if req.WorkingCapitalReserve != nil {
		workingCapitalReserve = *req.WorkingCapitalReserve
	}
	// iterations: -1 = continuous, N>0 = N round-trips, unset/0 = one round-trip. Passed
	// through verbatim; the coordinator normalizes 0 → one round-trip.
	iterations := 0
	if req.Iterations != nil {
		iterations = int(*req.Iterations)
	}
	maxMarketAgeMinutes := 0
	if req.MaxMarketAgeMinutes != nil {
		maxMarketAgeMinutes = int(*req.MaxMarketAgeMinutes)
	}
	targetPerGood := 0
	if req.TargetPerGood != nil {
		targetPerGood = int(*req.TargetPerGood)
	}
	// sp-k1ka: STANDING refill + its cadence/hysteresis knobs. standing=false (unset) keeps
	// the historical finite/continuous behavior; standing=true makes the container park-and-
	// re-stage at target and survive restart (re-adopted standing from persisted config).
	standing := false
	if req.Standing != nil {
		standing = *req.Standing
	}
	tickSeconds := 0
	if req.TickSeconds != nil {
		tickSeconds = int(*req.TickSeconds)
	}
	refillHysteresis := 0
	if req.RefillHysteresis != nil {
		refillHysteresis = int(*req.RefillHysteresis)
	}
	agentSymbol := ""
	if req.AgentSymbol != nil {
		agentSymbol = *req.AgentSymbol
	}

	// homeSystemOnly=false: the CLI `workflow stocker` launches the GENERIC cross-system stocker,
	// unchanged. The intra-system constraint (RULINGS #14) is set only by launchDepotStocker
	// for the contract depot — no proto/CLI surface, so this manual path keeps its cross-system economics.
	result, err := s.daemon.StartStocker(ctx, req.ShipSymbol, req.WarehouseWaypoint, budgetPerLeg, workingCapitalReserve, iterations, maxMarketAgeMinutes, targetPerGood, standing, tickSeconds, refillHysteresis, false, agentSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to start stocker: %w", err)
	}

	return &pb.StartStockerResponse{
		ContainerId:       result.ContainerID,
		ShipSymbol:        result.ShipSymbol,
		WarehouseWaypoint: result.WarehouseWaypoint,
		Status:            "RUNNING",
		Message:           fmt.Sprintf("Stocker started for %s filling warehouse %s", req.ShipSymbol, req.WarehouseWaypoint),
	}, nil
}

// GasExtractionOperation starts a gas extraction operation with siphon and transport ships
func (s *daemonServiceImpl) GasExtractionOperation(ctx context.Context, req *pb.GasExtractionOperationRequest) (*pb.GasExtractionOperationResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}

	gasGiant := ""
	if req.GasGiant != nil {
		gasGiant = *req.GasGiant
	}

	result, err := s.daemon.GasExtractionOperation(
		ctx,
		gasGiant,
		req.SiphonShips,
		req.TransportShips,
		req.Force,
		req.DryRun,
		int(req.MaxLegTime),
		playerID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to start gas extraction operation: %w", err)
	}

	status := "RUNNING"
	if req.DryRun {
		status = "DRY_RUN_COMPLETE"
	}

	resp := &pb.GasExtractionOperationResponse{
		ContainerId:    result.ContainerID,
		GasGiant:       result.GasGiant,
		SiphonShips:    req.SiphonShips,
		TransportShips: req.TransportShips,
		Status:         status,
		Errors:         result.Errors,
	}

	if req.DryRun && len(result.ShipRoutes) > 0 {
		resp.ShipRoutes = make([]*pb.ShipRoute, len(result.ShipRoutes))
		for i, route := range result.ShipRoutes {
			segments := make([]*pb.RouteSegment, len(route.Segments))
			for j, seg := range route.Segments {
				segments[j] = &pb.RouteSegment{
					From:       seg.From,
					To:         seg.To,
					FlightMode: seg.FlightMode,
					FuelCost:   int32(seg.FuelCost),
					TravelTime: int32(seg.TravelTime),
				}
			}
			resp.ShipRoutes[i] = &pb.ShipRoute{
				ShipSymbol: route.ShipSymbol,
				ShipType:   route.ShipType,
				Segments:   segments,
				TotalFuel:  int32(route.TotalFuel),
				TotalTime:  int32(route.TotalTime),
			}
		}
	}

	return resp, nil
}
