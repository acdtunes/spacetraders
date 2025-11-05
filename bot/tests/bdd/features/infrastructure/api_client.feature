Feature: SpaceTraders API Client
  As an API client
  I want to interact with the SpaceTraders API
  So that I can manage ships, agents, and waypoints with proper rate limiting

  Background:
    Given the API client is initialized with token "test-token-123"

  # Initialization
  Scenario: API client initializes with valid token
    Then the client should have token "test-token-123"
    And the session should have Authorization header "Bearer test-token-123"
    And the session should have Content-Type header "application/json"

  Scenario: API client initializes rate limiter with correct parameters
    Then the rate limiter should be initialized with 2 requests per second

  # Agent Operations
  Scenario: Get agent returns agent data
    Given the API will return agent data {"symbol": "TEST_AGENT"}
    When I call get_agent
    Then the rate limiter should be acquired
    And the result should contain {"symbol": "TEST_AGENT"}
    And the request should be GET to "https://api.spacetraders.io/v2/my/agent"

  # Ship Operations
  Scenario: Get ship returns ship data
    Given the API will return ship data {"symbol": "SHIP-1"}
    When I call get_ship with ship_symbol "SHIP-1"
    Then the result should contain {"symbol": "SHIP-1"}
    And the request should be GET to "https://api.spacetraders.io/v2/my/ships/SHIP-1"

  Scenario: Get ships returns all ships
    Given the API will return ships list [{"symbol": "SHIP-1"}, {"symbol": "SHIP-2"}]
    When I call get_ships
    Then the result should contain list with 2 ships
    And the request should be GET to "https://api.spacetraders.io/v2/my/ships"

  Scenario: Navigate ship sends waypoint
    Given the API will return navigation data {"nav": {"status": "IN_TRANSIT"}}
    When I call navigate_ship with ship_symbol "SHIP-1" and waypoint "X1-A1"
    Then the result should contain navigation status "IN_TRANSIT"
    And the request should be POST to "https://api.spacetraders.io/v2/my/ships/SHIP-1/navigate"
    And the request body should contain {"waypointSymbol": "X1-A1"}

  Scenario: Dock ship sends dock request
    Given the API will return dock data {"nav": {"status": "DOCKED"}}
    When I call dock_ship with ship_symbol "SHIP-1"
    Then the result should contain navigation status "DOCKED"
    And the request should be POST to "https://api.spacetraders.io/v2/my/ships/SHIP-1/dock"

  Scenario: Orbit ship sends orbit request
    Given the API will return orbit data {"nav": {"status": "IN_ORBIT"}}
    When I call orbit_ship with ship_symbol "SHIP-1"
    Then the result should contain navigation status "IN_ORBIT"
    And the request should be POST to "https://api.spacetraders.io/v2/my/ships/SHIP-1/orbit"

  Scenario: Refuel ship sends refuel request
    Given the API will return refuel data {"fuel": {"current": 100}}
    When I call refuel_ship with ship_symbol "SHIP-1"
    Then the result should contain fuel current 100
    And the request should be POST to "https://api.spacetraders.io/v2/my/ships/SHIP-1/refuel"

  # Waypoint Operations
  Scenario: List waypoints with default pagination
    Given the API will return empty waypoints list
    When I call list_waypoints with system_symbol "X1"
    Then the request should be GET to "https://api.spacetraders.io/v2/systems/X1/waypoints"
    And the request params should include page 1
    And the request params should include limit 20

  Scenario: List waypoints with custom pagination
    Given the API will return empty waypoints list
    When I call list_waypoints with system_symbol "X1" and page 3 and limit 50
    Then the request params should include page 3
    And the request params should include limit 50

  # Rate Limiting
  Scenario: All methods acquire rate limiter token
    Given the API will return success for all requests
    When I call all API methods
    Then the rate limiter should be acquired 8 times

  # Error Handling - 429 Rate Limit
  Scenario: Handle 429 rate limit with retry
    Given the API will return 429 on first call then succeed
    When I call get_agent
    Then the request should be retried once
    And the client should sleep for 1.0 seconds
    And the result should be successful

  Scenario: Handle 429 with multiple retries
    Given the API will return 429 twice then succeed
    When I call get_agent
    Then the request should be retried twice
    And the client should sleep twice
    And the result should be successful

  Scenario: Raise RuntimeError after max 429 retries
    Given the API will always return 429
    When I attempt to call get_agent
    Then the call should fail with RuntimeError
    And the error message should mention "Request failed after 3 attempts"

  # Error Handling - 4xx Client Errors
  Scenario: Raise error on 400 client error
    Given the API will return 400 Bad Request
    When I attempt to call get_agent
    Then the call should fail with HTTPError

  # Error Handling - 5xx Server Errors
  Scenario: Raise error on 500 server error after retries
    Given the API will always return 500 Server Error
    When I attempt to call get_agent
    Then the call should fail with HTTPError
    And the request should be retried 3 times

  # Error Handling - Network Errors
  Scenario: Retry on connection error
    Given the API will raise ConnectionError twice then succeed
    When I call get_agent
    Then the request should be retried twice
    And the result should be successful

  Scenario: Raise error after max connection retries
    Given the API will always raise ConnectionError
    When I attempt to call get_agent
    Then the call should fail with ConnectionError
    And the request should be retried 3 times

  # Base URL Configuration
  Scenario: Use correct SpaceTraders API base URL
    Given the API will return agent data
    When I call get_agent
    Then the request URL should start with "https://api.spacetraders.io/v2/"

  # Contract Operations
  Scenario: Get contracts with pagination
    Given the API will return contracts list
    When I call get_contracts with page 1 and limit 20
    Then the request should be GET to "https://api.spacetraders.io/v2/my/contracts"
    And the request params should include page 1
    And the request params should include limit 20

  Scenario: Get specific contract
    Given the API will return contract data {"id": "contract-123"}
    When I call get_contract with contract_id "contract-123"
    Then the result should contain contract id "contract-123"
    And the request should be GET to "https://api.spacetraders.io/v2/my/contracts/contract-123"

  Scenario: Accept contract
    Given the API will return accepted contract data
    When I call accept_contract with contract_id "contract-123"
    Then the request should be POST to "https://api.spacetraders.io/v2/my/contracts/contract-123/accept"

  Scenario: Deliver contract goods
    Given the API will return delivery confirmation
    When I call deliver_contract with contract_id "contract-123" ship_symbol "SHIP-1" trade_symbol "IRON_ORE" and units 50
    Then the request should be POST to "https://api.spacetraders.io/v2/my/contracts/contract-123/deliver"
    And the delivery request body should be correct

  Scenario: Fulfill contract
    Given the API will return fulfillment confirmation
    When I call fulfill_contract with contract_id "contract-123"
    Then the request should be POST to "https://api.spacetraders.io/v2/my/contracts/contract-123/fulfill"

  Scenario: Negotiate contract
    Given the API will return new contract data
    When I call negotiate_contract with ship_symbol "SHIP-1"
    Then the request should be POST to "https://api.spacetraders.io/v2/my/ships/SHIP-1/negotiate/contract"
