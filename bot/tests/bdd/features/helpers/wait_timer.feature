Feature: Wait timer helper
  Scenario: Wait timer sleeps and logs progress
    Given the wait timer is patched to capture output
    When the wait timer runs for 2 seconds
    Then the wait timer reports the wait duration
    And the wait timer calls sleep with 2 seconds
