Feature: Core Library Components
  Tests for core infrastructure: API client, smart navigator, daemon manager, routing

  # API Client Tests
  Scenario: APIResult helper creates success result
    Given an APIResult with success data
    When I create a success result with data and status 200
    Then the result should be ok
    And the result data should match the input
    And the status code should be 200

  Scenario: APIResult helper creates failure result
    Given an APIResult with error data
    When I create a failure result with error and status 400
    Then the result should not be ok
    And the result error should match the input
    And the status code should be 400

  Scenario: API request handles successful response
    Given an API client with rate limiting disabled
    And a mocked GET request returning ship data
    When I execute request_result for "/my/ships/ship-1"
    Then the result should be successful
    And the response data should contain ship information

  Scenario: API request preserves client error payload
    Given an API client with rate limiting disabled
    And a mocked 404 response with error details
    When I execute request_result for "/my/ships/missing"
    Then the result should be an error
    And the error payload should be preserved

  Scenario: API request wraps client error response
    Given an API client with rate limiting disabled
    And a mocked 404 response
    When I execute raw request for "/my/ships/missing"
    Then the raw response should contain the error payload

  Scenario: Rate limiter retries until success
    Given an API client with rate limiting disabled
    And a mocked request that fails with 429 then succeeds
    When I execute request_result with max retries 3
    Then the request should eventually succeed
    And the retry count should be 2

  Scenario: Server failure returns None
    Given an API client with rate limiting disabled
    And a mocked 503 server error response
    When I execute raw request with max retries 1
    Then the result should be None

  # Smart Navigator Tests
  Scenario: Validate ship health before navigation
    Given a smart navigator for system "X1-UNIT"
    And a ship with health integrity less than 50%
    When I validate route to destination
    Then validation should fail with "health" error

  # Daemon Manager Tests
  Scenario: Daemon status checks running process
    Given a daemon manager
    And a running daemon process with PID
    When I check daemon status
    Then status should show "running"

  # OR-Tools Routing Tests
  Scenario: Build VRP solution for mining route
    Given waypoints with mining resources
    And a ship with cargo capacity
    When I solve VRP problem
    Then solution should visit all waypoints
    And solution should minimize distance

  # Route Optimizer Tests
  Scenario: Optimize multi-waypoint route
    Given a list of waypoints to visit
    When I optimize the route
    Then the route should be ordered efficiently
    And total distance should be minimized

  # Scout Coordinator Tests
  Scenario: Coordinate scout assignments
    Given multiple scout ships
    And market waypoints to survey
    When I assign scouts to markets
    Then each market should have one scout
    And no scout should be double-assigned

  # Tour Optimizer Tests
  Scenario: Optimize scout tour using 2-opt
    Given a set of market waypoints
    When I optimize tour with 2-opt algorithm
    Then tour length should be reduced
    And all waypoints should be visited once
