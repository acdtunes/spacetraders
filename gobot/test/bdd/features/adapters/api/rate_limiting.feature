Feature: Rate Limiting
  As a SpaceTraders API client
  I want automatic rate limiting
  So that I respect the API's 2 requests/second limit and avoid 429 errors

  Background:
    Given a rate limiter with limit 2 requests per second and burst 2

  # Basic Rate Limiting
  Scenario: Allow burst of requests within limit
    Given the rate limiter has full capacity
    When I make 2 API requests simultaneously
    Then both requests should proceed immediately
    And the rate limiter should have 0 tokens available

  Scenario: Throttle third request in same second
    Given the rate limiter has full capacity
    When I make 2 API requests at time 0ms
    And I make 1 API request at time 100ms
    Then the first 2 requests should proceed immediately
    And the third request should be delayed

  Scenario: Rate limiter refills after 1 second
    Given I made 2 requests 1 second ago
    And the rate limiter has refilled
    When I make 2 new API requests
    Then both requests should proceed immediately

  # Concurrent Request Handling
  Scenario: Multiple concurrent requests respect rate limit
    When I make 5 concurrent API requests
    Then requests 1-2 should proceed immediately
    And requests 3-5 should be queued and throttled
    And all 5 requests should eventually complete

  Scenario: Rate limiter is thread-safe under concurrent load
    When I make 100 concurrent API requests from multiple goroutines
    Then all requests should eventually complete
    And the rate should never exceed 2 requests per second

  # Token Bucket Behavior
  Scenario: Burst allows immediate requests up to burst size
    Given the rate limiter has full capacity
    When I make 2 requests at exactly the same time
    Then both requests should proceed without delay
    And the rate limiter tokens should be 0

  Scenario: Tokens refill at specified rate
    Given the rate limiter is empty
    When I wait 1 second
    Then the rate limiter should have 2 tokens available

  Scenario: Tokens do not exceed burst capacity
    Given the rate limiter is empty
    When I wait 10 seconds
    Then the rate limiter should have 2 tokens available
    And tokens should not exceed the burst size

  # Edge Cases
  Scenario: Single request does not get throttled
    Given the rate limiter has full capacity
    When I make 1 API request
    Then the request should proceed immediately

  Scenario: Requests spread over time do not trigger throttling
    Given the rate limiter has full capacity
    When I make a request at time 0s
    And I make a request at time 1s
    And I make a request at time 2s
    Then all 3 requests should proceed immediately

  Scenario: Rate limiter allows sustained rate of 2 req/sec
    When I make requests at times [0s, 0s, 1s, 1s, 2s, 2s, 3s, 3s]
    Then all 8 requests should complete
    And total execution time should be approximately 3 seconds

  # Context Cancellation
  Scenario: Waiting request is cancelled when context is cancelled
    Given the rate limiter is empty
    When I make a request with a cancellable context
    And I cancel the context while waiting for rate limiter
    Then the request should fail with context cancelled error
    And no API call should be made

  # Integration with API Client
  Scenario: Rate limiter prevents bursting beyond API limits
    Given an API client with rate limiting enabled
    When I make 10 rapid requests to get ship data
    Then the requests should be spread over at least 4 seconds
    And no 429 rate limit errors should occur

  Scenario: Rate limiter works with retry logic
    Given an API client with rate limiting and retry enabled
    And the API returns 503 for first 2 attempts
    When I make a request to get ship "SHIP-1"
    Then the request should succeed after retries
    And all retry attempts should also respect rate limiting

  # Performance
  Scenario: Rate limiter has minimal overhead for allowed requests
    Given the rate limiter has full capacity
    When I make 2 requests with timing measurement
    Then each request should have less than 1ms rate limiter overhead

  Scenario: Rate limiter accurately delays throttled requests
    Given the rate limiter is empty
    When I make a request and measure wait time
    Then the wait time should be approximately 500ms
    And the request should then proceed

  # Configuration Validation
  Scenario: Rate limiter validates positive rate
    When I create a rate limiter with rate 0
    Then creation should fail with "rate must be positive"

  Scenario: Rate limiter validates positive burst
    When I create a rate limiter with burst 0
    Then creation should fail with "burst must be positive"
