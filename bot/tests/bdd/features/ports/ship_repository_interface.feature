Feature: Ship Repository Interface (API-Only)
  As a developer using the ship repository
  I want an interface that only provides read operations
  So that ship data is always fresh from the API

  Scenario: Ship repository interface has only read methods
    Given the IShipRepository interface
    Then it should have method "find_by_symbol"
    And it should have method "find_all_by_player"
    And it should not have method "create"
    And it should not have method "update"
    And it should not have method "delete"
    And it should not have method "sync_from_api"
