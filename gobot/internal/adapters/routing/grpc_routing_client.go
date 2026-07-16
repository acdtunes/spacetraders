package routing

import (
	"context"
	"fmt"
	"sort"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"

	domainRouting "github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/routing"
)

const (
	unknownRoutingError = "unknown error"
	defaultFlightMode   = "CRUISE"
)

// GRPCRoutingClient implements RoutingClient using gRPC to communicate with Python OR-Tools service
type GRPCRoutingClient struct {
	conn   *grpc.ClientConn
	client pb.RoutingServiceClient
}

// NewGRPCRoutingClient creates a new gRPC routing client.
//
// The ClientConn is created lazily (grpc.NewClient): it is constructed here with
// no network I/O, the transport connects on the first RPC, and gRPC transparently
// reconnects for the life of the process. The daemon therefore boots even when the
// routing service is down — RPCs issued during an outage fail fast with
// codes.Unavailable and self-heal once the service returns (sp-g5ct). The
// constructor only errors on a malformed target/credentials, never on the service
// being unreachable.
//
// Note: grpc.NewClient uses the "dns" resolver by default (the deprecated
// DialContext used "passthrough"). The configured address is a host:port such as
// localhost:50051, which the dns resolver resolves correctly.
func NewGRPCRoutingClient(address string) (*GRPCRoutingClient, error) {
	conn, err := grpc.NewClient(address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create routing client for %s: %w", address, err)
	}

	client := pb.NewRoutingServiceClient(conn)

	return &GRPCRoutingClient{
		conn:   conn,
		client: client,
	}, nil
}

// WaitForReady nudges the lazy connection out of IDLE and blocks until it reaches
// READY or ctx expires, returning nil on success and an error naming the last
// observed state otherwise. It is a boot-time observability probe only: nothing in
// the engine depends on it, and a failed probe simply means route planning is
// degraded until the service returns (the conn reconnects on its own). (sp-g5ct)
func (c *GRPCRoutingClient) WaitForReady(ctx context.Context) error {
	c.conn.Connect()
	for {
		state := c.conn.GetState()
		if state == connectivity.Ready {
			return nil
		}
		if !c.conn.WaitForStateChange(ctx, state) {
			return fmt.Errorf("routing service at %s not ready (last state: %s)", c.conn.Target(), state)
		}
	}
}

// Close closes the gRPC connection
func (c *GRPCRoutingClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// PlanRoute implements RoutingClient.PlanRoute using gRPC
func (c *GRPCRoutingClient) PlanRoute(ctx context.Context, req *domainRouting.RouteRequest) (*domainRouting.RouteResponse, error) {
	// Convert to protobuf request
	pbReq := &pb.PlanRouteRequest{
		SystemSymbol:  req.SystemSymbol,
		StartWaypoint: req.StartWaypoint,
		GoalWaypoint:  req.GoalWaypoint,
		CurrentFuel:   int32(req.CurrentFuel),
		FuelCapacity:  int32(req.FuelCapacity),
		EngineSpeed:   int32(req.EngineSpeed),
		Waypoints:     convertWaypointsToPb(req.Waypoints),
		FuelEfficient: req.FuelEfficient,
		PreferCruise:  req.PreferCruise,
	}

	// Call gRPC service
	pbResp, err := c.client.PlanRoute(ctx, pbReq)
	if err != nil {
		return nil, fmt.Errorf("gRPC PlanRoute failed: %w", err)
	}

	if !pbResp.Success {
		return nil, fmt.Errorf("routing failed: %s", responseErrorMessage(pbResp.ErrorMessage))
	}

	// Convert response
	return &domainRouting.RouteResponse{
		Steps:            convertRouteStepsFromPb(pbResp.Steps),
		TotalFuelCost:    int(pbResp.TotalFuelCost),
		TotalTimeSeconds: int(pbResp.TotalTimeSeconds),
		TotalDistance:    pbResp.TotalDistance,
	}, nil
}

// OptimizeTour implements RoutingClient.OptimizeTour using gRPC
// Tours always return to start by definition
func (c *GRPCRoutingClient) OptimizeTour(ctx context.Context, req *domainRouting.TourRequest) (*domainRouting.TourResponse, error) {
	// Convert to protobuf request
	pbReq := &pb.OptimizeTourRequest{
		SystemSymbol:    req.SystemSymbol,
		StartWaypoint:   req.StartWaypoint,
		TargetWaypoints: req.Waypoints,
		FuelCapacity:    int32(req.FuelCapacity),
		EngineSpeed:     int32(req.EngineSpeed),
		AllWaypoints:    convertWaypointsToPb(req.AllWaypoints),
	}

	// Call gRPC service
	pbResp, err := c.client.OptimizeTour(ctx, pbReq)
	if err != nil {
		return nil, fmt.Errorf("gRPC OptimizeTour failed: %w", err)
	}

	if !pbResp.Success {
		return nil, fmt.Errorf("tour optimization failed: %s", responseErrorMessage(pbResp.ErrorMessage))
	}

	// Convert response
	return &domainRouting.TourResponse{
		VisitOrder:       pbResp.VisitOrder,
		CombinedRoute:    convertRouteStepsFromPb(pbResp.RouteSteps),
		TotalTimeSeconds: int(pbResp.TotalTimeSeconds),
	}, nil
}

// OptimizeFueledTour implements RoutingClient.OptimizeFueledTour using gRPC
// This endpoint globally optimizes visit order + flight modes + refuel stops
func (c *GRPCRoutingClient) OptimizeFueledTour(ctx context.Context, req *domainRouting.FueledTourRequest) (*domainRouting.FueledTourResponse, error) {
	// Convert to protobuf request
	pbReq := &pb.OptimizeFueledTourRequest{
		SystemSymbol:    req.SystemSymbol,
		StartWaypoint:   req.StartWaypoint,
		TargetWaypoints: req.TargetWaypoints,
		CurrentFuel:     int32(req.CurrentFuel),
		FuelCapacity:    int32(req.FuelCapacity),
		EngineSpeed:     int32(req.EngineSpeed),
		AllWaypoints:    convertWaypointsToPb(req.AllWaypoints),
	}

	// Set optional return waypoint
	if req.ReturnWaypoint != "" {
		pbReq.ReturnWaypoint = &req.ReturnWaypoint
	}

	// Call gRPC service
	pbResp, err := c.client.OptimizeFueledTour(ctx, pbReq)
	if err != nil {
		return nil, fmt.Errorf("gRPC OptimizeFueledTour failed: %w", err)
	}

	if !pbResp.Success {
		return nil, fmt.Errorf("fueled tour optimization failed: %s", responseErrorMessage(pbResp.ErrorMessage))
	}

	// Convert legs from protobuf
	legs := make([]*domainRouting.TourLegData, len(pbResp.Legs))
	for i, pbLeg := range pbResp.Legs {
		// Convert intermediate stops
		stops := make([]*domainRouting.IntermediateStopData, len(pbLeg.IntermediateStops))
		for j, pbStop := range pbLeg.IntermediateStops {
			stops[j] = &domainRouting.IntermediateStopData{
				Waypoint:     pbStop.Waypoint,
				FlightMode:   pbStop.FlightMode,
				FuelCost:     int(pbStop.FuelCost),
				TimeSeconds:  int(pbStop.TimeSeconds),
				RefuelAmount: int(pbStop.RefuelAmount),
			}
		}

		refuelAmount := 0
		if pbLeg.RefuelAmount != nil {
			refuelAmount = int(*pbLeg.RefuelAmount)
		}

		legs[i] = &domainRouting.TourLegData{
			FromWaypoint:      pbLeg.FromWaypoint,
			ToWaypoint:        pbLeg.ToWaypoint,
			FlightMode:        pbLeg.FlightMode,
			FuelCost:          int(pbLeg.FuelCost),
			TimeSeconds:       int(pbLeg.TimeSeconds),
			Distance:          pbLeg.Distance,
			RefuelBefore:      pbLeg.RefuelBefore,
			RefuelAmount:      refuelAmount,
			IntermediateStops: stops,
		}
	}

	return &domainRouting.FueledTourResponse{
		VisitOrder:       pbResp.VisitOrder,
		Legs:             legs,
		TotalTimeSeconds: int(pbResp.TotalTimeSeconds),
		TotalFuelCost:    int(pbResp.TotalFuelCost),
		TotalDistance:    pbResp.TotalDistance,
		RefuelStops:      int(pbResp.RefuelStops),
	}, nil
}

// PartitionFleet implements RoutingClient.PartitionFleet using gRPC
func (c *GRPCRoutingClient) PartitionFleet(ctx context.Context, req *domainRouting.VRPRequest) (*domainRouting.VRPResponse, error) {
	// Convert ship configs to protobuf
	pbShipConfigs := make(map[string]*pb.ShipConfig)
	for ship, config := range req.ShipConfigs {
		pbShipConfigs[ship] = &pb.ShipConfig{
			CurrentLocation: config.CurrentLocation,
			FuelCapacity:    int32(config.FuelCapacity),
			EngineSpeed:     int32(config.EngineSpeed),
			CurrentFuel:     int32(config.FuelCapacity), // Assume full fuel
		}
	}

	// Convert to protobuf request
	pbReq := &pb.PartitionFleetRequest{
		SystemSymbol:    req.SystemSymbol,
		ShipSymbols:     req.ShipSymbols,
		MarketWaypoints: req.MarketWaypoints,
		ShipConfigs:     pbShipConfigs,
		AllWaypoints:    convertWaypointsToPb(req.AllWaypoints),
		Iterations:      1, // Default to single iteration
	}

	// Call gRPC service
	pbResp, err := c.client.PartitionFleet(ctx, pbReq)
	if err != nil {
		return nil, fmt.Errorf("gRPC PartitionFleet failed: %w", err)
	}

	if !pbResp.Success {
		return nil, fmt.Errorf("fleet partitioning failed: %s", responseErrorMessage(pbResp.ErrorMessage))
	}

	// Convert assignments
	assignments := make(map[string]*domainRouting.ShipTourData)
	for ship, tour := range pbResp.Assignments {
		assignments[ship] = &domainRouting.ShipTourData{
			Waypoints: tour.Waypoints,
			Route:     convertRouteStepsFromPb(tour.RouteSteps),
		}
	}

	return &domainRouting.VRPResponse{
		Assignments: assignments,
	}, nil
}

// Helper functions for conversion

func responseErrorMessage(errorMessage *string) string {
	if errorMessage == nil {
		return unknownRoutingError
	}
	return *errorMessage
}

func convertWaypointsToPb(waypoints []*system.WaypointData) []*pb.Waypoint {
	pbWaypoints := make([]*pb.Waypoint, len(waypoints))
	for i, wp := range waypoints {
		pbWaypoints[i] = &pb.Waypoint{
			Symbol:  wp.Symbol,
			X:       wp.X,
			Y:       wp.Y,
			HasFuel: wp.HasFuel,
		}
	}
	return pbWaypoints
}

func convertRouteStepsFromPb(pbSteps []*pb.RouteStep) []*domainRouting.RouteStepData {
	steps := make([]*domainRouting.RouteStepData, len(pbSteps))
	for i, pbStep := range pbSteps {
		action := domainRouting.RouteActionTravel
		if pbStep.Action == pb.RouteAction_ROUTE_ACTION_REFUEL {
			action = domainRouting.RouteActionRefuel
		}

		// Extract flight mode from protobuf (BURN-first logic is in routing engine)
		mode := defaultFlightMode
		if pbStep.Mode != nil && *pbStep.Mode != "" {
			mode = *pbStep.Mode
		}

		steps[i] = &domainRouting.RouteStepData{
			Action:      action,
			Waypoint:    pbStep.Waypoint,
			FuelCost:    int(pbStep.FuelCost),
			TimeSeconds: int(pbStep.TimeSeconds),
			Mode:        mode,
		}
	}
	return steps
}

// OptimizeTradeTour implements RoutingClient.OptimizeTradeTour (sp-1ek0). It
// marshals the request-carried snapshot + waypoint coordinates + ship + constraints
// into the proto request, calls the stateless Python planner, and converts the
// response into a domain TourPlan. A transport error surfaces as an error; an
// infeasible plan comes back as a TourPlan with Feasible=false and a structured
// reason — the executor decides fail-open from that reason, never from a transport
// error (a planner that is down is a different failure than one that says "no tour").
func (c *GRPCRoutingClient) OptimizeTradeTour(
	ctx context.Context,
	snapshot []domainRouting.TourGoodSnapshot,
	waypoints []domainRouting.TourWaypoint,
	ship domainRouting.TourShipState,
	cons domainRouting.TourConstraints,
	deposits []domainRouting.TourDepositCandidate,
	absorption []domainRouting.TourMarketAbsorption,
) (*domainRouting.TourPlan, error) {
	pbResp, err := c.client.OptimizeTradeTour(ctx, buildTourRequest(snapshot, waypoints, ship, cons, deposits, absorption))
	if err != nil {
		return nil, fmt.Errorf("gRPC OptimizeTradeTour failed: %w", err)
	}
	return tourPlanFromPb(pbResp), nil
}

// buildTourRequest converts the domain inputs into the proto request. Cargo is
// emitted in a deterministic good-symbol order so request payloads (and their
// logs) are reproducible regardless of Go map iteration order.
func buildTourRequest(
	snapshot []domainRouting.TourGoodSnapshot,
	waypoints []domainRouting.TourWaypoint,
	ship domainRouting.TourShipState,
	cons domainRouting.TourConstraints,
	deposits []domainRouting.TourDepositCandidate,
	absorption []domainRouting.TourMarketAbsorption,
) *pb.OptimizeTradeTourRequest {
	pbSnapshot := make([]*pb.MarketGoodSnapshot, len(snapshot))
	for i, s := range snapshot {
		pbSnapshot[i] = &pb.MarketGoodSnapshot{
			WaypointSymbol: s.Waypoint,
			SystemSymbol:   s.System,
			GoodSymbol:     s.Good,
			Ask:            int32(s.Ask),
			Bid:            int32(s.Bid),
			TradeVolume:    int32(s.TradeVolume),
			Supply:         s.Supply,
			Activity:       s.Activity,
			ObservedAtUnix: s.ObservedAt.Unix(),
		}
	}
	pbWaypoints := make([]*pb.TourWaypoint, len(waypoints))
	for i, w := range waypoints {
		pbWaypoints[i] = &pb.TourWaypoint{
			Symbol:       w.Symbol,
			SystemSymbol: w.System,
			X:            int32(w.X),
			Y:            int32(w.Y),
		}
	}
	pbCargo := make([]*pb.TourCargoItem, 0, len(ship.Cargo))
	for good, units := range ship.Cargo {
		pbCargo = append(pbCargo, &pb.TourCargoItem{GoodSymbol: good, Units: int32(units)})
	}
	sort.Slice(pbCargo, func(i, j int) bool { return pbCargo[i].GoodSymbol < pbCargo[j].GoodSymbol })

	// Deposit candidates (sp-dchv Lane C): the haul-to-storage sinks the daemon
	// assembled and capped. Emitted in a deterministic (waypoint, good) order so
	// request payloads and their logs are reproducible.
	pbDeposits := make([]*pb.DepositCandidate, 0, len(deposits))
	for _, d := range deposits {
		pbDeposits = append(pbDeposits, &pb.DepositCandidate{
			GoodSymbol:      d.Good,
			UnitsWanted:     int32(d.UnitsWanted),
			SyntheticBid:    int32(d.SyntheticBid),
			StorageWaypoint: d.StorageWaypoint,
			StorageSystem:   d.StorageSystem,
		})
	}
	sort.Slice(pbDeposits, func(i, j int) bool {
		if pbDeposits[i].StorageWaypoint != pbDeposits[j].StorageWaypoint {
			return pbDeposits[i].StorageWaypoint < pbDeposits[j].StorageWaypoint
		}
		return pbDeposits[i].GoodSymbol < pbDeposits[j].GoodSymbol
	})

	// Absorption (sp-78ai L3): the outstanding cross-container depth per
	// (waypoint, good, side) the daemon netted from the ledger. Emitted in a
	// deterministic (waypoint, good, side) order so request payloads and their logs
	// are reproducible, mirroring the snapshot/deposit ordering.
	pbAbsorption := make([]*pb.MarketAbsorption, 0, len(absorption))
	for _, a := range absorption {
		pbAbsorption = append(pbAbsorption, &pb.MarketAbsorption{
			WaypointSymbol:  a.Waypoint,
			GoodSymbol:      a.Good,
			Side:            a.Side,
			UnitsPlanned:    int32(a.PlannedUnits),
			UnitsRecovering: a.RecoveringUnits,
		})
	}
	sort.Slice(pbAbsorption, func(i, j int) bool {
		if pbAbsorption[i].WaypointSymbol != pbAbsorption[j].WaypointSymbol {
			return pbAbsorption[i].WaypointSymbol < pbAbsorption[j].WaypointSymbol
		}
		if pbAbsorption[i].GoodSymbol != pbAbsorption[j].GoodSymbol {
			return pbAbsorption[i].GoodSymbol < pbAbsorption[j].GoodSymbol
		}
		return pbAbsorption[i].Side < pbAbsorption[j].Side
	})

	return &pb.OptimizeTradeTourRequest{
		Snapshot: pbSnapshot,
		Ship: &pb.TourShip{
			ShipSymbol:      ship.ShipSymbol,
			CurrentWaypoint: ship.CurrentWaypoint,
			CurrentSystem:   ship.CurrentSystem,
			HoldCapacity:    int32(ship.HoldCapacity),
			FuelCurrent:     int32(ship.FuelCurrent),
			FuelCapacity:    int32(ship.FuelCapacity),
			EngineSpeed:     int32(ship.EngineSpeed),
			Cargo:           pbCargo,
		},
		Constraints: &pb.TourConstraints{
			MaxHops:               int32(cons.MaxHops),
			MaxSpend:              cons.MaxSpend,
			MinMarginPerUnit:      int32(cons.MinMarginPerUnit),
			WorkingCapitalReserve: cons.WorkingCapitalReserve,
			AllowedSystems:        cons.AllowedSystems,
			MaxSnapshotAgeMinutes: int32(cons.MaxSnapshotAgeMinutes),
			ExpectedModelVersion:  cons.ExpectedModelVersion,
			MaxTourSystems:        int32(cons.MaxTourSystems),
			// sp-im74: closure mode. Zero-values (false/"") serialize to nothing —
			// an open request is byte-identical to a pre-closure binary's.
			Closed:       cons.Closed,
			AnchorSystem: cons.AnchorSystem,
		},
		Waypoints:         pbWaypoints,
		DepositCandidates: pbDeposits,
		Absorption:        pbAbsorption,
	}
}

// tourPlanFromPb converts the proto response into a domain TourPlan, flattening
// each RejectedTour into a single "<summary> — <reason>" line (observability
// parity with the lane-selection log).
func tourPlanFromPb(resp *pb.OptimizeTradeTourResponse) *domainRouting.TourPlan {
	plan := &domainRouting.TourPlan{
		Feasible:                resp.GetFeasible(),
		InfeasibleReason:        resp.GetInfeasibleReason(),
		ProjectedProfit:         resp.GetProjectedProfit(),
		ProjectedCreditsPerHour: resp.GetProjectedCreditsPerHour(),
		HeldLiquidation:         resp.GetHeldLiquidation(),
		DepositValue:            resp.GetDepositValue(),
		ModelVersion:            resp.GetModelVersion(),
	}
	for _, leg := range resp.GetLegs() {
		domainLeg := domainRouting.TourLeg{
			Waypoint:              leg.GetWaypointSymbol(),
			System:                leg.GetSystemSymbol(),
			ProjectedLegProfit:    leg.GetProjectedLegProfit(),
			TravelSecondsFromPrev: int(leg.GetTravelSecondsFromPrev()),
		}
		for _, tr := range leg.GetTrades() {
			domainLeg.Trades = append(domainLeg.Trades, domainRouting.TourTrade{
				Good:              tr.GetGoodSymbol(),
				Units:             int(tr.GetUnits()),
				ExpectedUnitPrice: int(tr.GetExpectedUnitPrice()),
				IsBuy:             tr.GetIsBuy(),
				IsDeposit:         tr.GetIsDeposit(),
			})
		}
		plan.Legs = append(plan.Legs, domainLeg)
	}
	for _, r := range resp.GetTopRejected() {
		summary := r.GetSummary()
		if reason := r.GetReason(); reason != "" {
			summary = fmt.Sprintf("%s — %s", summary, reason)
		}
		plan.TopRejected = append(plan.TopRejected, summary)
	}
	return plan
}
