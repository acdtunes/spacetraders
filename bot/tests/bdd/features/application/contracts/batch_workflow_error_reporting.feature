Feature: Batch Contract Workflow Error Reporting
  As a bot operator
  I want to see detailed error messages when batch workflow operations fail
  So that I can understand which operations failed and why

  Scenario: Batch workflow with all failures reports errors
    Given a batch workflow command with 3 iterations
    And all contract negotiations will fail with error "No faction available"
    When I execute the batch workflow
    Then the result should show 0 contracts fulfilled
    And the result should show 3 failed operations
    And the error list should contain "No faction available"

  Scenario: Batch workflow with mixed success and failures tracks both
    Given a batch workflow command with 3 iterations
    And iteration 1 will succeed
    And iteration 2 will fail with "Rate limit exceeded"
    And iteration 3 will succeed
    When I execute the batch workflow
    Then the result should show 2 contracts fulfilled
    And the result should show 1 failed operation
    And the error list should contain "Rate limit exceeded"
