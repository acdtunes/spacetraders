Feature: Helpers regression coverage

  Scenario Outline: Legacy regression <name>
    When I execute regression "<module>" "<callable>"
    Then the regression completes successfully

    Examples:
      | name | module | callable |
      | captain logs root creates directories | tests.unit.helpers.test_paths | regression_captain_logs_root_creates_directories |
