package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const grpcCallFailed = "gRPC call failed: %w"

// DaemonClient provides a client interface to communicate with the daemon via gRPC
type DaemonClient struct {
	conn       *grpc.ClientConn
	client     pb.DaemonServiceClient
	socketPath string
}

// Response types (mirrors protobuf messages)

type NavigateResponse struct {
	ContainerID   string
	ShipSymbol    string
	Destination   string
	Status        string
	EstimatedTime int32
}

type DockResponse struct {
	ContainerID string
	ShipSymbol  string
	Status      string
}

type OrbitResponse struct {
	ContainerID string
	ShipSymbol  string
	Status      string
}

type RefuelResponse struct {
	ContainerID string
	ShipSymbol  string
	FuelAdded   int32
	CreditsCost int32
	Status      string
}

type JumpResponse struct {
	Success           bool
	NavigatedToGate   bool
	JumpGateSymbol    string
	DestinationSystem string
	CooldownSeconds   int32
	Message           string
	Error             string
}

type JettisonResponse struct {
	ContainerID     string
	ShipSymbol      string
	GoodSymbol      string
	UnitsJettisoned int32
	Status          string
	Message         string
}

type BatchContractWorkflowResponse struct {
	ContainerID string
	ShipSymbol  string
	Status      string
}

type ScoutTourResponse struct {
	ContainerID string
	ShipSymbol  string
	Markets     []string
	Iterations  int
	Status      string
}

type ScoutMarketsResponse struct {
	ContainerIDs     []string
	Assignments      map[string]*MarketAssignment
	ReusedContainers []string
}

type MarketAssignment struct {
	Markets []string
}

type ContractFleetCoordinatorResponse struct {
	ContainerID string
	ShipSymbols []string
	Status      string
}

// ScoutPost mirrors the protobuf ScoutPost message for CLI display (sp-cxpq). Hulls
// is the probe budget N and MannedCount how many of those slots have a hull (sp-enry).
type ScoutPost struct {
	SystemSymbol     string
	FreshnessSeconds int
	Kind             string
	AssignedHull     string
	TourContainerID  string
	Hulls            int
	MannedCount      int
}

// ContainerInfo mirrors the protobuf ContainerInfo message for CLI display.
// This struct includes all fields needed for user-facing container information.
// Note: PlayerID is int32 per protobuf requirements (converted from domain int).
type ContainerInfo struct {
	ContainerID      string
	ContainerType    string
	Status           string
	PlayerID         int32 // Protobuf int32 (convert from domain int)
	CreatedAt        string
	UpdatedAt        string
	CurrentIteration int32
	MaxIterations    int32
	RestartCount     int32
	Metadata         string
}

type StopContainerResponse struct {
	ContainerID string
	Status      string
	Message     string
}

type LogEntry struct {
	Timestamp string
	Level     string
	Message   string
	Metadata  string
}

type HealthResponse struct {
	Status           string
	Version          string
	ActiveContainers int32
}

// NewDaemonClient creates a new daemon client
func NewDaemonClient(socketPath string) (*DaemonClient, error) {
	// Create context with timeout for connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Connect to Unix socket via gRPC
	// Use "unix:" scheme for Unix domain sockets
	conn, err := grpc.DialContext(
		ctx,
		"unix:"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to daemon socket: %w", err)
	}

	// Create gRPC client
	client := pb.NewDaemonServiceClient(conn)

	return &DaemonClient{
		conn:       conn,
		client:     client,
		socketPath: socketPath,
	}, nil
}

// Close closes the client connection
func (c *DaemonClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// NavigateShip initiates ship navigation
func (c *DaemonClient) NavigateShip(
	ctx context.Context,
	shipSymbol, destination string,
	playerID int,
	agentSymbol string,
) (*NavigateResponse, error) {
	// Build request
	req := &pb.NavigateShipRequest{
		ShipSymbol:  shipSymbol,
		Destination: destination,
		PlayerId:    int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	// Call gRPC service
	resp, err := c.client.NavigateShip(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	// Convert to client response type
	return &NavigateResponse{
		ContainerID:   resp.ContainerId,
		ShipSymbol:    resp.ShipSymbol,
		Destination:   resp.Destination,
		Status:        resp.Status,
		EstimatedTime: resp.EstimatedTimeSeconds,
	}, nil
}

// DockShip initiates ship docking
func (c *DaemonClient) DockShip(
	ctx context.Context,
	shipSymbol string,
	playerID int,
	agentSymbol string,
) (*DockResponse, error) {
	req := &pb.DockShipRequest{
		ShipSymbol: shipSymbol,
		PlayerId:   int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.DockShip(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return &DockResponse{
		ContainerID: resp.ContainerId,
		ShipSymbol:  resp.ShipSymbol,
		Status:      resp.Status,
	}, nil
}

// OrbitShip initiates ship orbit
func (c *DaemonClient) OrbitShip(
	ctx context.Context,
	shipSymbol string,
	playerID int,
	agentSymbol string,
) (*OrbitResponse, error) {
	req := &pb.OrbitShipRequest{
		ShipSymbol: shipSymbol,
		PlayerId:   int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.OrbitShip(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return &OrbitResponse{
		ContainerID: resp.ContainerId,
		ShipSymbol:  resp.ShipSymbol,
		Status:      resp.Status,
	}, nil
}

// RefuelShip initiates ship refuel
func (c *DaemonClient) RefuelShip(
	ctx context.Context,
	shipSymbol string,
	playerID int,
	agentSymbol string,
	units *int,
) (*RefuelResponse, error) {
	req := &pb.RefuelShipRequest{
		ShipSymbol: shipSymbol,
		PlayerId:   int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	if units != nil {
		u := int32(*units)
		req.Units = &u
	}

	resp, err := c.client.RefuelShip(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return &RefuelResponse{
		ContainerID: resp.ContainerId,
		ShipSymbol:  resp.ShipSymbol,
		FuelAdded:   resp.FuelAdded,
		CreditsCost: resp.CreditsCost,
		Status:      resp.Status,
	}, nil
}

// JumpShip executes a jump to a different star system via jump gate
func (c *DaemonClient) JumpShip(
	ctx context.Context,
	shipSymbol string,
	destinationSystem string,
	playerID int,
	agentSymbol string,
) (*JumpResponse, error) {
	req := &pb.JumpShipRequest{
		ShipSymbol:        shipSymbol,
		DestinationSystem: destinationSystem,
		PlayerId:          int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.JumpShip(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return &JumpResponse{
		Success:           resp.Success,
		NavigatedToGate:   resp.NavigatedToGate,
		JumpGateSymbol:    resp.JumpGateSymbol,
		DestinationSystem: resp.DestinationSystem,
		CooldownSeconds:   resp.CooldownSeconds,
		Message:           resp.Message,
		Error:             resp.Error,
	}, nil
}

// ModuleInfoDTO is a single ship module in a CLI response.
type ModuleInfoDTO struct {
	Symbol   string
	Name     string
	Capacity int
	Range    int
	// Power, Crew, and Slots are the module's own install requirements
	// (sp-el60) - what installing it draws from the ship's reactor power
	// budget, crew capacity, and module-slot budget respectively.
	Power int
	Crew  int
	Slots int
}

// ModuleModificationResponse is the CLI-side result of an install or remove.
type ModuleModificationResponse struct {
	Success       bool
	ShipSymbol    string
	ModuleSymbol  string
	CargoCapacity int
	Fee           int
	Modules       []ModuleInfoDTO
	Message       string
	Error         string
}

// ModuleFeasibilityDTO is the CLI-side offline install-feasibility verdict
// for a candidate module (sp-el60), populated only when the list request
// carried a candidate symbol.
//
// RequirementsKnown is true only when the candidate's own power/crew/slot
// requirements were actually resolved server-side - from another ship in
// the fleet that has the symbol installed, since there is no catalog of
// unowned module specs anywhere (sp-el60 acceptance fix). When false,
// RequirementsPower/Crew/Slots are unset/zero and CanInstall is always
// false; callers must present the requirements as "unknown", never as a
// real zero-cost spec, and must never report CAN-INSTALL.
type ModuleFeasibilityDTO struct {
	CandidateSymbol string
	CanInstall      bool
	PowerShort      int
	SlotShort       int
	CrewShort       int

	RequirementsKnown bool
	RequirementsPower int
	RequirementsCrew  int
	RequirementsSlots int
}

// ShipModulesResponse is the CLI-side result of listing a ship's modules,
// plus its power/slot/crew budget summary (sp-el60) computed offline from
// the DB-cached ship state.
type ShipModulesResponse struct {
	ShipSymbol string
	Modules    []ModuleInfoDTO
	Error      string

	ReactorPowerOutput int
	PowerUsed          int
	ModuleSlots        int
	ModuleSlotsUsed    int
	MountingPoints     int
	MountingPointsUsed int
	CrewCurrent        int
	CrewRequired       int
	CrewCapacity       int

	// Feasibility is populated only when the caller supplied a candidate symbol.
	Feasibility *ModuleFeasibilityDTO
}

func protoToModuleDTOs(modules []*pb.ShipModuleInfo) []ModuleInfoDTO {
	out := make([]ModuleInfoDTO, 0, len(modules))
	for _, m := range modules {
		out = append(out, ModuleInfoDTO{
			Symbol:   m.Symbol,
			Name:     m.Name,
			Capacity: int(m.Capacity),
			Range:    int(m.Range),
			Power:    int(m.Power),
			Crew:     int(m.Crew),
			Slots:    int(m.Slots),
		})
	}
	return out
}

// InstallModule installs a module (which must be in the ship's cargo) onto the ship.
func (c *DaemonClient) InstallModule(
	ctx context.Context,
	shipSymbol string,
	moduleSymbol string,
	playerID int,
	agentSymbol string,
) (*ModuleModificationResponse, error) {
	req := &pb.InstallModuleRequest{
		ShipSymbol:   shipSymbol,
		ModuleSymbol: moduleSymbol,
		PlayerId:     int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.InstallModule(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return &ModuleModificationResponse{
		Success:       resp.Success,
		ShipSymbol:    resp.ShipSymbol,
		ModuleSymbol:  resp.ModuleSymbol,
		CargoCapacity: int(resp.CargoCapacity),
		Fee:           int(resp.Fee),
		Modules:       protoToModuleDTOs(resp.Modules),
		Message:       resp.Message,
		Error:         resp.Error,
	}, nil
}

// RemoveModule removes an installed module from the ship back into its cargo.
func (c *DaemonClient) RemoveModule(
	ctx context.Context,
	shipSymbol string,
	moduleSymbol string,
	playerID int,
	agentSymbol string,
) (*ModuleModificationResponse, error) {
	req := &pb.RemoveModuleRequest{
		ShipSymbol:   shipSymbol,
		ModuleSymbol: moduleSymbol,
		PlayerId:     int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.RemoveModule(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return &ModuleModificationResponse{
		Success:       resp.Success,
		ShipSymbol:    resp.ShipSymbol,
		ModuleSymbol:  resp.ModuleSymbol,
		CargoCapacity: int(resp.CargoCapacity),
		Fee:           int(resp.Fee),
		Modules:       protoToModuleDTOs(resp.Modules),
		Message:       resp.Message,
		Error:         resp.Error,
	}, nil
}

// ListShipModules lists the modules installed on a ship, along with its
// power/slot/crew budget summary computed offline from cached ship state
// (sp-el60). When candidateSymbol is non-empty, the response also carries an
// offline install-feasibility verdict for that not-yet-installed module. The
// candidate's own power/crew/slot requirements are resolved server-side, not
// supplied by the caller (sp-el60 acceptance fix) — there is no catalog of
// unowned module specs anywhere, so the only real data source is another
// ship in the fleet that has the symbol installed. See
// ModuleFeasibilityDTO.RequirementsKnown for the fail-closed signal when no
// ship anywhere ever has.
func (c *DaemonClient) ListShipModules(
	ctx context.Context,
	shipSymbol string,
	playerID int,
	agentSymbol string,
	candidateSymbol string,
) (*ShipModulesResponse, error) {
	req := &pb.ListShipModulesRequest{
		ShipSymbol: shipSymbol,
		PlayerId:   int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	if candidateSymbol != "" {
		req.CandidateSymbol = &candidateSymbol
	}

	resp, err := c.client.ListShipModules(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	out := &ShipModulesResponse{
		ShipSymbol: resp.ShipSymbol,
		Modules:    protoToModuleDTOs(resp.Modules),
		Error:      resp.Error,

		ReactorPowerOutput: int(resp.ReactorPowerOutput),
		PowerUsed:          int(resp.PowerUsed),
		ModuleSlots:        int(resp.ModuleSlots),
		ModuleSlotsUsed:    int(resp.ModuleSlotsUsed),
		MountingPoints:     int(resp.MountingPoints),
		MountingPointsUsed: int(resp.MountingPointsUsed),
		CrewCurrent:        int(resp.CrewCurrent),
		CrewRequired:       int(resp.CrewRequired),
		CrewCapacity:       int(resp.CrewCapacity),
	}

	if f := resp.Feasibility; f != nil {
		out.Feasibility = &ModuleFeasibilityDTO{
			CandidateSymbol:   f.CandidateSymbol,
			CanInstall:        f.CanInstall,
			PowerShort:        int(f.PowerShort),
			SlotShort:         int(f.SlotShort),
			CrewShort:         int(f.CrewShort),
			RequirementsKnown: f.RequirementsKnown,
			RequirementsPower: int(f.RequirementsPower),
			RequirementsCrew:  int(f.RequirementsCrew),
			RequirementsSlots: int(f.RequirementsSlots),
		}
	}

	return out, nil
}

// JettisonCargo jettisons cargo from a ship
func (c *DaemonClient) JettisonCargo(
	ctx context.Context,
	shipSymbol string,
	goodSymbol string,
	units int,
	playerID int,
	agentSymbol string,
) (*JettisonResponse, error) {
	req := &pb.JettisonCargoRequest{
		ShipSymbol: shipSymbol,
		GoodSymbol: goodSymbol,
		Units:      int32(units),
		PlayerId:   int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.JettisonCargo(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return &JettisonResponse{
		ContainerID:     resp.ContainerId,
		ShipSymbol:      resp.ShipSymbol,
		GoodSymbol:      resp.GoodSymbol,
		UnitsJettisoned: resp.UnitsJettisoned,
		Status:          resp.Status,
		Message:         resp.Message,
	}, nil
}

// BatchContractWorkflow initiates batch contract workflow
func (c *DaemonClient) BatchContractWorkflow(
	ctx context.Context,
	shipSymbol string,
	playerID int,
	agentSymbol string,
) (*BatchContractWorkflowResponse, error) {
	req := &pb.BatchContractWorkflowRequest{
		ShipSymbol: shipSymbol,
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

// ScoutTour initiates market scouting tour (single ship)
func (c *DaemonClient) ScoutTour(
	ctx context.Context,
	shipSymbol string,
	markets []string,
	iterations int,
	playerID int,
	agentSymbol string,
) (*ScoutTourResponse, error) {
	req := &pb.ScoutTourRequest{
		ShipSymbol: shipSymbol,
		Markets:    markets,
		Iterations: int32(iterations),
		PlayerId:   int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.ScoutTour(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return &ScoutTourResponse{
		ContainerID: resp.ContainerId,
		ShipSymbol:  resp.ShipSymbol,
		Markets:     resp.Markets,
		Iterations:  int(resp.Iterations),
		Status:      resp.Status,
	}, nil
}

// ScoutMarkets initiates fleet market scouting with VRP optimization (multi-ship)
func (c *DaemonClient) ScoutMarkets(
	ctx context.Context,
	shipSymbols []string,
	systemSymbol string,
	markets []string,
	iterations int,
	playerID int,
	agentSymbol string,
) (*ScoutMarketsResponse, error) {
	req := &pb.ScoutMarketsRequest{
		ShipSymbols:  shipSymbols,
		SystemSymbol: systemSymbol,
		Markets:      markets,
		Iterations:   int32(iterations),
		PlayerId:     int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.ScoutMarkets(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	// Convert protobuf response to client response type
	assignments := make(map[string]*MarketAssignment)
	for ship, pbAssignment := range resp.Assignments {
		assignments[ship] = &MarketAssignment{
			Markets: pbAssignment.Markets,
		}
	}

	return &ScoutMarketsResponse{
		ContainerIDs:     resp.ContainerIds,
		Assignments:      assignments,
		ReusedContainers: resp.ReusedContainers,
	}, nil
}

// AssignScoutingFleetResponse contains the fleet-assignment container ID
type AssignScoutingFleetResponse struct {
	ContainerID string
}

// AssignScoutingFleet creates a fleet-assignment container for async VRP optimization
func (c *DaemonClient) AssignScoutingFleet(
	ctx context.Context,
	systemSymbol string,
	playerID int,
	agentSymbol string,
) (*AssignScoutingFleetResponse, error) {
	req := &pb.AssignScoutingFleetRequest{
		SystemSymbol: systemSymbol,
		PlayerId:     int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.AssignScoutingFleet(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return &AssignScoutingFleetResponse{
		ContainerID: resp.ContainerId,
	}, nil
}

// ListContainers lists all containers
func (c *DaemonClient) ListContainers(
	ctx context.Context,
	playerID *int,
	status *string,
) ([]*ContainerInfo, error) {
	req := &pb.ListContainersRequest{}
	if playerID != nil {
		p := int32(*playerID)
		req.PlayerId = &p
	}
	if status != nil {
		req.Status = status
	}

	resp, err := c.client.ListContainers(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	// Convert to client response type
	containers := make([]*ContainerInfo, 0, len(resp.Containers))
	for _, pbCont := range resp.Containers {
		containers = append(containers, &ContainerInfo{
			ContainerID:      pbCont.ContainerId,
			ContainerType:    pbCont.ContainerType,
			Status:           pbCont.Status,
			PlayerID:         pbCont.PlayerId,
			CreatedAt:        pbCont.CreatedAt,
			UpdatedAt:        pbCont.UpdatedAt,
			CurrentIteration: pbCont.CurrentIteration,
			MaxIterations:    pbCont.MaxIterations,
			RestartCount:     pbCont.RestartCount,
		})
	}

	return containers, nil
}

// GetContainer retrieves container details
func (c *DaemonClient) GetContainer(
	ctx context.Context,
	containerID string,
) (*ContainerInfo, error) {
	req := &pb.GetContainerRequest{
		ContainerId: containerID,
	}

	resp, err := c.client.GetContainer(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	pbCont := resp.Container
	return &ContainerInfo{
		ContainerID:      pbCont.ContainerId,
		ContainerType:    pbCont.ContainerType,
		Status:           pbCont.Status,
		PlayerID:         pbCont.PlayerId,
		CreatedAt:        pbCont.CreatedAt,
		UpdatedAt:        pbCont.UpdatedAt,
		CurrentIteration: pbCont.CurrentIteration,
		MaxIterations:    pbCont.MaxIterations,
		RestartCount:     pbCont.RestartCount,
		Metadata:         resp.Metadata,
	}, nil
}

// StopContainer stops a container
func (c *DaemonClient) StopContainer(
	ctx context.Context,
	containerID string,
) (*StopContainerResponse, error) {
	req := &pb.StopContainerRequest{
		ContainerId: containerID,
	}

	resp, err := c.client.StopContainer(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return &StopContainerResponse{
		ContainerID: resp.ContainerId,
		Status:      resp.Status,
		Message:     resp.Message,
	}, nil
}

// GetContainerLogs retrieves container logs
func (c *DaemonClient) GetContainerLogs(
	ctx context.Context,
	containerID string,
	limit *int,
	level *string,
) ([]*LogEntry, error) {
	req := &pb.GetContainerLogsRequest{
		ContainerId: containerID,
	}
	if limit != nil {
		l := int32(*limit)
		req.Limit = &l
	}
	if level != nil {
		req.Level = level
	}

	resp, err := c.client.GetContainerLogs(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	// Convert to client response type
	logs := make([]*LogEntry, 0, len(resp.Logs))
	for _, pbLog := range resp.Logs {
		logs = append(logs, &LogEntry{
			Timestamp: pbLog.Timestamp,
			Level:     pbLog.Level,
			Message:   pbLog.Message,
			Metadata:  pbLog.Metadata,
		})
	}

	return logs, nil
}

// HealthCheck verifies daemon health
func (c *DaemonClient) HealthCheck(ctx context.Context) (*HealthResponse, error) {
	req := &pb.HealthCheckRequest{}

	resp, err := c.client.HealthCheck(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return &HealthResponse{
		Status:           resp.Status,
		Version:          resp.Version,
		ActiveContainers: resp.ActiveContainers,
	}, nil
}

// GetAPIBudget retrieves API request-budget observability (sp-51ti):
// per-hull req/s, global utilization vs the rate ceiling, and the
// duty-cycle KPI (ship-hours earning/day per hull).
func (c *DaemonClient) GetAPIBudget(ctx context.Context) (*pb.GetAPIBudgetResponse, error) {
	req := &pb.GetAPIBudgetRequest{}

	resp, err := c.client.GetAPIBudget(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}

// ListShips lists all ships for a player
func (c *DaemonClient) ListShips(ctx context.Context, playerID *int32, agentSymbol *string) (*pb.ListShipsResponse, error) {
	req := &pb.ListShipsRequest{
		PlayerId:    playerID,
		AgentSymbol: agentSymbol,
	}

	resp, err := c.client.ListShips(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}

// GetShip gets detailed ship information
func (c *DaemonClient) GetShip(ctx context.Context, shipSymbol string, playerID *int32, agentSymbol *string) (*pb.GetShipResponse, error) {
	req := &pb.GetShipRequest{
		ShipSymbol:  shipSymbol,
		PlayerId:    playerID,
		AgentSymbol: agentSymbol,
	}

	resp, err := c.client.GetShip(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}

// RefreshShip forces a resync of a ship from the API, overwriting the daemon cache
func (c *DaemonClient) RefreshShip(ctx context.Context, shipSymbol string, playerID *int32, agentSymbol *string) (*pb.RefreshShipResponse, error) {
	req := &pb.RefreshShipRequest{
		ShipSymbol:  shipSymbol,
		PlayerId:    playerID,
		AgentSymbol: agentSymbol,
	}

	resp, err := c.client.RefreshShip(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}

// ReserveShip reserves a ship for the captain's direct manual use, hiding it
// from every coordinator's assignment discovery (sp-i1ku)
func (c *DaemonClient) ReserveShip(ctx context.Context, shipSymbol string, reason *string, playerID *int32, agentSymbol *string) (*pb.ReserveShipResponse, error) {
	req := &pb.ReserveShipRequest{
		ShipSymbol:  shipSymbol,
		Reason:      reason,
		PlayerId:    playerID,
		AgentSymbol: agentSymbol,
	}

	resp, err := c.client.ReserveShip(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}

// ReleaseShip clears a captain reservation, returning the ship to idle so
// normal coordinator discovery can claim it again (sp-i1ku)
func (c *DaemonClient) ReleaseShip(ctx context.Context, shipSymbol string, reason *string, playerID *int32, agentSymbol *string) (*pb.ReleaseShipResponse, error) {
	req := &pb.ReleaseShipRequest{
		ShipSymbol:  shipSymbol,
		Reason:      reason,
		PlayerId:    playerID,
		AgentSymbol: agentSymbol,
	}

	resp, err := c.client.ReleaseShip(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}

// AssignShipFleet dedicates a ship to a named fleet, making it exclusive to
// that coordinator's discovery (sp-l7h2)
func (c *DaemonClient) AssignShipFleet(ctx context.Context, shipSymbol, fleet string, playerID *int32, agentSymbol *string) (*pb.AssignShipFleetResponse, error) {
	req := &pb.AssignShipFleetRequest{
		ShipSymbol:  shipSymbol,
		Fleet:       fleet,
		PlayerId:    playerID,
		AgentSymbol: agentSymbol,
	}

	resp, err := c.client.AssignShipFleet(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}

// UnassignShipFleet clears a ship's fleet dedication, returning it to the
// general pool (sp-l7h2)
func (c *DaemonClient) UnassignShipFleet(ctx context.Context, shipSymbol string, playerID *int32, agentSymbol *string) (*pb.UnassignShipFleetResponse, error) {
	req := &pb.UnassignShipFleetRequest{
		ShipSymbol:  shipSymbol,
		PlayerId:    playerID,
		AgentSymbol: agentSymbol,
	}

	resp, err := c.client.UnassignShipFleet(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}

// ListFleets lists every dedicated fleet and its member ships (sp-l7h2)
func (c *DaemonClient) ListFleets(ctx context.Context, playerID *int32, agentSymbol *string) (*pb.ListFleetsResponse, error) {
	req := &pb.ListFleetsRequest{
		PlayerId:    playerID,
		AgentSymbol: agentSymbol,
	}

	resp, err := c.client.ListFleets(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}

// ListWaypoints lists the waypoints of a system from the daemon's waypoint cache
func (c *DaemonClient) ListWaypoints(ctx context.Context, systemSymbol string, trait, waypointType *string, playerID *int32, agentSymbol *string) (*pb.ListWaypointsResponse, error) {
	req := &pb.ListWaypointsRequest{
		SystemSymbol: systemSymbol,
		Trait:        trait,
		Type:         waypointType,
		PlayerId:     playerID,
		AgentSymbol:  agentSymbol,
	}

	resp, err := c.client.ListWaypoints(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}

// GetWaypoint gets the detail of a single waypoint
func (c *DaemonClient) GetWaypoint(ctx context.Context, waypointSymbol string, playerID *int32, agentSymbol *string) (*pb.GetWaypointResponse, error) {
	req := &pb.GetWaypointRequest{
		WaypointSymbol: waypointSymbol,
		PlayerId:       playerID,
		AgentSymbol:    agentSymbol,
	}

	resp, err := c.client.GetWaypoint(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}

// GetShipyardListings gets shipyard listings at a waypoint
func (c *DaemonClient) GetShipyardListings(ctx context.Context, systemSymbol, waypointSymbol string, playerID int) (*pb.GetShipyardListingsResponse, error) {
	req := &pb.GetShipyardListingsRequest{
		SystemSymbol:   systemSymbol,
		WaypointSymbol: waypointSymbol,
		PlayerId:       int32(playerID),
	}

	resp, err := c.client.GetShipyardListings(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}

// PurchaseShip purchases a ship from a shipyard
func (c *DaemonClient) PurchaseShip(ctx context.Context, purchasingShipSymbol, shipType string, playerID int, agentSymbol, shipyardWaypoint string) (*pb.PurchaseShipResponse, error) {
	req := &pb.PurchaseShipRequest{
		PurchasingShipSymbol: purchasingShipSymbol,
		ShipType:             shipType,
		PlayerId:             int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	if shipyardWaypoint != "" {
		req.ShipyardWaypoint = &shipyardWaypoint
	}

	resp, err := c.client.PurchaseShip(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}

// BatchPurchaseShips purchases multiple ships in batch
func (c *DaemonClient) BatchPurchaseShips(ctx context.Context, purchasingShipSymbol, shipType string, quantity, maxBudget, playerID int, agentSymbol, shipyardWaypoint string) (*pb.BatchPurchaseShipsResponse, error) {
	req := &pb.BatchPurchaseShipsRequest{
		PurchasingShipSymbol: purchasingShipSymbol,
		ShipType:             shipType,
		Quantity:             int32(quantity),
		MaxBudget:            int32(maxBudget),
		PlayerId:             int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	if shipyardWaypoint != "" {
		req.ShipyardWaypoint = &shipyardWaypoint
	}

	resp, err := c.client.BatchPurchaseShips(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return resp, nil
}

// ContractFleetCoordinator starts a contract fleet coordinator
// Uses all available idle light hauler ships (no pre-assignment needed).
//
// dedicatedShips/standbyStations (sp-snmb) carry the operator's optional
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

// AddScoutPost adds or updates a desired-state scout post (sp-cxpq). hulls is the
// probe budget N (sp-enry); 0 defaults to single-hull.
func (c *DaemonClient) AddScoutPost(ctx context.Context, playerID int, agentSymbol, systemSymbol string, freshnessSeconds int, kind string, hulls int) (*ScoutPost, error) {
	req := &pb.AddScoutPostRequest{
		PlayerId:         int32(playerID),
		SystemSymbol:     systemSymbol,
		FreshnessSeconds: int32(freshnessSeconds),
		Kind:             kind,
		Hulls:            int32(hulls),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	resp, err := c.client.AddScoutPost(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}
	return protoToScoutPost(resp.Post), nil
}

// RemoveScoutPost removes a scout post (sp-cxpq).
func (c *DaemonClient) RemoveScoutPost(ctx context.Context, playerID int, agentSymbol, systemSymbol string) error {
	req := &pb.RemoveScoutPostRequest{
		PlayerId:     int32(playerID),
		SystemSymbol: systemSymbol,
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	if _, err := c.client.RemoveScoutPost(ctx, req); err != nil {
		return fmt.Errorf(grpcCallFailed, err)
	}
	return nil
}

// ListScoutPosts lists the active scout posts (sp-cxpq).
func (c *DaemonClient) ListScoutPosts(ctx context.Context, playerID int, agentSymbol string) ([]*ScoutPost, error) {
	req := &pb.ListScoutPostsRequest{PlayerId: int32(playerID)}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	resp, err := c.client.ListScoutPosts(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}
	posts := make([]*ScoutPost, len(resp.Posts))
	for i, p := range resp.Posts {
		posts[i] = protoToScoutPost(p)
	}
	return posts, nil
}

func protoToScoutPost(p *pb.ScoutPost) *ScoutPost {
	if p == nil {
		return nil
	}
	return &ScoutPost{
		SystemSymbol:     p.SystemSymbol,
		FreshnessSeconds: int(p.FreshnessSeconds),
		Kind:             p.Kind,
		AssignedHull:     p.AssignedHull,
		TourContainerID:  p.TourContainerId,
		Hulls:            int(p.Hulls),
		MannedCount:      int(p.MannedCount),
	}
}

// StartGoodsFactory starts a goods factory for automated production
// StartTradeRoute launches a single-hull pure-arbitrage circuit as a recovery-safe
// daemon container (sp-zewt). Replaces the old in-process CLI runner.
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

// StartArbRunResult reports the container started for a one-shot guarded arb run (sp-p4ua).
type StartArbRunResult struct {
	ContainerID string
	ShipSymbol  string
	Good        string
	BuyAt       string
	SellAt      string
	Status      string
	Message     string
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
// replan_limit→2, iterations→one tour). iterations=-1 makes it CONTINUOUS (sp-m5kv):
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

// StartArbRun asks the daemon to launch a one-shot, captain-directed, guarded arbitrage
// run as a recovery-safe container (sp-p4ua). maxUnits/maxSpend/minMargin/workingCapitalReserve
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

// StartStockerResult reports the container started for a stocker loop (sp-zdwg).
type StartStockerResult struct {
	ContainerID       string
	ShipSymbol        string
	WarehouseWaypoint string
	Status            string
	Message           string
}

// StartStocker asks the daemon to launch the STOCKER LOOP (sp-zdwg) as a recovery-safe
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

func (c *DaemonClient) StartGoodsFactory(
	ctx context.Context,
	targetGood string,
	systemSymbol *string,
	playerID int,
	agentSymbol *string,
	maxIterations *int32,
	inputsOnly bool,
) (*StartGoodsFactoryResult, error) {
	resp, err := c.client.StartGoodsFactory(ctx, &pb.StartGoodsFactoryRequest{
		PlayerId:      int32(playerID),
		TargetGood:    targetGood,
		SystemSymbol:  systemSymbol,
		AgentSymbol:   agentSymbol,
		MaxIterations: maxIterations,
		InputsOnly:    inputsOnly,
	})
	if err != nil {
		return nil, err
	}

	return &StartGoodsFactoryResult{
		FactoryID:  resp.FactoryId,
		TargetGood: resp.TargetGood,
		Status:     resp.Status,
		Message:    resp.Message,
		NodesTotal: int(resp.NodesTotal),
	}, nil
}

// StopGoodsFactory stops a running goods factory
func (c *DaemonClient) StopGoodsFactory(
	ctx context.Context,
	factoryID string,
	playerID int,
) (*StopGoodsFactoryResult, error) {
	resp, err := c.client.StopGoodsFactory(ctx, &pb.StopGoodsFactoryRequest{
		PlayerId:  int32(playerID),
		FactoryId: factoryID,
	})
	if err != nil {
		return nil, err
	}

	return &StopGoodsFactoryResult{
		FactoryID: resp.FactoryId,
		Status:    resp.Status,
		Message:   resp.Message,
	}, nil
}

// GetFactoryStatus retrieves the status of a goods factory
func (c *DaemonClient) GetFactoryStatus(
	ctx context.Context,
	factoryID string,
	playerID int,
) (*GoodsFactoryStatusResult, error) {
	resp, err := c.client.GetFactoryStatus(ctx, &pb.GetFactoryStatusRequest{
		PlayerId:  int32(playerID),
		FactoryId: factoryID,
	})
	if err != nil {
		return nil, err
	}

	return &GoodsFactoryStatusResult{
		FactoryID:        resp.FactoryId,
		TargetGood:       resp.TargetGood,
		Status:           resp.Status,
		DependencyTree:   resp.DependencyTree,
		QuantityAcquired: int(resp.QuantityAcquired),
		TotalCost:        int(resp.TotalCost),
		NodesCompleted:   int(resp.NodesCompleted),
		NodesTotal:       int(resp.NodesTotal),
		SystemSymbol:     resp.SystemSymbol,
		ShipsUsed:        int(resp.ShipsUsed),
		MarketQueries:    int(resp.MarketQueries),
		ParallelLevels:   int(resp.ParallelLevels),
		EstimatedSpeedup: float64(resp.EstimatedSpeedup),
	}, nil
}

// StartGoodsFactoryResult contains the result of starting a goods factory
type StartGoodsFactoryResult struct {
	FactoryID  string
	TargetGood string
	Status     string
	Message    string
	NodesTotal int
}

// StartTradeRouteResult contains the result of starting a trade-route container
type StartTradeRouteResult struct {
	ContainerID  string
	ShipSymbol   string
	SystemSymbol string
	Status       string
	Message      string
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

// StopGoodsFactoryResult contains the result of stopping a goods factory
type StopGoodsFactoryResult struct {
	FactoryID string
	Status    string
	Message   string
}

// GoodsFactoryStatusResult contains detailed status of a goods factory
type GoodsFactoryStatusResult struct {
	FactoryID        string
	TargetGood       string
	Status           string
	DependencyTree   string
	QuantityAcquired int
	TotalCost        int
	NodesCompleted   int
	NodesTotal       int
	SystemSymbol     string
	ShipsUsed        int
	MarketQueries    int
	ParallelLevels   int
	EstimatedSpeedup float64
}

// StartManufacturingCoordinatorResult contains the result of starting a manufacturing coordinator
type StartManufacturingCoordinatorResult struct {
	ContainerID  string
	SystemSymbol string
	MinPrice     int
	MaxWorkers   int
	MaxPipelines int
	MinBalance   int
	Status       string
	Message      string
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

// StartParallelManufacturingCoordinator starts a parallel task-based manufacturing coordinator
func (c *DaemonClient) StartParallelManufacturingCoordinator(
	ctx context.Context,
	systemSymbol string,
	playerID int,
	minPrice int,
	maxWorkers int,
	maxPipelines int,
	maxCollectionPipelines int,
	minBalance int,
	strategy string,
) (*StartManufacturingCoordinatorResult, error) {
	resp, err := c.client.StartParallelManufacturingCoordinator(ctx, &pb.StartParallelManufacturingCoordinatorRequest{
		PlayerId:               int32(playerID),
		SystemSymbol:           systemSymbol,
		MinPrice:               int32(minPrice),
		MaxWorkers:             int32(maxWorkers),
		MaxPipelines:           int32(maxPipelines),
		MaxCollectionPipelines: int32(maxCollectionPipelines),
		MinBalance:             int32(minBalance),
		Strategy:               strategy,
	})
	if err != nil {
		return nil, err
	}

	return &StartManufacturingCoordinatorResult{
		ContainerID:  resp.ContainerId,
		SystemSymbol: resp.SystemSymbol,
		MinPrice:     int(resp.MinPrice),
		MaxWorkers:   int(resp.MaxWorkers),
		MaxPipelines: int(resp.MaxPipelines),
		MinBalance:   int(resp.MinBalance),
		Status:       resp.Status,
		Message:      resp.Message,
	}, nil
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

	// Convert ship routes from protobuf
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

// StartConstructionPipelineResponse contains the result of starting a construction pipeline
type StartConstructionPipelineResponse struct {
	PipelineID       string
	ConstructionSite string
	IsResumed        bool
	Materials        []*ConstructionMaterialResponse
	TaskCount        int32
	Status           string
	Message          string

	// DeferredMaterials names every material (trade symbol) that could not be
	// sourced this call (sp-560b/sp-ooba), so the CLI can report the gap by
	// name instead of a generic "no market" message.
	DeferredMaterials []string
}

// ConstructionMaterialResponse represents a construction material status
type ConstructionMaterialResponse struct {
	TradeSymbol string
	Required    int32
	Fulfilled   int32
	Remaining   int32
	Progress    float64
}

// GetConstructionStatusResponse contains the status of a construction site
type GetConstructionStatusResponse struct {
	ConstructionSite string
	IsComplete       bool
	Progress         float64
	Materials        []*ConstructionMaterialResponse
	PipelineID       *string
	PipelineStatus   *string
	PipelineProgress *float64
}

// StartConstructionPipeline starts a pipeline to supply materials to a construction site
func (c *DaemonClient) StartConstructionPipeline(
	ctx context.Context,
	constructionSite string,
	playerID int32,
	agentSymbol *string,
	supplyChainDepth int32,
	maxWorkers int32,
	systemSymbol *string,
	minSupply *string,
) (*StartConstructionPipelineResponse, error) {
	req := &pb.StartConstructionPipelineRequest{
		ConstructionSite: constructionSite,
		PlayerId:         playerID,
		AgentSymbol:      agentSymbol,
		SupplyChainDepth: supplyChainDepth,
		MaxWorkers:       maxWorkers,
		SystemSymbol:     systemSymbol,
		MinSupply:        minSupply,
	}

	resp, err := c.client.StartConstructionPipeline(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	// Convert materials
	materials := make([]*ConstructionMaterialResponse, len(resp.Materials))
	for i, mat := range resp.Materials {
		materials[i] = &ConstructionMaterialResponse{
			TradeSymbol: mat.TradeSymbol,
			Required:    mat.Required,
			Fulfilled:   mat.Fulfilled,
			Remaining:   mat.Remaining,
			Progress:    mat.Progress,
		}
	}

	return &StartConstructionPipelineResponse{
		PipelineID:        resp.PipelineId,
		ConstructionSite:  resp.ConstructionSite,
		IsResumed:         resp.IsResumed,
		Materials:         materials,
		TaskCount:         resp.TaskCount,
		Status:            resp.Status,
		Message:           resp.Message,
		DeferredMaterials: resp.DeferredMaterials,
	}, nil
}

// GetConstructionStatus retrieves the status of a construction site
func (c *DaemonClient) GetConstructionStatus(
	ctx context.Context,
	constructionSite string,
	playerID int32,
	agentSymbol *string,
) (*GetConstructionStatusResponse, error) {
	req := &pb.GetConstructionStatusRequest{
		ConstructionSite: constructionSite,
		PlayerId:         playerID,
		AgentSymbol:      agentSymbol,
	}

	resp, err := c.client.GetConstructionStatus(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	// Convert materials
	materials := make([]*ConstructionMaterialResponse, len(resp.Materials))
	for i, mat := range resp.Materials {
		materials[i] = &ConstructionMaterialResponse{
			TradeSymbol: mat.TradeSymbol,
			Required:    mat.Required,
			Fulfilled:   mat.Fulfilled,
			Remaining:   mat.Remaining,
			Progress:    mat.Progress,
		}
	}

	return &GetConstructionStatusResponse{
		ConstructionSite: resp.ConstructionSite,
		IsComplete:       resp.IsComplete,
		Progress:         resp.Progress,
		Materials:        materials,
		PipelineID:       resp.PipelineId,
		PipelineStatus:   resp.PipelineStatus,
		PipelineProgress: resp.PipelineProgress,
	}, nil
}

// StopConstructionPipelineResponse contains the result of stopping a construction pipeline
type StopConstructionPipelineResponse struct {
	PipelineID       string
	ConstructionSite string
	Status           string
	TasksCancelled   int32
	Message          string
}

// StopConstructionPipeline cancels the active construction pipeline for a site (sp-yzrv)
func (c *DaemonClient) StopConstructionPipeline(
	ctx context.Context,
	constructionSite string,
	playerID int32,
	agentSymbol *string,
) (*StopConstructionPipelineResponse, error) {
	req := &pb.StopConstructionPipelineRequest{
		ConstructionSite: constructionSite,
		PlayerId:         playerID,
		AgentSymbol:      agentSymbol,
	}

	resp, err := c.client.StopConstructionPipeline(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return &StopConstructionPipelineResponse{
		PipelineID:       resp.PipelineId,
		ConstructionSite: resp.ConstructionSite,
		Status:           resp.Status,
		TasksCancelled:   resp.TasksCancelled,
		Message:          resp.Message,
	}, nil
}
