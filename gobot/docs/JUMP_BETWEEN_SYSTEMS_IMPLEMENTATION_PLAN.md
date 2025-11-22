# Jump Between Systems Implementation Plan

## Overview

This document outlines the implementation plan for adding cross-system navigation via jump gates to the SpaceTraders Go bot. The feature enables ships to travel between star systems using jump gates, with automatic navigation to the nearest jump gate if the ship is not already at one.

## Feature Requirements

### User Story
As a player, I want to command my ships to jump between star systems so that I can explore the universe and access resources in different systems.

### Acceptance Criteria
- ✅ CLI command to jump a ship to a different system
- ✅ Validate ship has jump drive module before attempting jump
- ✅ Automatically navigate ship to nearest jump gate if not already at one
- ✅ Execute jump through SpaceTraders API
- ✅ Track and display jump cooldown
- ✅ Handle error cases (no jump drive, no gates, invalid system)
- ✅ Follow hexagonal architecture pattern
- ✅ Comprehensive BDD test coverage

### Scope
- **Manual CLI usage only** - User explicitly commands ship to jump
- **Single jump operation** - Not multi-hop cross-system routing
- **Jump gates only** - Not implementing warp drive functionality

## Research Findings

### SpaceTraders API v2.1 Changes
- **Jump drives removed**: In v2.1, jump drive modules (MODULE_JUMP_DRIVE_I/II/III) were removed
- **Jump gates introduced**: Cross-system travel now requires jump gates at waypoints
- **Cooldown system**: Jump operations impose a cooldown period on the ship

### API Endpoints

#### 1. Get Ship with Modules
```
GET /my/ships/{shipSymbol}
```
**Response includes:**
```json
{
  "data": {
    "symbol": "SHIP-1",
    "modules": [
      {
        "symbol": "MODULE_JUMP_DRIVE_I",
        "capacity": 0,
        "range": 500
      }
    ],
    "nav": { ... },
    "fuel": { ... }
  }
}
```

#### 2. Get Jump Gate Information
```
GET /systems/{systemSymbol}/waypoints/{waypointSymbol}/jump-gate
```
**Response includes:**
```json
{
  "data": {
    "symbol": "X1-AB12-GATE",
    "connections": ["X1-CD34", "X1-EF56"]
  }
}
```

#### 3. Execute Jump
```
POST /my/ships/{shipSymbol}/jump
```
**Request body:**
```json
{
  "systemSymbol": "X1-TARGET"
}
```
**Response includes:**
```json
{
  "data": {
    "nav": { ... },
    "cooldown": {
      "remainingSeconds": 60,
      "totalSeconds": 60
    },
    "transaction": {
      "waypointSymbol": "X1-AB12-GATE",
      "shipSymbol": "SHIP-1",
      "totalPrice": 0
    }
  }
}
```

### Ship Module Structure
- Ships have a `modules` array containing installed module objects
- Jump drive modules have `symbol` matching pattern `MODULE_JUMP_DRIVE_*`
- Modules may have `capacity` and `range` properties
- Some ships may not have jump capability

### Jump Gate Mechanics
- Jump gates are waypoints with type `JUMP_GATE`
- Ships must be at a jump gate waypoint to execute a jump
- Jump gates have connections to other systems
- No antimatter cost in current API version (may change)

## Architecture Design

### Layer 1: Domain Layer

#### Ship Entity Enhancement (`internal/domain/navigation/ship.go`)

**New field:**
```go
type Ship struct {
    // ... existing fields ...
    Modules []ShipModule  // New: Installed ship modules
}
```

**New methods:**
```go
// HasJumpDrive checks if ship has any jump drive module installed
func (s *Ship) HasJumpDrive() bool {
    for _, module := range s.Modules {
        if strings.HasPrefix(module.Symbol(), "MODULE_JUMP_DRIVE") {
            return true
        }
    }
    return false
}

// GetJumpDriveRange returns the range of the ship's jump drive, or 0 if none
func (s *Ship) GetJumpDriveRange() int {
    for _, module := range s.Modules {
        if strings.HasPrefix(module.Symbol(), "MODULE_JUMP_DRIVE") {
            return module.Range()
        }
    }
    return 0
}
```

#### New Value Object: ShipModule (`internal/domain/navigation/ship_module.go`)

```go
package navigation

// ShipModule represents an installed module on a ship
type ShipModule struct {
    symbol   string
    capacity int
    range_   int  // use range_ to avoid Go keyword
}

func NewShipModule(symbol string, capacity, range_ int) *ShipModule {
    return &ShipModule{
        symbol:   symbol,
        capacity: capacity,
        range_:   range_,
    }
}

func (m *ShipModule) Symbol() string   { return m.symbol }
func (m *ShipModule) Capacity() int    { return m.capacity }
func (m *ShipModule) Range() int       { return m.range_ }

func (m *ShipModule) IsJumpDrive() bool {
    return strings.HasPrefix(m.symbol, "MODULE_JUMP_DRIVE")
}
```

#### Waypoint Enhancement (`internal/domain/shared/waypoint.go`)

**New method:**
```go
// IsJumpGate returns true if this waypoint is a jump gate
func (w *Waypoint) IsJumpGate() bool {
    return w.Type == "JUMP_GATE"
}
```

### Layer 2: Application Layer

#### Query: Find Nearest Jump Gate (`internal/application/ship/queries/find_jump_gate.go`)

**Purpose:** Find the nearest jump gate in a ship's current system

```go
package queries

type FindNearestJumpGateQuery struct {
    ShipSymbol string
    PlayerID   uint
}

type FindNearestJumpGateResponse struct {
    JumpGate      *shared.Waypoint
    Distance      float64
    SystemSymbol  string
}

type FindNearestJumpGateHandler struct {
    shipRepo      ports.ShipRepository
    apiClient     ports.APIClient
    graphProvider ports.GraphProvider
}

func (h *FindNearestJumpGateHandler) Handle(ctx context.Context, query FindNearestJumpGateQuery) (*FindNearestJumpGateResponse, error) {
    // 1. Get ship to determine current location and system
    // 2. Get all waypoints in the system
    // 3. Filter for jump gates (Type == "JUMP_GATE")
    // 4. Calculate distances from ship's current location
    // 5. Return nearest jump gate with distance
}
```

#### Command: Jump Ship (`internal/application/ship/commands/jump_ship.go`)

**Purpose:** Orchestrate the complete jump operation with auto-navigation

```go
package commands

type JumpShipCommand struct {
    ShipSymbol        string
    DestinationSystem string
    PlayerID          uint
}

type JumpShipResponse struct {
    Success            bool
    NavigatedToGate    bool
    JumpGateSymbol     string
    DestinationSystem  string
    CooldownSeconds    int
    Message            string
}

type JumpShipCommandHandler struct {
    shipRepo  ports.ShipRepository
    apiClient ports.APIClient
    mediator  *mediator.Mediator
}

func (h *JumpShipCommandHandler) Handle(ctx context.Context, cmd JumpShipCommand) (*JumpShipResponse, error) {
    // 1. Fetch ship from repository
    ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)

    // 2. Validate ship has jump drive module
    if !ship.HasJumpDrive() {
        return nil, fmt.Errorf("ship %s does not have a jump drive module", cmd.ShipSymbol)
    }

    // 3. Check if ship is at a jump gate
    currentSystem := ship.CurrentLocation().SystemSymbol
    isAtJumpGate := ship.CurrentLocation().IsJumpGate()

    navigatedToGate := false
    jumpGateSymbol := ship.CurrentLocation().Symbol

    // 4. If not at jump gate, navigate to nearest one
    if !isAtJumpGate {
        // Find nearest jump gate
        findQuery := &queries.FindNearestJumpGateQuery{
            ShipSymbol: cmd.ShipSymbol,
            PlayerID:   cmd.PlayerID,
        }
        findResult, err := h.mediator.Send(ctx, findQuery)
        if err != nil {
            return nil, fmt.Errorf("failed to find jump gate: %w", err)
        }

        jumpGateSymbol = findResult.JumpGate.Symbol

        // Navigate to jump gate using existing NavigateRouteCommand
        navCmd := &NavigateRouteCommand{
            ShipSymbol:  cmd.ShipSymbol,
            Destination: jumpGateSymbol,
            PlayerID:    cmd.PlayerID,
        }
        _, err = h.mediator.Send(ctx, navCmd)
        if err != nil {
            return nil, fmt.Errorf("failed to navigate to jump gate: %w", err)
        }

        navigatedToGate = true

        // Reload ship after navigation
        ship, err = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
    }

    // 5. Verify ship is now at jump gate
    if !ship.CurrentLocation().IsJumpGate() {
        return nil, fmt.Errorf("ship is not at a jump gate")
    }

    // 6. Execute jump via API
    token := // get from player repository
    jumpResult, err := h.apiClient.JumpShip(ctx, cmd.ShipSymbol, cmd.DestinationSystem, token)
    if err != nil {
        return nil, fmt.Errorf("failed to execute jump: %w", err)
    }

    // 7. Update ship in repository (new location, cooldown, etc.)
    // ... update logic ...

    // 8. Return success response
    return &JumpShipResponse{
        Success:            true,
        NavigatedToGate:    navigatedToGate,
        JumpGateSymbol:     jumpGateSymbol,
        DestinationSystem:  cmd.DestinationSystem,
        CooldownSeconds:    jumpResult.Cooldown.RemainingSeconds,
        Message:            fmt.Sprintf("Ship %s jumped from %s to %s", cmd.ShipSymbol, currentSystem, cmd.DestinationSystem),
    }, nil
}
```

### Layer 3: Adapter Layer

#### API Client Extension (`internal/adapters/api/client.go`)

**New structs:**
```go
type JumpShipRequest struct {
    SystemSymbol string `json:"systemSymbol"`
}

type JumpShipResponse struct {
    Data struct {
        Nav struct {
            SystemSymbol   string `json:"systemSymbol"`
            WaypointSymbol string `json:"waypointSymbol"`
            Route          struct {
                Departure struct {
                    SystemSymbol   string `json:"systemSymbol"`
                    WaypointSymbol string `json:"waypointSymbol"`
                } `json:"departure"`
                Destination struct {
                    SystemSymbol   string `json:"systemSymbol"`
                    WaypointSymbol string `json:"waypointSymbol"`
                } `json:"destination"`
            } `json:"route"`
            Status     string `json:"status"`
            FlightMode string `json:"flightMode"`
        } `json:"nav"`
        Cooldown struct {
            ShipSymbol       string `json:"shipSymbol"`
            TotalSeconds     int    `json:"totalSeconds"`
            RemainingSeconds int    `json:"remainingSeconds"`
            Expiration       string `json:"expiration"`
        } `json:"cooldown"`
        Transaction struct {
            WaypointSymbol string `json:"waypointSymbol"`
            ShipSymbol     string `json:"shipSymbol"`
            TotalPrice     int    `json:"totalPrice"`
        } `json:"transaction"`
    } `json:"data"`
}

type JumpGateResponse struct {
    Data struct {
        Symbol      string   `json:"symbol"`
        Connections []string `json:"connections"`
    } `json:"data"`
}

type ShipModule struct {
    Symbol   string `json:"symbol"`
    Capacity int    `json:"capacity"`
    Range    int    `json:"range"`
}
```

**New methods:**
```go
// JumpShip executes a jump through a jump gate to a different system
func (c *SpaceTradersClient) JumpShip(ctx context.Context, shipSymbol, systemSymbol, token string) (*JumpShipResponse, error) {
    url := fmt.Sprintf("%s/my/ships/%s/jump", c.baseURL, shipSymbol)

    requestBody := JumpShipRequest{
        SystemSymbol: systemSymbol,
    }

    bodyBytes, err := json.Marshal(requestBody)
    if err != nil {
        return nil, fmt.Errorf("failed to marshal request: %w", err)
    }

    var response JumpShipResponse
    err = c.doRequest(ctx, "POST", url, token, bodyBytes, &response)
    if err != nil {
        return nil, err
    }

    return &response, nil
}

// GetJumpGate retrieves information about a jump gate waypoint
func (c *SpaceTradersClient) GetJumpGate(ctx context.Context, systemSymbol, waypointSymbol, token string) (*JumpGateResponse, error) {
    url := fmt.Sprintf("%s/systems/%s/waypoints/%s/jump-gate", c.baseURL, systemSymbol, waypointSymbol)

    var response JumpGateResponse
    err := c.doRequest(ctx, "GET", url, token, nil, &response)
    if err != nil {
        return nil, err
    }

    return &response, nil
}
```

**Update GetShip method:**
Modify the existing `GetShip` method to parse and include the `modules` array in the response, converting them to domain `ShipModule` objects.

#### Protobuf Definitions (`pkg/proto/daemon/daemon.proto`)

**Add to service definition:**
```protobuf
service DaemonService {
    // ... existing RPCs ...

    rpc JumpShip(JumpShipRequest) returns (JumpShipResponse);
}

message JumpShipRequest {
    string ship_symbol = 1;
    string destination_system = 2;
    uint32 player_id = 3;
}

message JumpShipResponse {
    bool success = 1;
    bool navigated_to_gate = 2;
    string jump_gate_symbol = 3;
    string destination_system = 4;
    int32 cooldown_seconds = 5;
    string message = 6;
    string error = 7;
}
```

#### gRPC Handler (`internal/adapters/grpc/daemon_service_impl.go`)

```go
func (s *DaemonServer) JumpShip(ctx context.Context, req *pb.JumpShipRequest) (*pb.JumpShipResponse, error) {
    cmd := &commands.JumpShipCommand{
        ShipSymbol:        req.ShipSymbol,
        DestinationSystem: req.DestinationSystem,
        PlayerID:          uint(req.PlayerId),
    }

    result, err := s.mediator.Send(ctx, cmd)
    if err != nil {
        return &pb.JumpShipResponse{
            Success: false,
            Error:   err.Error(),
        }, nil
    }

    response := result.(*commands.JumpShipResponse)
    return &pb.JumpShipResponse{
        Success:           response.Success,
        NavigatedToGate:   response.NavigatedToGate,
        JumpGateSymbol:    response.JumpGateSymbol,
        DestinationSystem: response.DestinationSystem,
        CooldownSeconds:   int32(response.CooldownSeconds),
        Message:           response.Message,
    }, nil
}
```

#### CLI Command (`internal/adapters/cli/jump.go`)

```go
package cli

var jumpCmd = &cobra.Command{
    Use:   "jump",
    Short: "Jump a ship to a different star system via jump gate",
    Long: `Jump a ship to a different star system using a jump gate.

If the ship is not currently at a jump gate, it will automatically
navigate to the nearest jump gate in the current system before jumping.

The ship must have a jump drive module installed to use this command.`,
    Example: `  # Jump ship PROBE-1 to system X1-ALPHA
  spacetraders ship jump --ship PROBE-1 --system X1-ALPHA

  # Jump with explicit player
  spacetraders ship jump --ship PROBE-1 --system X1-ALPHA --player-id 1`,
    RunE: func(cmd *cobra.Command, args []string) error {
        shipSymbol, _ := cmd.Flags().GetString("ship")
        destinationSystem, _ := cmd.Flags().GetString("system")

        // Resolve player
        playerID, err := resolvePlayer(cmd)
        if err != nil {
            return err
        }

        // Connect to daemon
        client, conn, err := getDaemonClient()
        if err != nil {
            return fmt.Errorf("failed to connect to daemon: %w", err)
        }
        defer conn.Close()

        // Execute jump
        fmt.Printf("Initiating jump for ship %s to system %s...\n", shipSymbol, destinationSystem)

        resp, err := client.JumpShip(context.Background(), &pb.JumpShipRequest{
            ShipSymbol:        shipSymbol,
            DestinationSystem: destinationSystem,
            PlayerId:          uint32(playerID),
        })

        if err != nil {
            return fmt.Errorf("jump failed: %w", err)
        }

        if !resp.Success {
            return fmt.Errorf("jump failed: %s", resp.Error)
        }

        // Display results
        if resp.NavigatedToGate {
            fmt.Printf("✓ Navigated to jump gate: %s\n", resp.JumpGateSymbol)
        }

        fmt.Printf("✓ %s\n", resp.Message)

        if resp.CooldownSeconds > 0 {
            fmt.Printf("⏱  Jump cooldown: %d seconds\n", resp.CooldownSeconds)
        }

        return nil
    },
}

func init() {
    jumpCmd.Flags().String("ship", "", "Ship symbol to jump")
    jumpCmd.Flags().String("system", "", "Destination system symbol (e.g., X1-ALPHA)")
    jumpCmd.MarkFlagRequired("ship")
    jumpCmd.MarkFlagRequired("system")

    shipCmd.AddCommand(jumpCmd)
}
```

## Implementation Workflow

### Auto-Navigation Flow

When a ship is commanded to jump but is not at a jump gate:

```
1. User executes: ./bin/spacetraders ship jump --ship PROBE-1 --system X1-ALPHA

2. JumpShipCommand validates: PROBE-1 has jump drive module ✓

3. Check location: PROBE-1 at "X1-BETA-MARKETPLACE" (not a jump gate)

4. FindNearestJumpGateQuery:
   - Query all waypoints in system X1-BETA
   - Filter for type == "JUMP_GATE"
   - Find gates: ["X1-BETA-GATE1", "X1-BETA-GATE2"]
   - Calculate distances: [120 units, 340 units]
   - Return: X1-BETA-GATE1 (nearest)

5. NavigateRouteCommand (reuse existing):
   - From: X1-BETA-MARKETPLACE
   - To: X1-BETA-GATE1
   - Handles: orbit, fuel checks, refueling, multi-hop if needed
   - Executes: ship travels to gate
   - Returns: success

6. Reload ship: fresh state from API

7. Verify: ship.CurrentLocation() == X1-BETA-GATE1 && IsJumpGate() ✓

8. Execute jump:
   - API: POST /my/ships/PROBE-1/jump {systemSymbol: "X1-ALPHA"}
   - Response: nav data, cooldown, transaction
   - Ship now in system X1-ALPHA

9. Return to user:
   ✓ Navigated to jump gate: X1-BETA-GATE1
   ✓ Ship PROBE-1 jumped from X1-BETA to X1-ALPHA
   ⏱  Jump cooldown: 60 seconds
```

### Key Design Patterns

1. **Command Orchestration**: JumpShipCommand orchestrates multiple operations (find gate, navigate, jump)
2. **Mediator Pattern**: Uses mediator to dispatch sub-commands (FindNearestJumpGateQuery, NavigateRouteCommand)
3. **Separation of Concerns**: Navigation logic stays in NavigateRouteCommand, jump logic in JumpShipCommand
4. **Reusability**: Leverages existing route planning, fuel management, and navigation infrastructure
5. **Resilience**: Uses RouteExecutor's resilience patterns (reload ship state, wait for arrivals)

## BDD Test Scenarios

### Feature File: `test/bdd/features/application/jump_ship.feature`

```gherkin
Feature: Jump Ship Between Systems
  As a player
  I want to jump my ships between star systems
  So that I can explore the universe and access different markets

  Background:
    Given a player exists with ID 1
    And the current system is "X1-BETA"

  Scenario: Successfully jump when already at jump gate
    Given a ship "PROBE-1" exists with jump drive module
    And ship "PROBE-1" is at waypoint "X1-BETA-GATE" which is a jump gate
    And jump gate "X1-BETA-GATE" connects to system "X1-ALPHA"
    When I jump ship "PROBE-1" to system "X1-ALPHA"
    Then the jump should succeed
    And ship "PROBE-1" should be in system "X1-ALPHA"
    And a jump cooldown should be applied
    And the ship should not have navigated to a gate

  Scenario: Auto-navigate to jump gate then jump
    Given a ship "PROBE-1" exists with jump drive module
    And ship "PROBE-1" is at waypoint "X1-BETA-MARKETPLACE"
    And a jump gate "X1-BETA-GATE" exists 100 units away
    And jump gate "X1-BETA-GATE" connects to system "X1-ALPHA"
    When I jump ship "PROBE-1" to system "X1-ALPHA"
    Then ship "PROBE-1" should navigate to "X1-BETA-GATE"
    And the jump should succeed
    And ship "PROBE-1" should be in system "X1-ALPHA"
    And the response should indicate navigated to gate

  Scenario: Fail to jump - ship lacks jump drive module
    Given a ship "HAULER-1" exists without jump drive module
    And ship "HAULER-1" is at waypoint "X1-BETA-GATE" which is a jump gate
    When I jump ship "HAULER-1" to system "X1-ALPHA"
    Then the jump should fail with error "does not have a jump drive module"

  Scenario: Fail to jump - no jump gates in current system
    Given a ship "PROBE-1" exists with jump drive module
    And ship "PROBE-1" is at waypoint "X1-BETA-MARKETPLACE"
    And there are no jump gates in system "X1-BETA"
    When I jump ship "PROBE-1" to system "X1-ALPHA"
    Then the jump should fail with error "failed to find jump gate"

  Scenario: Select nearest jump gate when multiple exist
    Given a ship "PROBE-1" exists with jump drive module
    And ship "PROBE-1" is at waypoint "X1-BETA-MARKETPLACE"
    And a jump gate "X1-BETA-GATE1" exists 100 units away
    And a jump gate "X1-BETA-GATE2" exists 300 units away
    And both gates connect to system "X1-ALPHA"
    When I jump ship "PROBE-1" to system "X1-ALPHA"
    Then ship "PROBE-1" should navigate to "X1-BETA-GATE1"
    And the jump should succeed

  Scenario: Jump with cooldown tracking
    Given a ship "PROBE-1" exists with jump drive module
    And ship "PROBE-1" is at waypoint "X1-BETA-GATE" which is a jump gate
    And the API will return a 60 second cooldown
    When I jump ship "PROBE-1" to system "X1-ALPHA"
    Then the jump should succeed
    And the cooldown should be 60 seconds
    And the cooldown should be recorded in the response

  Scenario: Navigation to gate fails - insufficient fuel
    Given a ship "PROBE-1" exists with jump drive module
    And ship "PROBE-1" is at waypoint "X1-BETA-MARKETPLACE"
    And ship "PROBE-1" has 10 fuel units
    And a jump gate "X1-BETA-GATE" exists 200 units away requiring 50 fuel
    And there are no fuel sources between current location and gate
    When I jump ship "PROBE-1" to system "X1-ALPHA"
    Then the jump should fail during navigation
    And the error should indicate insufficient fuel
```

## Files to Create

1. **Documentation**
   - `docs/JUMP_BETWEEN_SYSTEMS_IMPLEMENTATION_PLAN.md` (this file)

2. **Domain Layer**
   - `internal/domain/navigation/ship_module.go` - ShipModule value object

3. **Application Layer**
   - `internal/application/ship/queries/find_jump_gate.go` - Find nearest jump gate query
   - `internal/application/ship/commands/jump_ship.go` - Jump ship command with auto-navigation

4. **Adapter Layer**
   - `internal/adapters/cli/jump.go` - CLI jump command

5. **Tests**
   - `test/bdd/features/application/jump_ship.feature` - BDD feature file
   - `test/bdd/steps/jump_steps.go` - Step definitions

## Files to Modify

1. **Domain Layer**
   - `internal/domain/navigation/ship.go` - Add Modules field, HasJumpDrive() method
   - `internal/domain/shared/waypoint.go` - Add IsJumpGate() method

2. **Adapter Layer**
   - `internal/adapters/api/client.go` - Add JumpShip(), GetJumpGate(), update GetShip()
   - `internal/adapters/cli/root.go` - Register jump command
   - `pkg/proto/daemon/daemon.proto` - Add JumpShip RPC
   - `internal/adapters/grpc/daemon_service_impl.go` - Implement JumpShip handler

3. **Application Setup**
   - Register JumpShipCommandHandler in mediator
   - Register FindNearestJumpGateHandler in mediator

## Testing Strategy

### Unit Tests (BDD Style)
All tests in `test/bdd/` directory following existing patterns:
- Ship module validation tests
- Jump gate discovery tests
- Auto-navigation integration tests
- Error case coverage

### Manual Testing Checklist
- [ ] Test with real SpaceTraders API account
- [ ] Verify ship at jump gate can jump immediately
- [ ] Verify ship not at gate navigates to nearest gate first
- [ ] Test with ship without jump drive (should fail validation)
- [ ] Test in system with no jump gates (should fail)
- [ ] Test in system with multiple jump gates (selects nearest)
- [ ] Verify cooldown is tracked correctly
- [ ] Test with insufficient fuel for navigation (should fail)

### Integration Points
- NavigateRouteCommand integration (auto-navigation)
- API client integration (real jump endpoint)
- gRPC/CLI integration (end-to-end flow)

## Success Criteria

### Functional Requirements
- ✅ CLI command works: `./bin/spacetraders ship jump --ship X --system Y`
- ✅ Validates ship has jump drive module before attempting
- ✅ Automatically finds and navigates to nearest jump gate
- ✅ Executes jump through SpaceTraders API
- ✅ Tracks jump cooldown
- ✅ Handles all error cases gracefully

### Technical Requirements
- ✅ Follows hexagonal architecture (domain → application → adapters)
- ✅ Uses existing NavigateRouteCommand for navigation
- ✅ Reuses existing route planning and fuel management
- ✅ All BDD tests pass
- ✅ No test files in production code directories
- ✅ Proper error handling and logging

### Code Quality
- ✅ Clear separation of concerns
- ✅ Dependency injection throughout
- ✅ Immutable value objects (ShipModule)
- ✅ Idiomatic Go code
- ✅ Comprehensive test coverage

## Future Enhancements (Out of Scope)

These features are NOT part of this implementation but could be added later:

1. **Multi-hop cross-system routing**
   - Plan routes through multiple systems
   - Multiple jumps to reach distant systems
   - Extend routing service for cross-system pathfinding

2. **Warp drive support**
   - Different mechanics from jump gates
   - Range-based direct system-to-system travel
   - Module-based capability checking

3. **Jump gate network visualization**
   - Query all jump gates in universe
   - Build graph of system connections
   - Shortest path between systems

4. **Automatic cross-system trading**
   - Background workers navigate across systems
   - Contract deliveries to other systems
   - Market arbitrage across systems

5. **Jump cost optimization**
   - If antimatter costs are added
   - Optimize for cheapest route
   - Balance cost vs time

## References

- [SpaceTraders API Documentation](https://spacetraders.stoplight.io/docs/spacetraders/11f2735b75b02-space-traders-api)
- [Get Jump Gate Endpoint](https://spacetraders.stoplight.io/docs/spacetraders/decd101af6414-get-jump-gate)
- [SpaceTraders API GitHub](https://github.com/SpaceTradersAPI/api-docs)
- [v2.1 Release Changelog](https://x.com/SpaceTradersAPI/status/1718341478978908354)
- SpaceTraders Go Bot CLAUDE.md - Architecture patterns and testing standards
- Existing implementation patterns in `internal/application/ship/commands/navigate_route.go`

## Implementation Timeline

This is a rough estimate for development time:

1. **API Research & Documentation** - 1-2 hours
   - Manual API testing
   - Document exact request/response formats
   - Update this document with findings

2. **Domain Layer** - 1 hour
   - ShipModule value object
   - Ship entity enhancements
   - Waypoint helper method

3. **API Client** - 1-2 hours
   - JumpShip endpoint
   - GetJumpGate endpoint
   - Update GetShip for modules

4. **Application Layer** - 3-4 hours
   - FindNearestJumpGateQuery
   - JumpShipCommand with orchestration
   - Integration with NavigateRouteCommand

5. **gRPC & CLI** - 2 hours
   - Protobuf definitions
   - gRPC handler
   - CLI command

6. **BDD Tests** - 3-4 hours
   - Feature file scenarios
   - Step definitions
   - Mock API responses

7. **Testing & Refinement** - 2-3 hours
   - Manual testing with real API
   - Bug fixes
   - Documentation updates

**Total Estimated Time: 13-18 hours**

## Notes

- This feature is the foundation for future cross-system capabilities
- Keeping scope limited to manual single jumps makes it achievable and testable
- Auto-navigation integration demonstrates good reuse of existing infrastructure
- BDD tests ensure reliability and prevent regressions
- Architecture follows established patterns in the codebase
