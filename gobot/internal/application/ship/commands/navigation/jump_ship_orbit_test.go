package navigation

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-28n2 — a NEW tour-death class, distinct from the wc5h cooldown-409: a jump
// leg fires while the hull is DOCKED (it refueled at the gate — the route
// planner's post-arrival refuel docks the hull and never re-orbits, see
// route_executor.handlePostArrivalRefueling), so the live jump API hard-rejects
// it with 400 code 4236 "not currently in orbit" and the container dies. Every
// navigate path already orbits before departing (navigate_direct.EnsureInOrbit,
// RouteExecutor.ensureShipInOrbit); the jump path was the one mover that did
// not. These tests pin the fix: the jump handler orbits a docked hull before
// jumping, and rides a residual 4236 (a raced nav_status) by orbiting live and
// retrying rather than crashing — exactly how wc5h rides a cooldown-409.

// orbitCallLog records the relative order of orbit vs jump calls across the
// shipRepo and apiClient stubs, so a test can assert the handler orbits BEFORE
// it jumps (the whole point of the fix — a jump that fires before the orbit is
// the 4236 death).
type orbitCallLog struct {
	calls []string
}

// stubOrbitJumpShipRepo implements only the methods the jump handler exercises
// on the ship repository for these scenarios, including Orbit (which the
// existing stubJumpShipRepo deliberately omits so an unexpected orbit panics).
type stubOrbitJumpShipRepo struct {
	domainNavigation.ShipRepository

	ship       *domainNavigation.Ship
	log        *orbitCallLog
	orbitCalls int
	orbitErr   error
}

func (s *stubOrbitJumpShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*domainNavigation.Ship, error) {
	return s.ship, nil
}

func (s *stubOrbitJumpShipRepo) Save(_ context.Context, _ *domainNavigation.Ship) error {
	return nil
}

func (s *stubOrbitJumpShipRepo) Orbit(_ context.Context, ship *domainNavigation.Ship, _ shared.PlayerID) error {
	s.orbitCalls++
	s.log.calls = append(s.log.calls, "orbit")
	if s.orbitErr != nil {
		return s.orbitErr
	}
	// Mirror the real ShipRepository.Orbit: idempotently transition the domain
	// entity into orbit (the live API is idempotent; already-in-orbit is a no-op).
	if _, err := ship.EnsureInOrbit(); err != nil {
		return err
	}
	return nil
}

// stubOrbitJumpAPIClient drives the jump API. errByCall is consumed one entry
// per JumpShip call (front to back); a nil slot (or running past the end) yields
// success with result. This lets a test make the first jump 4236 and the retry
// succeed.
type stubOrbitJumpAPIClient struct {
	ports.APIClient

	log       *orbitCallLog
	gateData  *ports.JumpGateData
	result    *ports.JumpResult
	errByCall []error
	jumpCalls int
}

func (s *stubOrbitJumpAPIClient) JumpShip(_ context.Context, _ string, waypointSymbol string, _ string) (*ports.JumpResult, error) {
	s.log.calls = append(s.log.calls, "jump")
	call := s.jumpCalls
	s.jumpCalls++
	if call < len(s.errByCall) && s.errByCall[call] != nil {
		return nil, s.errByCall[call]
	}
	return s.result, nil
}

func (s *stubOrbitJumpAPIClient) GetJumpGate(_ context.Context, _, _, _ string) (*ports.JumpGateData, error) {
	return s.gateData, nil
}

// newDockedDrivelessShipAtGate builds the hull shape the incident stranded: a
// driveless hauler DOCKED on a jump gate with a fresh full tank (fuel 2300/2300)
// — the post-refuel state that fires the jump while docked. (Cargo is left empty;
// the death turns on nav status, not the manifest.)
func newDockedDrivelessShipAtGate(t *testing.T, symbol, gateSymbol string) *domainNavigation.Ship {
	t.Helper()
	gate := newJumpGateWaypoint(t, gateSymbol)
	fuel, err := shared.NewFuel(2300, 2300)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	cargo, err := shared.NewCargo(225, 0, nil)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	ship, err := domainNavigation.NewShip(
		symbol,
		shared.MustNewPlayerID(1),
		gate,
		fuel,
		2300,
		225,
		cargo,
		9,
		"FRAME_LIGHT_FREIGHTER",
		"HAULER",
		nil, // driveless — a gate-adjacent driveless jump, like the arb/trade haulers
		domainNavigation.NavStatusDocked,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

// The reproduction: a driveless hull DOCKED at a complete jump gate (post-refuel)
// must be ORBITED before the jump API is called. The handler had no nav-status
// guard, so it issued the jump while docked and the API 4236'd — the tour-death.
// SkipClaim mirrors the trade-route coordinator holding the ship for the circuit.
func TestJumpShip_DockedAtGate_OrbitsBeforeJumping(t *testing.T) {
	log := &orbitCallLog{}
	ship := newDockedDrivelessShipAtGate(t, "TORWIND-2B", "X1-NK36-E14F")

	shipRepo := &stubOrbitJumpShipRepo{ship: ship, log: log}
	playerRepo := &stubJumpPlayerRepo{playerEntity: player.NewPlayer(shared.MustNewPlayerID(1), "AGENT", "test-token")}
	containerRepo := &stubJumpContainerRepo{}
	constructionRepo := &stubJumpConstructionRepo{
		site: manufacturing.NewConstructionSite("X1-NK36-E14F", "JUMP_GATE", nil, true),
	}
	apiClient := &stubOrbitJumpAPIClient{
		log:      log,
		gateData: &ports.JumpGateData{Symbol: "X1-NK36-E14F", Connections: []string{"X1-GQ92-I51"}},
		result: &ports.JumpResult{
			DestinationSystem:   "X1-GQ92",
			DestinationWaypoint: "X1-GQ92-I51",
			CooldownSeconds:     60,
		},
	}
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)}

	handler := NewJumpShipHandler(shipRepo, playerRepo, apiClient, nil, containerRepo, constructionRepo, clock)

	playerIDInt := 1
	cmd := &JumpShipCommand{
		ShipSymbol:        "TORWIND-2B",
		DestinationSystem: "X1-GQ92",
		PlayerID:          &playerIDInt,
		SkipClaim:         true,
	}

	resp, err := handler.Handle(context.Background(), cmd)
	if err != nil {
		t.Fatalf("expected a docked-at-gate jump to orbit then succeed, got error: %v", err)
	}
	jumpResp, ok := resp.(*JumpShipResponse)
	if !ok || !jumpResp.Success {
		t.Fatalf("expected a successful jump response, got %+v (ok=%v)", resp, ok)
	}

	// The orbit MUST have happened, and it MUST precede the jump — a jump issued
	// before the orbit is exactly the 4236 death this fix retires.
	if shipRepo.orbitCalls != 1 {
		t.Fatalf("expected exactly one orbit before the jump, got %d", shipRepo.orbitCalls)
	}
	if got := fmt.Sprintf("%v", log.calls); got != "[orbit jump]" {
		t.Fatalf("expected orbit to precede jump (call order [orbit jump]), got %v", log.calls)
	}

	// The jump itself must still take effect (nav state synced to the destination).
	if got := ship.CurrentLocation().SystemSymbol; got != "X1-GQ92" {
		t.Fatalf("expected ship system synced to X1-GQ92 after the jump, got %s", got)
	}
}

// Belt-and-suspenders: even when the handler reads the hull as IN_ORBIT, a raced
// nav_status (the persisted state lagged a server-side dock) can still draw a
// 4236 from the live jump API. Rather than surface it as a hard iteration error
// (the container-killing path), the handler orbits live and retries — bounded,
// exactly as wc5h rides a cooldown-409. Here the first jump 4236s, the handler
// orbits, and the retry succeeds.
func TestJumpShip_JumpRejectedNotInOrbit4236_OrbitsAndRetries(t *testing.T) {
	log := &orbitCallLog{}
	gate := newJumpGateWaypoint(t, "X1-NK36-E14F")
	ship := newJumpTestShip(t, "TORWIND-2B", gate) // drive-equipped, starts IN_ORBIT

	notInOrbit4236 := fmt.Errorf("failed to jump ship: API error (status 400): " +
		`{"error":{"code":4236,"message":"Ship action failed. Ship is not currently in orbit at X1-NK36-E14F.","data":{"waypointSymbol":"X1-NK36-E14F"}}}`)

	shipRepo := &stubOrbitJumpShipRepo{ship: ship, log: log}
	playerRepo := &stubJumpPlayerRepo{playerEntity: player.NewPlayer(shared.MustNewPlayerID(1), "AGENT", "test-token")}
	containerRepo := &stubJumpContainerRepo{}
	apiClient := &stubOrbitJumpAPIClient{
		log:       log,
		gateData:  &ports.JumpGateData{Symbol: "X1-NK36-E14F", Connections: []string{"X1-GQ92-I51"}},
		errByCall: []error{notInOrbit4236}, // first jump 4236s; the retry succeeds
		result: &ports.JumpResult{
			DestinationSystem:   "X1-GQ92",
			DestinationWaypoint: "X1-GQ92-I51",
			CooldownSeconds:     60,
		},
	}
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)}

	handler := NewJumpShipHandler(shipRepo, playerRepo, apiClient, nil, containerRepo, nil, clock)

	playerIDInt := 1
	cmd := &JumpShipCommand{
		ShipSymbol:        "TORWIND-2B",
		DestinationSystem: "X1-GQ92",
		PlayerID:          &playerIDInt,
		SkipClaim:         true,
	}

	resp, err := handler.Handle(context.Background(), cmd)
	if err != nil {
		t.Fatalf("expected a 4236-then-orbit-then-retry jump to succeed, got error: %v", err)
	}
	jumpResp, ok := resp.(*JumpShipResponse)
	if !ok || !jumpResp.Success {
		t.Fatalf("expected a successful jump response after the retry, got %+v (ok=%v)", resp, ok)
	}

	// Two jump attempts (the 4236 then the retry), with an orbit in between.
	if apiClient.jumpCalls != 2 {
		t.Fatalf("expected exactly two jump attempts (4236 then retry), got %d", apiClient.jumpCalls)
	}
	if shipRepo.orbitCalls != 1 {
		t.Fatalf("expected exactly one orbit between the failed and retried jump, got %d", shipRepo.orbitCalls)
	}
	if got := fmt.Sprintf("%v", log.calls); got != "[jump orbit jump]" {
		t.Fatalf("expected call order [jump orbit jump] (4236, orbit, retry), got %v", log.calls)
	}
}
