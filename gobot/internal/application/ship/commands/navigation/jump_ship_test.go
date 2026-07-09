package navigation

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	domainContainer "github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// jumpSaveSnapshot captures the persisted-relevant fields of a ship at the
// moment Save was called. Because the handler and the test share the same
// *Ship pointer, storing the pointer alone can't distinguish "this field was
// mutated in memory" from "this field was actually persisted" - inspecting
// the pointer after Handle returns sees every mutation regardless of which
// Save call (if any) was supposed to persist it. Snapshotting the fields we
// care about at each Save call closes that gap.
type jumpSaveSnapshot struct {
	systemSymbol    string
	cooldownIsSet   bool
	containerClaims bool
}

// stubJumpShipRepo embeds the domain interface so we only implement the
// methods the handler exercises; any unexpected call will panic with a
// nil-method deref.
type stubJumpShipRepo struct {
	domainNavigation.ShipRepository

	ship       *domainNavigation.Ship
	savedShips []*domainNavigation.Ship
	saves      []jumpSaveSnapshot
}

func (s *stubJumpShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*domainNavigation.Ship, error) {
	return s.ship, nil
}

func (s *stubJumpShipRepo) Save(_ context.Context, ship *domainNavigation.Ship) error {
	s.savedShips = append(s.savedShips, ship)
	s.saves = append(s.saves, jumpSaveSnapshot{
		systemSymbol:    ship.CurrentLocation().SystemSymbol,
		cooldownIsSet:   ship.CooldownExpiration() != nil,
		containerClaims: ship.IsAssigned(),
	})
	return nil
}

// stubJumpPlayerRepo embeds the domain interface so we only implement FindByID.
type stubJumpPlayerRepo struct {
	player.PlayerRepository

	playerEntity *player.Player
}

func (s *stubJumpPlayerRepo) FindByID(_ context.Context, _ shared.PlayerID) (*player.Player, error) {
	return s.playerEntity, nil
}

// stubJumpAPIClient embeds the domain interface so we only implement
// JumpShip and GetJumpGate. It captures the waypoint argument JumpShip was
// called with (the value that crosses the port boundary into the live
// jump API's request body) so tests can assert the handler resolved a
// destination GATE WAYPOINT rather than forwarding the bare destination
// SYSTEM symbol (sp-n0x7 round 2).
type stubJumpAPIClient struct {
	ports.APIClient

	result *ports.JumpResult
	err    error

	gateData *ports.JumpGateData
	gateErr  error

	jumpShipWaypointArg string
}

func (s *stubJumpAPIClient) JumpShip(_ context.Context, _ string, waypointSymbol string, _ string) (*ports.JumpResult, error) {
	s.jumpShipWaypointArg = waypointSymbol
	return s.result, s.err
}

func (s *stubJumpAPIClient) GetJumpGate(_ context.Context, _, _, _ string) (*ports.JumpGateData, error) {
	return s.gateData, s.gateErr
}

// stubJumpContainerRepo records claim-lifecycle calls (Add when the claim is
// taken, Remove when it is released) so tests can assert the temporary
// container record used to satisfy the ship_assignments FK constraint is
// created and cleaned up, mirroring balance_ship_position.go's pattern.
type stubJumpContainerRepo struct {
	added   []*domainContainer.Container
	removed []string
}

func (s *stubJumpContainerRepo) Add(_ context.Context, c *domainContainer.Container, _ string) error {
	s.added = append(s.added, c)
	return nil
}

func (s *stubJumpContainerRepo) Remove(_ context.Context, containerID string, _ int) error {
	s.removed = append(s.removed, containerID)
	return nil
}

// stubJumpConstructionRepo embeds the domain interface so we only implement
// FindByWaypoint. site/err let a test control whether the queried gate
// reports as complete, still under construction, or unreachable (repo
// error) - the latter exercises the fail-open path, where the handler
// cannot verify status and defers the decision to the live jump API rather
// than blocking an otherwise-legal jump on a repository hiccup.
type stubJumpConstructionRepo struct {
	manufacturing.ConstructionSiteRepository

	site *manufacturing.ConstructionSite
	err  error
}

func (s *stubJumpConstructionRepo) FindByWaypoint(_ context.Context, _ string, _ int) (*manufacturing.ConstructionSite, error) {
	return s.site, s.err
}

func newJumpTestShip(t *testing.T, symbol string, location *shared.Waypoint) *domainNavigation.Ship {
	t.Helper()
	jumpDrive := domainNavigation.NewShipModule("MODULE_JUMP_DRIVE_I", 0, 500)
	return newJumpTestShipWithModules(t, symbol, location, []*domainNavigation.ShipModule{jumpDrive})
}

// newDrivelessJumpTestShip builds a ship with no jump drive module, used to
// exercise the gate-adjacent driveless-jump precondition (sp-n0x7): such a
// ship may only jump if it is currently at a COMPLETE jump gate.
func newDrivelessJumpTestShip(t *testing.T, symbol string, location *shared.Waypoint) *domainNavigation.Ship {
	t.Helper()
	return newJumpTestShipWithModules(t, symbol, location, nil)
}

func newJumpTestShipWithModules(t *testing.T, symbol string, location *shared.Waypoint, modules []*domainNavigation.ShipModule) *domainNavigation.Ship {
	t.Helper()
	fuel, err := shared.NewFuel(0, 0)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	cargo, err := shared.NewCargo(0, 0, nil)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	ship, err := domainNavigation.NewShip(
		symbol,
		shared.MustNewPlayerID(1),
		location,
		fuel,
		0,
		0,
		cargo,
		9,
		"FRAME_PROBE",
		"SATELLITE",
		modules,
		domainNavigation.NavStatusInOrbit,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

func newJumpGateWaypoint(t *testing.T, symbol string) *shared.Waypoint {
	t.Helper()
	wp, err := shared.NewWaypoint(symbol, 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	wp.Type = "JUMP_GATE"
	return wp
}

// newNonGateWaypoint builds an ordinary (non jump-gate) waypoint, used to
// prove a driveless ship away from any gate is rejected.
func newNonGateWaypoint(t *testing.T, symbol string) *shared.Waypoint {
	t.Helper()
	wp, err := shared.NewWaypoint(symbol, 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	wp.Type = "PLANET"
	return wp
}

// Dispatching a JumpShipCommand for a gate-parked probe with a jump drive
// must succeed, sync the ship's nav state (destination system + cooldown),
// and release the temporary claim taken to satisfy the ship_assignments FK
// constraint - mirroring how navigate syncs state and releases its claim on
// completion.
func TestJumpShip_SuccessfulJump_SyncsShipStateAndReleasesClaim(t *testing.T) {
	gate := newJumpGateWaypoint(t, "X1-AB12-GATE")
	ship := newJumpTestShip(t, "PROBE-1", gate)

	shipRepo := &stubJumpShipRepo{ship: ship}
	playerRepo := &stubJumpPlayerRepo{playerEntity: player.NewPlayer(shared.MustNewPlayerID(1), "AGENT", "test-token")}
	containerRepo := &stubJumpContainerRepo{}
	apiClient := &stubJumpAPIClient{
		gateData: &ports.JumpGateData{
			Symbol:      "X1-AB12-GATE",
			Connections: []string{"X1-CD34-GATE"},
		},
		result: &ports.JumpResult{
			DestinationSystem:   "X1-CD34",
			DestinationWaypoint: "X1-CD34-GATE",
			CooldownSeconds:     60,
		},
	}
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)}

	handler := NewJumpShipHandler(shipRepo, playerRepo, apiClient, nil, containerRepo, nil, clock)

	playerIDInt := 1
	cmd := &JumpShipCommand{
		ShipSymbol:        "PROBE-1",
		DestinationSystem: "X1-CD34",
		PlayerID:          &playerIDInt,
	}

	resp, err := handler.Handle(context.Background(), cmd)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	jumpResp, ok := resp.(*JumpShipResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if !jumpResp.Success {
		t.Fatalf("expected Success=true")
	}

	// Ship nav state must be synced to the destination system.
	if got := ship.CurrentLocation().SystemSymbol; got != "X1-CD34" {
		t.Fatalf("expected ship system synced to X1-CD34, got %s", got)
	}

	// Cooldown must be set from the jump result.
	cooldown := ship.CooldownExpiration()
	if cooldown == nil {
		t.Fatalf("expected cooldown to be set")
	}
	wantCooldown := clock.CurrentTime.Add(60 * time.Second)
	if !cooldown.Equal(wantCooldown) {
		t.Fatalf("expected cooldown %v, got %v", wantCooldown, *cooldown)
	}

	// The claim taken to satisfy the FK constraint must be released once the
	// handler completes.
	if ship.IsAssigned() {
		t.Fatalf("expected ship claim to be released after jump completes")
	}

	// The lightweight container record must be created (for the FK
	// constraint) then removed once the claim is released.
	if len(containerRepo.added) != 1 {
		t.Fatalf("expected 1 container record added, got %d", len(containerRepo.added))
	}
	if len(containerRepo.removed) != 1 {
		t.Fatalf("expected 1 container record removed, got %d", len(containerRepo.removed))
	}

	// The handler saves the ship at least twice: once when the claim is
	// taken (step 8, still at the gate/no cooldown) and once after the
	// post-jump nav/cooldown sync (step 10). Asserting only "saved at
	// least once" would still pass even if the final post-jump
	// persistence call were deleted, since the claim-time save alone
	// satisfies that.
	if len(shipRepo.savedShips) < 2 {
		t.Fatalf("expected ship to be saved at least twice (claim + post-jump sync), got %d", len(shipRepo.savedShips))
	}

	// At least one Save call must have persisted the ship *while* it
	// already carried the destination system and cooldown. saves[] records
	// a value snapshot taken at each Save invocation (not the shared
	// pointer), so this distinguishes "the sync was in memory when some
	// Save fired" from "the pointer merely looks right now that Handle has
	// returned" - it fails if SetLocation/SetCooldown were never called
	// before any Save, even though it can't isolate which specific Save
	// call did the persisting (the claim-release defer also unconditionally
	// re-saves the same ship, which is itself part of why the claim can
	// never leak stale nav state).
	syncedBeforeSave := false
	for _, snap := range shipRepo.saves {
		if snap.systemSymbol == "X1-CD34" && snap.cooldownIsSet {
			syncedBeforeSave = true
			break
		}
	}
	if !syncedBeforeSave {
		t.Fatalf("expected at least one Save call to persist the ship with destination X1-CD34 and cooldown set, got saves: %+v", shipRepo.saves)
	}
}

// A 4262 API response (destination jump gate still under construction) must
// surface as a clean, readable error - not the raw API/JSON failure - and
// the claim must still be released even though the jump failed.
func TestJumpShip_DestinationGateUnderConstruction4262_SurfacesCleanError(t *testing.T) {
	gate := newJumpGateWaypoint(t, "X1-AB12-GATE")
	ship := newJumpTestShip(t, "PROBE-1", gate)

	shipRepo := &stubJumpShipRepo{ship: ship}
	playerRepo := &stubJumpPlayerRepo{playerEntity: player.NewPlayer(shared.MustNewPlayerID(1), "AGENT", "test-token")}
	containerRepo := &stubJumpContainerRepo{}
	apiClient := &stubJumpAPIClient{
		gateData: &ports.JumpGateData{
			Symbol:      "X1-AB12-GATE",
			Connections: []string{"X1-CD34-GATE"},
		},
		err: fmt.Errorf("failed to jump ship: max retries exceeded: " +
			`API error (status 400): {"error":{"code":4262,"message":"Jump failed. Destination jump gate X1-CD34-GATE is under construction."}}`),
	}
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)}

	handler := NewJumpShipHandler(shipRepo, playerRepo, apiClient, nil, containerRepo, nil, clock)

	playerIDInt := 1
	cmd := &JumpShipCommand{
		ShipSymbol:        "PROBE-1",
		DestinationSystem: "X1-CD34",
		PlayerID:          &playerIDInt,
	}

	_, err := handler.Handle(context.Background(), cmd)
	if err == nil {
		t.Fatalf("expected an error for 4262 destination gate under construction")
	}
	if strings.Contains(err.Error(), "4262") {
		t.Fatalf("expected clean user-facing error without the raw API code, got: %v", err)
	}
	if !strings.Contains(err.Error(), "under construction") {
		t.Fatalf("expected clean error to mention the gate is under construction, got: %v", err)
	}

	// Even on failure, the claim must be released (completion = handler
	// returning, success or failure).
	if ship.IsAssigned() {
		t.Fatalf("expected ship claim to be released even after a failed jump")
	}
	if len(containerRepo.removed) != 1 {
		t.Fatalf("expected container record removed even on failure, got %d", len(containerRepo.removed))
	}
}

// SpaceTraders rule (sp-n0x7): gate-adjacent driveless jumps are legal - a
// ship with NO jump drive module can still jump if it is currently at a
// COMPLETE jump gate. The precondition must not hard-require a jump drive.
func TestJumpShip_DrivelessShipAtCompleteGate_PassesPrecondition(t *testing.T) {
	gate := newJumpGateWaypoint(t, "X1-AB12-GATE")
	ship := newDrivelessJumpTestShip(t, "PROBE-2", gate)

	shipRepo := &stubJumpShipRepo{ship: ship}
	playerRepo := &stubJumpPlayerRepo{playerEntity: player.NewPlayer(shared.MustNewPlayerID(1), "AGENT", "test-token")}
	containerRepo := &stubJumpContainerRepo{}
	constructionRepo := &stubJumpConstructionRepo{
		site: manufacturing.NewConstructionSite("X1-AB12-GATE", "JUMP_GATE", nil, true),
	}
	apiClient := &stubJumpAPIClient{
		gateData: &ports.JumpGateData{
			Symbol:      "X1-AB12-GATE",
			Connections: []string{"X1-CD34-GATE"},
		},
		result: &ports.JumpResult{
			DestinationSystem:   "X1-CD34",
			DestinationWaypoint: "X1-CD34-GATE",
			CooldownSeconds:     60,
		},
	}
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)}

	handler := NewJumpShipHandler(shipRepo, playerRepo, apiClient, nil, containerRepo, constructionRepo, clock)

	playerIDInt := 1
	cmd := &JumpShipCommand{
		ShipSymbol:        "PROBE-2",
		DestinationSystem: "X1-CD34",
		PlayerID:          &playerIDInt,
	}

	resp, err := handler.Handle(context.Background(), cmd)
	if err != nil {
		t.Fatalf("expected a driveless ship at a complete jump gate to pass the precondition, got error: %v", err)
	}
	jumpResp, ok := resp.(*JumpShipResponse)
	if !ok || !jumpResp.Success {
		t.Fatalf("expected a successful jump response, got %+v (ok=%v)", resp, ok)
	}
}

// A driveless ship that is NOT at a jump gate at all has no legal way to
// jump (no drive, and no gate-adjacency to fall back on) and must be
// rejected with a clear error before any API call is attempted.
func TestJumpShip_DrivelessShipNotAtGate_RejectedWithClearError(t *testing.T) {
	notAGate := newNonGateWaypoint(t, "X1-AB12-ROCK")
	ship := newDrivelessJumpTestShip(t, "PROBE-3", notAGate)

	shipRepo := &stubJumpShipRepo{ship: ship}
	playerRepo := &stubJumpPlayerRepo{playerEntity: player.NewPlayer(shared.MustNewPlayerID(1), "AGENT", "test-token")}
	containerRepo := &stubJumpContainerRepo{}
	constructionRepo := &stubJumpConstructionRepo{}
	apiClient := &stubJumpAPIClient{}
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)}

	handler := NewJumpShipHandler(shipRepo, playerRepo, apiClient, nil, containerRepo, constructionRepo, clock)

	playerIDInt := 1
	cmd := &JumpShipCommand{
		ShipSymbol:        "PROBE-3",
		DestinationSystem: "X1-CD34",
		PlayerID:          &playerIDInt,
	}

	_, err := handler.Handle(context.Background(), cmd)
	if err == nil {
		t.Fatalf("expected an error for a driveless ship that is not at a jump gate")
	}
	if !strings.Contains(err.Error(), "jump drive") || !strings.Contains(err.Error(), "jump gate") {
		t.Fatalf("expected a clear error mentioning both jump drive and jump gate, got: %v", err)
	}

	// The precondition must reject before any claim is taken or API call is
	// attempted - no side effects for an ineligible jump.
	if len(containerRepo.added) != 0 {
		t.Fatalf("expected no container claim to be taken for a rejected precondition, got %d", len(containerRepo.added))
	}
}

// A gate still UNDER_CONSTRUCTION is not a valid source for a driveless
// jump, even though it is a JUMP_GATE-typed waypoint.
func TestJumpShip_DrivelessShipAtUnderConstructionGate_RejectedWithClearError(t *testing.T) {
	gate := newJumpGateWaypoint(t, "X1-AB12-GATE")
	ship := newDrivelessJumpTestShip(t, "PROBE-4", gate)

	shipRepo := &stubJumpShipRepo{ship: ship}
	playerRepo := &stubJumpPlayerRepo{playerEntity: player.NewPlayer(shared.MustNewPlayerID(1), "AGENT", "test-token")}
	containerRepo := &stubJumpContainerRepo{}
	constructionRepo := &stubJumpConstructionRepo{
		site: manufacturing.NewConstructionSite("X1-AB12-GATE", "JUMP_GATE", nil, false),
	}
	apiClient := &stubJumpAPIClient{}
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)}

	handler := NewJumpShipHandler(shipRepo, playerRepo, apiClient, nil, containerRepo, constructionRepo, clock)

	playerIDInt := 1
	cmd := &JumpShipCommand{
		ShipSymbol:        "PROBE-4",
		DestinationSystem: "X1-CD34",
		PlayerID:          &playerIDInt,
	}

	_, err := handler.Handle(context.Background(), cmd)
	if err == nil {
		t.Fatalf("expected an error for a driveless ship at an under-construction gate")
	}
	if !strings.Contains(err.Error(), "under construction") {
		t.Fatalf("expected error to mention the gate is under construction, got: %v", err)
	}
	if len(containerRepo.added) != 0 {
		t.Fatalf("expected no container claim to be taken for a rejected precondition, got %d", len(containerRepo.added))
	}
}

// If the construction-status lookup itself fails (e.g. API hiccup), the
// handler must fail OPEN and let the live jump API make the final call,
// mirroring the existing 4262 destination-gate pattern - a repository
// hiccup must never block an otherwise-legal driveless gate jump.
func TestJumpShip_DrivelessShipAtGate_ConstructionLookupFails_FailsOpenAndProceeds(t *testing.T) {
	gate := newJumpGateWaypoint(t, "X1-AB12-GATE")
	ship := newDrivelessJumpTestShip(t, "PROBE-5", gate)

	shipRepo := &stubJumpShipRepo{ship: ship}
	playerRepo := &stubJumpPlayerRepo{playerEntity: player.NewPlayer(shared.MustNewPlayerID(1), "AGENT", "test-token")}
	containerRepo := &stubJumpContainerRepo{}
	constructionRepo := &stubJumpConstructionRepo{err: fmt.Errorf("construction lookup unavailable")}
	apiClient := &stubJumpAPIClient{
		gateData: &ports.JumpGateData{
			Symbol:      "X1-AB12-GATE",
			Connections: []string{"X1-CD34-GATE"},
		},
		result: &ports.JumpResult{
			DestinationSystem:   "X1-CD34",
			DestinationWaypoint: "X1-CD34-GATE",
			CooldownSeconds:     60,
		},
	}
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)}

	handler := NewJumpShipHandler(shipRepo, playerRepo, apiClient, nil, containerRepo, constructionRepo, clock)

	playerIDInt := 1
	cmd := &JumpShipCommand{
		ShipSymbol:        "PROBE-5",
		DestinationSystem: "X1-CD34",
		PlayerID:          &playerIDInt,
	}

	resp, err := handler.Handle(context.Background(), cmd)
	if err != nil {
		t.Fatalf("expected fail-open (proceed to live API) when construction status can't be verified, got error: %v", err)
	}
	jumpResp, ok := resp.(*JumpShipResponse)
	if !ok || !jumpResp.Success {
		t.Fatalf("expected a successful jump response, got %+v (ok=%v)", resp, ok)
	}
}

// sp-n0x7 round 2: the live jump API requires the destination JUMP GATE
// WAYPOINT in the request body, not the bare destination SYSTEM symbol -
// posting the system symbol 422s with "waypointSymbol Required, received
// undefined". The handler must resolve cmd.DestinationSystem to a gate
// waypoint via the origin gate's connections list (which carries full
// waypoint symbols, e.g. "X1-GQ92-I51") and pass THAT - not
// cmd.DestinationSystem - across the APIClient port boundary.
func TestJumpShip_ResolvesDestinationSystemToGateWaypoint_BeforeCallingJumpAPI(t *testing.T) {
	gate := newJumpGateWaypoint(t, "X1-PA3-B10D")
	ship := newJumpTestShip(t, "PROBE-1", gate)

	shipRepo := &stubJumpShipRepo{ship: ship}
	playerRepo := &stubJumpPlayerRepo{playerEntity: player.NewPlayer(shared.MustNewPlayerID(1), "AGENT", "test-token")}
	containerRepo := &stubJumpContainerRepo{}
	apiClient := &stubJumpAPIClient{
		gateData: &ports.JumpGateData{
			Symbol:      "X1-PA3-B10D",
			Connections: []string{"X1-ZC66-A40D", "X1-GQ92-I51", "X1-UQ16-D16D"},
		},
		result: &ports.JumpResult{
			DestinationSystem:   "X1-GQ92",
			DestinationWaypoint: "X1-GQ92-I51",
			CooldownSeconds:     60,
		},
	}
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)}

	handler := NewJumpShipHandler(shipRepo, playerRepo, apiClient, nil, containerRepo, nil, clock)

	playerIDInt := 1
	cmd := &JumpShipCommand{
		ShipSymbol:        "PROBE-1",
		DestinationSystem: "X1-GQ92",
		PlayerID:          &playerIDInt,
	}

	_, err := handler.Handle(context.Background(), cmd)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if apiClient.jumpShipWaypointArg != "X1-GQ92-I51" {
		t.Fatalf("expected JumpShip to be called with the resolved gate waypoint X1-GQ92-I51, got %q", apiClient.jumpShipWaypointArg)
	}
}

// sp-wlev (multi-system trade-route): a coordinator that already holds an
// outer claim on the ship (e.g. a trade-route container mid-circuit) needs
// to dispatch a jump leg WITHOUT jump_ship's own internal
// claim/AssignToContainer - a second claim on an already-assigned ship
// would either error ("already assigned to container X") or, worse,
// silently steal/overwrite the outer coordinator's assignment. SkipClaim
// lets the caller assert "I already hold this ship" and have jump_ship
// trust that instead of taking its own lightweight FK-satisfying claim.
// With SkipClaim, jump_ship must not create or remove any container record,
// and the ship's pre-existing assignment must survive Handle untouched.
func TestJumpShip_SkipClaim_DoesNotTouchContainerOrAssignment(t *testing.T) {
	gate := newJumpGateWaypoint(t, "X1-AB12-GATE")
	ship := newJumpTestShip(t, "PROBE-1", gate)

	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)}

	// The ship is already claimed by an outer coordinator (mirrors a
	// trade-route container holding the ship for the whole circuit) before
	// the jump is ever dispatched.
	const outerContainerID = "trade-route-PROBE-1"
	if err := ship.AssignToContainer(outerContainerID, clock); err != nil {
		t.Fatalf("test setup: failed to pre-assign ship to outer container: %v", err)
	}

	shipRepo := &stubJumpShipRepo{ship: ship}
	playerRepo := &stubJumpPlayerRepo{playerEntity: player.NewPlayer(shared.MustNewPlayerID(1), "AGENT", "test-token")}
	containerRepo := &stubJumpContainerRepo{}
	apiClient := &stubJumpAPIClient{
		gateData: &ports.JumpGateData{
			Symbol:      "X1-AB12-GATE",
			Connections: []string{"X1-CD34-GATE"},
		},
		result: &ports.JumpResult{
			DestinationSystem:   "X1-CD34",
			DestinationWaypoint: "X1-CD34-GATE",
			CooldownSeconds:     60,
		},
	}

	handler := NewJumpShipHandler(shipRepo, playerRepo, apiClient, nil, containerRepo, nil, clock)

	playerIDInt := 1
	cmd := &JumpShipCommand{
		ShipSymbol:        "PROBE-1",
		DestinationSystem: "X1-CD34",
		PlayerID:          &playerIDInt,
		SkipClaim:         true,
	}

	resp, err := handler.Handle(context.Background(), cmd)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	jumpResp, ok := resp.(*JumpShipResponse)
	if !ok || !jumpResp.Success {
		t.Fatalf("expected a successful jump response, got %+v (ok=%v)", resp, ok)
	}

	// No lightweight jump container should ever be created or removed - the
	// caller already holds the ship, so jump_ship must not touch container
	// records at all under SkipClaim.
	if len(containerRepo.added) != 0 {
		t.Fatalf("expected no container record added under SkipClaim, got %d", len(containerRepo.added))
	}
	if len(containerRepo.removed) != 0 {
		t.Fatalf("expected no container record removed under SkipClaim, got %d", len(containerRepo.removed))
	}

	// The ship's pre-existing outer assignment must survive completely
	// untouched - not released, not overwritten.
	if !ship.IsAssigned() {
		t.Fatalf("expected ship to remain assigned after SkipClaim jump, but it was released")
	}
	if got := ship.ContainerID(); got != outerContainerID {
		t.Fatalf("expected ship to remain assigned to outer container %q, got %q", outerContainerID, got)
	}

	// The jump itself must still take effect (nav state synced) even though
	// no claim was taken.
	if got := ship.CurrentLocation().SystemSymbol; got != "X1-CD34" {
		t.Fatalf("expected ship system synced to X1-CD34, got %s", got)
	}
	if ship.CooldownExpiration() == nil {
		t.Fatalf("expected cooldown to be set")
	}
}

// If the origin gate has no connection into the requested destination
// system, the handler must reject with a clear, actionable error instead
// of forwarding an empty/wrong value to the live jump API.
func TestJumpShip_NoGateConnectionToDestinationSystem_RejectedWithClearError(t *testing.T) {
	gate := newJumpGateWaypoint(t, "X1-PA3-B10D")
	ship := newJumpTestShip(t, "PROBE-1", gate)

	shipRepo := &stubJumpShipRepo{ship: ship}
	playerRepo := &stubJumpPlayerRepo{playerEntity: player.NewPlayer(shared.MustNewPlayerID(1), "AGENT", "test-token")}
	containerRepo := &stubJumpContainerRepo{}
	apiClient := &stubJumpAPIClient{
		gateData: &ports.JumpGateData{
			Symbol:      "X1-PA3-B10D",
			Connections: []string{"X1-ZC66-A40D"},
		},
	}
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)}

	handler := NewJumpShipHandler(shipRepo, playerRepo, apiClient, nil, containerRepo, nil, clock)

	playerIDInt := 1
	cmd := &JumpShipCommand{
		ShipSymbol:        "PROBE-1",
		DestinationSystem: "X1-UNREACHABLE",
		PlayerID:          &playerIDInt,
	}

	_, err := handler.Handle(context.Background(), cmd)
	if err == nil {
		t.Fatalf("expected an error when the origin gate has no connection to the destination system")
	}
	if !strings.Contains(err.Error(), "X1-UNREACHABLE") {
		t.Fatalf("expected error to mention the unreachable destination system, got: %v", err)
	}

	// Must fail before ever forwarding a bad value to the live jump API.
	if apiClient.jumpShipWaypointArg != "" {
		t.Fatalf("expected JumpShip not to be called when no gate connection exists, got waypoint arg %q", apiClient.jumpShipWaypointArg)
	}

	// The claim taken to satisfy the FK constraint must still be released
	// even though resolution failed - mirrors the existing 4262
	// under-construction failure test.
	if ship.IsAssigned() {
		t.Fatalf("expected ship claim to be released even after a failed resolution")
	}
}
