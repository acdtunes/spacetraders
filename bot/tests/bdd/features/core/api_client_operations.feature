Feature: API Client Operations
  As a bot developer
  I want a robust API client with proper error handling and retries
  So that all API interactions are reliable and well-tested

  Background:
    Given the API client is configured with a valid token

  Scenario: Successful GET request
    When I make a GET request to "/my/agent"
    Then the request should succeed
    And the response should contain data

  Scenario: GET request with 404 error
    Given the endpoint "/my/ships/INVALID-SHIP" will return 404
    When I make a GET request to "/my/ships/INVALID-SHIP"
    Then the request should fail
    And no data should be returned

  Scenario: Successful POST request
    When I make a POST request to "/my/ships/TEST-1/dock" with no data
    Then the request should succeed
    And the response should contain data

  Scenario: POST request with data payload
    Given I have POST data with "waypointSymbol" set to "X1-HU87-B9"
    When I make a POST request to "/my/ships/TEST-1/navigate" with the data
    Then the request should succeed
    And the response should contain navigation data

  Scenario: PATCH request updates ship flight mode
    Given I have PATCH data with "flightMode" set to "DRIFT"
    When I make a PATCH request to "/my/ships/TEST-1/nav" with the data
    Then the request should succeed
    And the response should contain nav data

  Scenario: Request with rate limiting retry
    Given the API will return rate limit error then succeed
    When I make a GET request to "/my/agent"
    Then the request should eventually succeed after retry
    And the rate limiter should have waited

  Scenario: Request with server error retry
    Given the API will return 500 error 2 times then succeed
    When I make a GET request to "/my/agent"
    Then the request should eventually succeed after retries
    And there should be 2 retry attempts

  Scenario: Request fails after max retries
    Given the API will always return 500 error
    When I make a GET request to "/my/agent" with max retries 3
    Then the request should fail after max retries
    And no data should be returned

  Scenario: Request with connection error retry
    Given the API will have connection error then succeed
    When I make a GET request to "/my/agent"
    Then the request should eventually succeed after retry

  Scenario: Request with timeout retry
    Given the API will timeout then succeed
    When I make a GET request to "/my/agent"
    Then the request should eventually succeed after retry

  Scenario: Invalid JSON response handling
    Given the API will return invalid JSON
    When I make a GET request to "/my/agent"
    Then the request should fail
    And an error should be logged about JSON parsing

  Scenario: Unsupported HTTP method
    When I make a DELETE request to "/my/agent"
    Then the request should fail
    And the error should be logged about unsupported method

  Scenario: Client error 400 should not retry
    Given the API will return 400 bad request error
    When I make a GET request to "/my/ships/TEST-1"
    Then the request should fail immediately
    And there should be no retry attempts

  Scenario: Authentication with bearer token
    When I make a GET request to "/my/agent"
    Then the request headers should include bearer token
    And the authorization header should be correctly formatted

  Scenario: List waypoints with pagination - first page
    Given the system "X1-HU87" has 50 waypoints
    When I list waypoints for system "X1-HU87" with limit 20 and page 1
    Then the request should succeed
    And the response should contain 20 waypoints
    And the meta should show total of 50 and page 1

  Scenario: List waypoints with pagination - second page
    Given the system "X1-HU87" has 50 waypoints
    When I list waypoints for system "X1-HU87" with limit 20 and page 2
    Then the request should succeed
    And the response should contain 20 waypoints
    And the meta should show total of 50 and page 2

  Scenario: List waypoints with trait filter
    Given the system "X1-HU87" has waypoints with various traits
    When I list waypoints for system "X1-HU87" with trait filter "MARKETPLACE"
    Then the request should succeed
    And all returned waypoints should have the "MARKETPLACE" trait

  Scenario: Get agent details convenience method
    When I call the get_agent convenience method
    Then the request should succeed
    And the response should contain agent data with symbol and credits

  Scenario: Get ship details convenience method
    When I call the get_ship convenience method for "TEST-1"
    Then the request should succeed
    And the response should contain ship data with symbol "TEST-1"

  Scenario: List all ships convenience method
    When I call the list_ships convenience method
    Then the request should succeed
    And the response should contain a list of ships

  Scenario: Get contract details convenience method
    When I call the get_contract convenience method for "CONTRACT-1"
    Then the request should succeed
    And the response should contain contract data

  Scenario: List all contracts convenience method
    When I call the list_contracts convenience method
    Then the request should succeed
    And the response should contain a list of contracts

  Scenario: Get market data convenience method
    When I call the get_market convenience method for system "X1-HU87" and waypoint "X1-HU87-A1"
    Then the request should succeed
    And the response should contain market data

  Scenario: Get waypoint details convenience method
    When I call the get_waypoint convenience method for system "X1-HU87" and waypoint "X1-HU87-A1"
    Then the request should succeed
    And the response should contain waypoint data with symbol "X1-HU87-A1"

  Scenario: Rate limiter enforces minimum interval
    Given the rate limiter has minimum interval 0.6 seconds
    When I make 3 consecutive GET requests
    Then each request should be separated by at least 0.6 seconds
    And the total time should be at least 1.2 seconds

  Scenario: Exponential backoff on retries
    Given the API will return 500 error 3 times then succeed
    When I make a GET request to "/my/agent"
    Then the retry wait times should increase exponentially
    And the wait times should be 2s, 4s, 8s

  Scenario: Exponential backoff caps at 60 seconds
    Given the API will return 500 error 10 times then succeed
    When I make a GET request to "/my/agent"
    Then no wait time should exceed 60 seconds

  Scenario: Response parsing extracts data field
    When I call the get_agent convenience method
    Then the raw response should have a "data" field
    And the convenience method should return only the data field contents

  Scenario: POST request with empty data object
    When I make a POST request to "/my/ships/TEST-1/orbit" with empty data
    Then the request should succeed
    And the POST should include an empty JSON object

  Scenario: Rate limit error message detection
    Given the API will return error with message "rate limit exceeded"
    When I make a GET request to "/my/agent"
    Then the request should be treated as rate limited
    And the client should wait and retry

  Scenario: Successful 201 response handling
    When I make a POST request that returns 201 created
    Then the request should be considered successful
    And the response data should be returned

  Scenario: Network error with max retries exceeded
    Given the API will always have connection errors
    When I make a GET request to "/my/agent" with max retries 3
    Then the request should fail after max retries
    And the last error should be about connection

  Scenario: Generic request exception handling
    Given the API will raise a generic request exception
    When I make a GET request to "/my/agent"
    Then the request should fail
    And the error should be logged with exception details

  Scenario: Unexpected status code 300
    Given the API will return status code 300
    When I make a GET request to "/my/agent"
    Then the request should fail
    And no data should be returned

  Scenario: Timeout with max retries exceeded
    Given the API will always timeout
    When I make a GET request to "/my/agent" with max retries 2
    Then the request should fail after max retries
    And no data should be returned

  Scenario: PATCH convenience method
    Given I have PATCH data with "flightMode" set to "CRUISE"
    When I call the patch convenience method
    Then the request should succeed
    And the response should contain data
