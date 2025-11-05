Feature: Daemon Event Loop Responsiveness
  As an autonomous bot operator
  I need the daemon to remain responsive during long-running operations
  So that I can monitor and control containers without waiting for operations to complete

  Scenario: Daemon responds quickly while container sleeps
    Given the daemon server is running
    When I create a test container that sleeps for 10 seconds
    And I wait for the container to start sleeping
    Then I should be able to list containers within 1 second
    And I should be able to inspect the container within 1 second
    And the container should show status "RUNNING"
