Feature: Daemon Python Bytecode Cache Prevention
  As a bot administrator
  I want daemon processes to prevent Python bytecode cache creation
  So that the codebase stays clean without __pycache__ directories

  Background:
    Given a daemon manager for player 999

  Scenario: Inject -B flag for basic Python command
    Given a Python command: python3 script.py --arg value
    When I inject the no-cache flag
    Then the command should start with: python3 -B
    And the remaining arguments should be: script.py --arg value

  Scenario: Inject -B flag for module mode
    Given a Python command: python3 -m spacetraders_bot.cli trade
    When I inject the no-cache flag
    Then the command should start with: python3 -B
    And the remaining arguments should be: -m spacetraders_bot.cli trade

  Scenario: Inject -B flag preserves existing flags
    Given a Python command: python3 -u -W ignore script.py
    When I inject the no-cache flag
    Then the command should start with: python3 -B
    And the remaining arguments should be: -u -W ignore script.py

  Scenario: -B flag injection is idempotent
    Given a Python command: python3 -B script.py
    When I inject the no-cache flag
    Then the command should have exactly one -B flag
    And the command should be: python3 -B script.py

  Scenario: Inject -B flag with full Python path
    Given a Python command: /usr/bin/python3.12 script.py
    When I inject the no-cache flag
    Then the command should start with: /usr/bin/python3.12 -B
    And the remaining arguments should be: script.py

  Scenario: Non-Python commands are unchanged
    Given a non-Python command: bash script.sh
    When I inject the no-cache flag
    Then the command should remain unchanged

  Scenario: Empty command is handled safely
    Given an empty command
    When I inject the no-cache flag
    Then the command should remain empty

  Scenario: Environment includes PYTHONDONTWRITEBYTECODE
    Given a daemon manager instance
    When I inspect the daemon start method source code
    Then the source should contain "PYTHONDONTWRITEBYTECODE"
    And the source should contain "env['PYTHONDONTWRITEBYTECODE'] = '1'"
