Feature: Pipeline Behaviors
  As the CQRS mediator
  I want to execute behaviors in the pipeline
  So that I can add cross-cutting concerns like logging and validation

  # LoggingBehavior Tests

  Scenario: LoggingBehavior logs request execution start
    Given the logging behavior is initialized
    And a test request named "TestRequest"
    And a mock next handler that returns success
    When I execute the logging behavior with the request
    Then the logger should log "Executing TestRequest"

  Scenario: LoggingBehavior logs request completion
    Given the logging behavior is initialized
    And a test request named "TestRequest"
    And a mock next handler that returns success
    When I execute the logging behavior with the request
    Then the logger should log "Successfully completed TestRequest"

  Scenario: LoggingBehavior calls next handler
    Given the logging behavior is initialized
    And a test request named "TestRequest"
    And a mock next handler that returns success
    When I execute the logging behavior with the request
    Then the next handler should be called
    And the result should be the handler response

  Scenario: LoggingBehavior returns handler response
    Given the logging behavior is initialized
    And a test request named "TestRequest"
    And a mock next handler that returns data "test_data"
    When I execute the logging behavior with the request
    Then the result should contain data "test_data"

  Scenario: LoggingBehavior logs error on exception
    Given the logging behavior is initialized
    And a test request named "TestRequest"
    And a mock next handler that raises ValueError "Test error"
    When I execute the logging behavior with the request
    Then the execution should fail with ValueError "Test error"
    And the logger should log error containing "Failed executing TestRequest"
    And the logger should log error containing "Test error"

  Scenario: LoggingBehavior logs error with exception info
    Given the logging behavior is initialized
    And a test request named "TestRequest"
    And a mock next handler that raises RuntimeError "Critical error"
    When I execute the logging behavior with the request
    Then the execution should fail with RuntimeError "Critical error"
    And the logger should log error with exc_info true

  Scenario: LoggingBehavior reraises exception after logging
    Given the logging behavior is initialized
    And a test request named "TestRequest"
    And a mock next handler that raises ValueError "Test error"
    When I execute the logging behavior with the request
    Then the execution should fail with ValueError "Test error"

  Scenario: LoggingBehavior logs different request types
    Given the logging behavior is initialized
    And a test request named "GetPlayerQuery"
    And a test request named "NavigateShipCommand"
    And a mock next handler that returns success
    When I execute the logging behavior with all requests
    Then the logger should log "GetPlayerQuery"
    And the logger should log "NavigateShipCommand"

  # ValidationBehavior Tests

  Scenario: ValidationBehavior calls validate when present
    Given the validation behavior is initialized
    And a test request with validate method
    And a mock next handler that returns success
    When I execute the validation behavior with the request
    Then the validate method should be called

  Scenario: ValidationBehavior does not fail when no validate method
    Given the validation behavior is initialized
    And a test request without validate method
    And a mock next handler that returns success
    When I execute the validation behavior with the request
    Then the execution should succeed
    And the result should be the handler response

  Scenario: ValidationBehavior calls next handler after validation
    Given the validation behavior is initialized
    And a test request with validate method
    And a mock next handler that returns success
    When I execute the validation behavior with the request
    Then the validate method should be called
    And the next handler should be called

  Scenario: ValidationBehavior returns handler response
    Given the validation behavior is initialized
    And a test request with validate method
    And a mock next handler that returns validated true
    When I execute the validation behavior with the request
    Then the result should contain validated true

  Scenario: ValidationBehavior propagates validation error
    Given the validation behavior is initialized
    And a test request with validate method that raises ValueError "Invalid data"
    And a mock next handler that returns success
    When I execute the validation behavior with the request
    Then the execution should fail with ValueError "Invalid data"
    And the next handler should not be called

  Scenario: ValidationBehavior checks validate is callable
    Given the validation behavior is initialized
    And a test request with non-callable validate attribute
    And a mock next handler that returns success
    When I execute the validation behavior with the request
    Then the execution should succeed
    And the result should be the handler response

  Scenario: ValidationBehavior validates before calling handler
    Given the validation behavior is initialized
    And a test request with validate method that tracks order
    And a mock next handler that tracks order
    When I execute the validation behavior with the request
    Then the call order should be "validate" then "handler"

  Scenario: ValidationBehavior propagates handler exceptions
    Given the validation behavior is initialized
    And a test request with validate method
    And a mock next handler that raises RuntimeError "Handler error"
    When I execute the validation behavior with the request
    Then the execution should fail with RuntimeError "Handler error"

  # Behavior Chaining Tests

  Scenario: Logging then validation order in pipeline
    Given the logging behavior is initialized
    And the validation behavior is initialized
    And a test request with validate method that tracks order
    And a final handler that tracks order
    When I execute the behavior pipeline with logging then validation
    Then the call order should be "validate" then "handler"

  Scenario: Behaviors pass response through chain
    Given the logging behavior is initialized
    And the validation behavior is initialized
    And a test request with validate method
    And a final handler that returns final_result
    When I execute the behavior pipeline with logging then validation
    Then the result should contain data "final_result"

  Scenario: Validation error stops chain
    Given the logging behavior is initialized
    And the validation behavior is initialized
    And a test request with validate method that raises ValueError "Validation failed"
    And a final handler that tracks calls
    When I execute the behavior pipeline with logging then validation
    Then the execution should fail with ValueError "Validation failed"
    And the final handler should not be called

  Scenario: Handler error logged by logging behavior
    Given the logging behavior is initialized
    And a test request named "TestRequest"
    And a failing handler that raises RuntimeError "Handler failed"
    When I execute the logging behavior with the request
    Then the execution should fail with RuntimeError "Handler failed"
    And the logger should log error once
