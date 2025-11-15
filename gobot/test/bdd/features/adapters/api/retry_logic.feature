Feature: Exponential Backoff Retry Logic
  As a SpaceTraders API client
  I want automatic retries with exponential backoff
  So that transient failures don't cause permanent errors

  Background:
    Given an API client with max retries 3 and initial backoff 1 second

  # Successful Scenarios
  Scenario: Request succeeds on first attempt
    Given the mock API is configured to respond with status 200
    When I make an API request to get ship "SHIP-1"
    Then the request should succeed
    And exactly 1 HTTP request should have been made

  Scenario: Request succeeds on second attempt after transient failure
    Given the mock API is configured to:
      | attempt | status |
      | 1       | 503    |
      | 2       | 200    |
    When I make an API request to get ship "SHIP-1"
    Then the request should succeed
    And exactly 2 HTTP requests should have been made
    And there should be 1 retry attempt

  Scenario: Request succeeds on third attempt with exponential backoff
    Given the mock API is configured to:
      | attempt | status |
      | 1       | 503    |
      | 2       | 503    |
      | 3       | 200    |
    When I make an API request to get ship "SHIP-1"
    Then the request should succeed
    And exactly 3 HTTP requests should have been made
    And there should be 2 retry attempts

  # Retryable HTTP Status Codes
  Scenario: Retry on 429 Too Many Requests
    Given the mock API is configured to:
      | attempt | status |
      | 1       | 429    |
      | 2       | 200    |
    When I make an API request to get ship "SHIP-1"
    Then the request should succeed
    And exactly 2 HTTP requests should have been made

  Scenario: Retry on 500 Internal Server Error
    Given the mock API is configured to:
      | attempt | status |
      | 1       | 500    |
      | 2       | 200    |
    When I make an API request to get ship "SHIP-1"
    Then the request should succeed

  Scenario: Retry on 502 Bad Gateway
    Given the mock API is configured to:
      | attempt | status |
      | 1       | 502    |
      | 2       | 200    |
    When I make an API request to get ship "SHIP-1"
    Then the request should succeed

  Scenario: Retry on 503 Service Unavailable
    Given the mock API is configured to:
      | attempt | status |
      | 1       | 503    |
      | 2       | 200    |
    When I make an API request to get ship "SHIP-1"
    Then the request should succeed

  Scenario: Retry on 504 Gateway Timeout
    Given the mock API is configured to:
      | attempt | status |
      | 1       | 504    |
      | 2       | 200    |
    When I make an API request to get ship "SHIP-1"
    Then the request should succeed

  # Non-Retryable HTTP Status Codes
  Scenario: Do not retry on 400 Bad Request
    Given the mock API is configured to respond with status 400
    When I make an API request to get ship "INVALID"
    Then the request should fail with error containing "status 400"
    And exactly 1 HTTP request should have been made

  Scenario: Do not retry on 401 Unauthorized
    Given the mock API is configured to respond with status 401
    When I make an API request to get ship "SHIP-1"
    Then the request should fail with error containing "status 401"
    And exactly 1 HTTP request should have been made

  Scenario: Do not retry on 403 Forbidden
    Given the mock API is configured to respond with status 403
    When I make an API request to get ship "SHIP-1"
    Then the request should fail with error containing "status 403"
    And exactly 1 HTTP request should have been made

  Scenario: Do not retry on 404 Not Found
    Given the mock API is configured to respond with status 404
    When I make an API request to get ship "NONEXISTENT"
    Then the request should fail with error containing "status 404"
    And exactly 1 HTTP request should have been made

  Scenario: Do not retry on 409 Conflict
    Given the mock API is configured to respond with status 409
    When I make an API request to navigate ship "SHIP-1"
    Then the request should fail with error containing "status 409"
    And exactly 1 HTTP request should have been made

  # Max Retries Exhausted
  Scenario: Fail after max retries exceeded
    Given the mock API is configured to always respond with status 503
    When I make an API request to get ship "SHIP-1"
    Then the request should fail with error containing "max retries"
    And exactly 4 HTTP requests should have been made

  Scenario: Exponential backoff delays increase correctly
    Given the mock API is configured to:
      | attempt | status |
      | 1       | 503    |
      | 2       | 503    |
      | 3       | 503    |
      | 4       | 200    |
    When I make an API request to get ship "SHIP-1"
    Then the request should succeed
    And the retry delays should follow exponential backoff pattern [1s, 2s, 4s]

  # Network Errors
  Scenario: Retry on connection refused
    Given the mock API is configured to:
      | attempt | behavior          |
      | 1       | connection_refused|
      | 2       | 200               |
    When I make an API request to get ship "SHIP-1"
    Then the request should succeed
    And exactly 2 HTTP requests should have been made

  Scenario: Retry on timeout
    Given the mock API is configured to:
      | attempt | behavior |
      | 1       | timeout  |
      | 2       | 200      |
    When I make an API request to get ship "SHIP-1"
    Then the request should succeed
    And exactly 2 HTTP requests should have been made

  Scenario: Do not retry on context cancelled
    Given the mock API is configured to always respond with status 503
    And the request context will be cancelled after 1 attempt
    When I make an API request to get ship "SHIP-1"
    Then the request should fail with error containing "context"
    And at most 1 HTTP request should have been made

  # Retry-After Header
  Scenario: Respect Retry-After header on 429 response
    Given the mock API is configured to:
      | attempt | status | retry_after |
      | 1       | 429    | 5           |
      | 2       | 200    |             |
    When I make an API request to get ship "SHIP-1"
    Then the request should succeed
    And the retry delay should be 5 seconds

  Scenario: Use exponential backoff when Retry-After is not provided
    Given the mock API is configured to respond with status 429 without Retry-After
    And then respond with status 200
    When I make an API request to get ship "SHIP-1"
    Then the request should succeed
    And the retry delay should be 1 second
