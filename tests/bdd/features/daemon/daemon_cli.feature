Feature: Daemon CLI Module Execution
  As a developer
  I need the daemon module to be executable
  So that I can run it as a Python module

  Scenario: Execute daemon server as module
    When I run the daemon module with "python -m spacetraders.adapters.primary.daemon.daemon_server"
    Then the module should start without errors
    And the main function should be called
