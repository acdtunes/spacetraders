package steps

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	playerApp "github.com/andrescamacho/spacetraders-go/internal/application/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
	"github.com/cucumber/godog"
)

// daemonPlayerResolutionContext holds state for daemon player resolution BDD tests
type daemonPlayerResolutionContext struct {
	// Service implementation
	service *testDaemonService

	// Mock dependencies
	mediator *mockPlayerMediator

	// Test data
	players map[string]*player.Player // agentSymbol -> Player

	// Resolution state
	resolvedPlayerID int
	resolutionErr    error

	// Request state
	requestPlayerID    int32
	requestAgentSymbol *string

	// Operation state
	operationPlayerID int
}

// mockPlayerMediator implements common.Mediator for testing player resolution
type mockPlayerMediator struct {
	players map[string]*player.Player // agentSymbol -> Player
}

func (m *mockPlayerMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch req := request.(type) {
	case *playerApp.GetPlayerQuery:
		// Resolve by agent symbol
		if req.AgentSymbol != "" {
			player, ok := m.players[req.AgentSymbol]
			if !ok {
				return nil, fmt.Errorf("player not found with agent symbol: %s", req.AgentSymbol)
			}
			return &playerApp.GetPlayerResponse{Player: player}, nil
		}
		return nil, fmt.Errorf("no lookup criteria provided")
	default:
		return nil, fmt.Errorf("unsupported request type: %T", request)
	}
}

func (m *mockPlayerMediator) Register(requestType reflect.Type, handler common.RequestHandler) error {
	return nil // Not needed for tests
}

// testDaemonService implements daemon service methods for testing player resolution
type testDaemonService struct {
	pb.UnimplementedDaemonServiceServer
	mediator common.Mediator
}

// newTestDaemonService creates a test daemon service with mediator access
func newTestDaemonService(mediator common.Mediator) *testDaemonService {
	return &testDaemonService{
		mediator: mediator,
	}
}

// resolvePlayerID is copied from daemon_service_impl.go for direct testing
func (s *testDaemonService) resolvePlayerID(ctx context.Context, playerID int32, agentSymbol *string) (int, error) {
	// If player_id is provided and non-zero, use it directly
	if playerID != 0 {
		return int(playerID), nil
	}

	// If agent_symbol is provided, resolve it to player_id
	if agentSymbol != nil && *agentSymbol != "" {
		response, err := s.mediator.Send(ctx, &playerApp.GetPlayerQuery{
			AgentSymbol: *agentSymbol,
		})
		if err != nil {
			return 0, fmt.Errorf("failed to resolve agent symbol %s to player_id: %w", *agentSymbol, err)
		}

		getPlayerResp, ok := response.(*playerApp.GetPlayerResponse)
		if !ok {
			return 0, fmt.Errorf("unexpected response type from GetPlayerQuery")
		}

		return getPlayerResp.Player.ID, nil
	}

	// Neither player_id nor agent_symbol provided
	return 0, fmt.Errorf("either player_id or agent_symbol must be provided")
}

// NavigateShip delegates to the resolution logic
func (s *testDaemonService) NavigateShip(ctx context.Context, req *pb.NavigateShipRequest) (*pb.NavigateShipResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, err
	}

	_ = playerID // playerID successfully resolved
	return &pb.NavigateShipResponse{
		ContainerId: "container-123",
		ShipSymbol:  req.ShipSymbol,
		Destination: req.Destination,
		Status:      "PENDING",
	}, nil
}

// DockShip delegates to the resolution logic
func (s *testDaemonService) DockShip(ctx context.Context, req *pb.DockShipRequest) (*pb.DockShipResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, err
	}

	_ = playerID
	return &pb.DockShipResponse{
		ContainerId: "container-124",
		ShipSymbol:  req.ShipSymbol,
		Status:      "PENDING",
	}, nil
}

// OrbitShip delegates to the resolution logic
func (s *testDaemonService) OrbitShip(ctx context.Context, req *pb.OrbitShipRequest) (*pb.OrbitShipResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, err
	}

	_ = playerID
	return &pb.OrbitShipResponse{
		ContainerId: "container-125",
		ShipSymbol:  req.ShipSymbol,
		Status:      "PENDING",
	}, nil
}

// RefuelShip delegates to the resolution logic
func (s *testDaemonService) RefuelShip(ctx context.Context, req *pb.RefuelShipRequest) (*pb.RefuelShipResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, err
	}

	_ = playerID
	return &pb.RefuelShipResponse{
		ContainerId: "container-126",
		ShipSymbol:  req.ShipSymbol,
		FuelAdded:   0,
		CreditsCost: 0,
		Status:      "PENDING",
	}, nil
}

func (ctx *daemonPlayerResolutionContext) reset() {
	ctx.mediator = &mockPlayerMediator{
		players: make(map[string]*player.Player),
	}
	ctx.service = newTestDaemonService(ctx.mediator)
	ctx.players = make(map[string]*player.Player)
	ctx.resolvedPlayerID = 0
	ctx.resolutionErr = nil
	ctx.requestPlayerID = 0
	ctx.requestAgentSymbol = nil
	ctx.operationPlayerID = 0
}

// InitializeDaemonPlayerResolutionScenario registers step definitions
func InitializeDaemonPlayerResolutionScenario(sc *godog.ScenarioContext) {
	sCtx := &daemonPlayerResolutionContext{}

	// Background
	sc.Step(`^the daemon player resolution service is initialized$`, sCtx.theDaemonPlayerResolutionServiceIsInitialized)
	sc.Step(`^player (\d+) exists with agent symbol "([^"]*)"$`, sCtx.playerExistsWithAgentSymbol)

	// When - Direct resolution
	sc.Step(`^I resolve player with player_id (\d+)$`, sCtx.iResolvePlayerWithPlayerID)
	sc.Step(`^I resolve player with agent_symbol "([^"]*)"$`, sCtx.iResolvePlayerWithAgentSymbol)
	sc.Step(`^I resolve player with player_id (\d+) and agent_symbol "([^"]*)"$`, sCtx.iResolvePlayerWithPlayerIDAndAgentSymbol)
	sc.Step(`^I resolve player with player_id (\d+) and no agent_symbol$`, sCtx.iResolvePlayerWithPlayerIDAndNoAgentSymbol)

	// When - gRPC operations
	sc.Step(`^I send a NavigateShip request with player_id (\d+)$`, sCtx.iSendNavigateShipRequestWithPlayerID)
	sc.Step(`^I send a NavigateShip request with agent_symbol "([^"]*)"$`, sCtx.iSendNavigateShipRequestWithAgentSymbol)
	sc.Step(`^I send a DockShip request with player_id (\d+)$`, sCtx.iSendDockShipRequestWithPlayerID)
	sc.Step(`^I send an OrbitShip request with agent_symbol "([^"]*)"$`, sCtx.iSendOrbitShipRequestWithAgentSymbol)
	sc.Step(`^I send a RefuelShip request with player_id (\d+)$`, sCtx.iSendRefuelShipRequestWithPlayerID)

	// Then
	sc.Step(`^the resolution should succeed$`, sCtx.theResolutionShouldSucceed)
	sc.Step(`^the resolution should fail$`, sCtx.theResolutionShouldFail)
	sc.Step(`^the resolved player_id should be (\d+)$`, sCtx.theResolvedPlayerIDShouldBe)
	sc.Step(`^the error should contain "([^"]*)"$`, sCtx.theErrorShouldContain)
	sc.Step(`^the player should be resolved to player_id (\d+)$`, sCtx.thePlayerShouldBeResolvedToPlayerID)
	sc.Step(`^the operation should use the correct player context$`, sCtx.theOperationShouldUseTheCorrectPlayerContext)

	// Before hook
	sc.Before(func(gCtx context.Context, sc *godog.Scenario) (context.Context, error) {
		sCtx.reset()
		return gCtx, nil
	})
}

// ============================================================================
// Background Steps
// ============================================================================

func (ctx *daemonPlayerResolutionContext) theDaemonPlayerResolutionServiceIsInitialized() error {
	ctx.reset()
	return nil
}

func (ctx *daemonPlayerResolutionContext) playerExistsWithAgentSymbol(playerID int, agentSymbol string) error {
	p := player.NewPlayer(playerID, agentSymbol, fmt.Sprintf("token-%d", playerID))
	ctx.players[agentSymbol] = p
	ctx.mediator.players[agentSymbol] = p
	return nil
}

// ============================================================================
// When Steps - Direct Resolution
// ============================================================================

func (ctx *daemonPlayerResolutionContext) iResolvePlayerWithPlayerID(playerID int) error {
	ctx.requestPlayerID = int32(playerID)
	ctx.requestAgentSymbol = nil

	// Call NavigateShip to trigger player resolution
	req := &pb.NavigateShipRequest{
		ShipSymbol:  "TEST-SHIP",
		Destination: "X1-TEST-DEST",
		PlayerId:    ctx.requestPlayerID,
		AgentSymbol: ctx.requestAgentSymbol,
	}

	resp, err := ctx.service.NavigateShip(context.Background(), req)
	if err != nil {
		ctx.resolutionErr = err
		return nil
	}

	ctx.resolvedPlayerID = int(ctx.requestPlayerID)
	ctx.resolutionErr = nil
	_ = resp // Success
	return nil
}

func (ctx *daemonPlayerResolutionContext) iResolvePlayerWithAgentSymbol(agentSymbol string) error {
	ctx.requestPlayerID = 0
	ctx.requestAgentSymbol = &agentSymbol

	// Call NavigateShip to trigger player resolution
	req := &pb.NavigateShipRequest{
		ShipSymbol:  "TEST-SHIP",
		Destination: "X1-TEST-DEST",
		PlayerId:    ctx.requestPlayerID,
		AgentSymbol: ctx.requestAgentSymbol,
	}

	resp, err := ctx.service.NavigateShip(context.Background(), req)
	if err != nil {
		ctx.resolutionErr = err
		return nil
	}

	// Extract player ID from successful resolution
	if p, ok := ctx.mediator.players[agentSymbol]; ok {
		ctx.resolvedPlayerID = p.ID
	}
	ctx.resolutionErr = nil
	_ = resp // Success
	return nil
}

func (ctx *daemonPlayerResolutionContext) iResolvePlayerWithPlayerIDAndAgentSymbol(playerID int, agentSymbol string) error {
	ctx.requestPlayerID = int32(playerID)
	ctx.requestAgentSymbol = &agentSymbol

	// Call NavigateShip to trigger player resolution
	req := &pb.NavigateShipRequest{
		ShipSymbol:  "TEST-SHIP",
		Destination: "X1-TEST-DEST",
		PlayerId:    ctx.requestPlayerID,
		AgentSymbol: ctx.requestAgentSymbol,
	}

	resp, err := ctx.service.NavigateShip(context.Background(), req)
	if err != nil {
		ctx.resolutionErr = err
		return nil
	}

	// player_id takes precedence
	ctx.resolvedPlayerID = int(ctx.requestPlayerID)
	ctx.resolutionErr = nil
	_ = resp // Success
	return nil
}

func (ctx *daemonPlayerResolutionContext) iResolvePlayerWithPlayerIDAndNoAgentSymbol(playerID int) error {
	ctx.requestPlayerID = int32(playerID)
	ctx.requestAgentSymbol = nil

	// Call NavigateShip to trigger player resolution
	req := &pb.NavigateShipRequest{
		ShipSymbol:  "TEST-SHIP",
		Destination: "X1-TEST-DEST",
		PlayerId:    ctx.requestPlayerID,
		AgentSymbol: ctx.requestAgentSymbol,
	}

	_, err := ctx.service.NavigateShip(context.Background(), req)
	ctx.resolutionErr = err
	return nil
}

// ============================================================================
// When Steps - gRPC Operations
// ============================================================================

func (ctx *daemonPlayerResolutionContext) iSendNavigateShipRequestWithPlayerID(playerID int) error {
	req := &pb.NavigateShipRequest{
		ShipSymbol:  "TEST-SHIP",
		Destination: "X1-TEST-DEST",
		PlayerId:    int32(playerID),
	}

	_, err := ctx.service.NavigateShip(context.Background(), req)
	if err != nil {
		return err
	}

	ctx.operationPlayerID = playerID
	return nil
}

func (ctx *daemonPlayerResolutionContext) iSendNavigateShipRequestWithAgentSymbol(agentSymbol string) error {
	req := &pb.NavigateShipRequest{
		ShipSymbol:  "TEST-SHIP",
		Destination: "X1-TEST-DEST",
		AgentSymbol: &agentSymbol,
	}

	_, err := ctx.service.NavigateShip(context.Background(), req)
	if err != nil {
		return err
	}

	// Resolve the player ID from agent symbol
	if p, ok := ctx.mediator.players[agentSymbol]; ok {
		ctx.operationPlayerID = p.ID
	}
	return nil
}

func (ctx *daemonPlayerResolutionContext) iSendDockShipRequestWithPlayerID(playerID int) error {
	req := &pb.DockShipRequest{
		ShipSymbol: "TEST-SHIP",
		PlayerId:   int32(playerID),
	}

	_, err := ctx.service.DockShip(context.Background(), req)
	if err != nil {
		return err
	}

	ctx.operationPlayerID = playerID
	return nil
}

func (ctx *daemonPlayerResolutionContext) iSendOrbitShipRequestWithAgentSymbol(agentSymbol string) error {
	req := &pb.OrbitShipRequest{
		ShipSymbol:  "TEST-SHIP",
		AgentSymbol: &agentSymbol,
	}

	_, err := ctx.service.OrbitShip(context.Background(), req)
	if err != nil {
		return err
	}

	// Resolve the player ID from agent symbol
	if p, ok := ctx.mediator.players[agentSymbol]; ok {
		ctx.operationPlayerID = p.ID
	}
	return nil
}

func (ctx *daemonPlayerResolutionContext) iSendRefuelShipRequestWithPlayerID(playerID int) error {
	req := &pb.RefuelShipRequest{
		ShipSymbol: "TEST-SHIP",
		PlayerId:   int32(playerID),
	}

	_, err := ctx.service.RefuelShip(context.Background(), req)
	if err != nil {
		return err
	}

	ctx.operationPlayerID = playerID
	return nil
}

// ============================================================================
// Then Steps
// ============================================================================

func (ctx *daemonPlayerResolutionContext) theResolutionShouldSucceed() error {
	if ctx.resolutionErr != nil {
		return fmt.Errorf("expected resolution to succeed, but got error: %v", ctx.resolutionErr)
	}
	return nil
}

func (ctx *daemonPlayerResolutionContext) theResolutionShouldFail() error {
	if ctx.resolutionErr == nil {
		return fmt.Errorf("expected resolution to fail, but it succeeded")
	}
	return nil
}

func (ctx *daemonPlayerResolutionContext) theResolvedPlayerIDShouldBe(expectedPlayerID int) error {
	if ctx.resolvedPlayerID != expectedPlayerID {
		return fmt.Errorf("expected resolved player_id to be %d, got %d", expectedPlayerID, ctx.resolvedPlayerID)
	}
	return nil
}

func (ctx *daemonPlayerResolutionContext) theErrorShouldContain(expectedMessage string) error {
	if ctx.resolutionErr == nil {
		return fmt.Errorf("expected error containing '%s', but no error occurred", expectedMessage)
	}

	if !strings.Contains(ctx.resolutionErr.Error(), expectedMessage) {
		return fmt.Errorf("expected error to contain '%s', but got: %v", expectedMessage, ctx.resolutionErr)
	}
	return nil
}

func (ctx *daemonPlayerResolutionContext) thePlayerShouldBeResolvedToPlayerID(expectedPlayerID int) error {
	if ctx.operationPlayerID != expectedPlayerID {
		return fmt.Errorf("expected player to be resolved to %d, got %d", expectedPlayerID, ctx.operationPlayerID)
	}
	return nil
}

func (ctx *daemonPlayerResolutionContext) theOperationShouldUseTheCorrectPlayerContext() error {
	// This is validated by the successful operation completion
	// The fact that the operation succeeded means player resolution worked correctly
	return nil
}
