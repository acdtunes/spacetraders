Feature: Cargo Transfer Request Value Object
  As a SpaceTraders mining coordinator
  I want to track cargo transfer requests between miners and transports
  So that I can coordinate efficient cargo movement in mining operations

  # ============================================================================
  # Cargo Transfer Request Creation Tests
  # ============================================================================

  Scenario: Create cargo transfer request in pending state
    When I create a cargo transfer request with:
      | id      | mining_operation_id | miner_ship | cargo_items |
      | req-001 | mine-op-1           | MINER-1    | 3           |
    Then the cargo transfer request status should be "PENDING"
    And the cargo transfer request miner ship should be "MINER-1"
    And the cargo transfer request mining operation id should be "mine-op-1"
    And the cargo transfer request transport ship should be empty
    And the cargo transfer request completed_at should be nil
    And the cargo transfer request should have 3 cargo items

  Scenario: Create cargo transfer request with multiple cargo types
    When I create a cargo transfer request with:
      | id      | mining_operation_id | miner_ship | cargo_items           |
      | req-002 | mine-op-1           | MINER-2    | IRON_ORE:50,COPPER:30 |
    Then the cargo transfer request should have 2 cargo items
    And the cargo transfer request total units should be 80

  # ============================================================================
  # Assign Transport Ship Tests (Immutable Transition)
  # ============================================================================

  Scenario: Assign transport ship to pending request
    Given a cargo transfer request in "PENDING" state
    When I assign transport ship "TRANSPORT-1" to the request
    Then a new cargo transfer request should be returned
    And the new request status should be "IN_PROGRESS"
    And the new request transport ship should be "TRANSPORT-1"
    And the original request should remain in "PENDING" state

  Scenario: Assign transport ship preserves other fields
    Given a cargo transfer request with id "req-123" and miner "MINER-5"
    When I assign transport ship "TRANSPORT-2" to the request
    Then the new request id should be "req-123"
    And the new request miner ship should be "MINER-5"
    And the new request cargo should be preserved

  # ============================================================================
  # Complete Transfer Tests (Immutable Transition)
  # ============================================================================

  Scenario: Complete in-progress transfer
    Given a cargo transfer request in "IN_PROGRESS" state
    When I mark the transfer as completed at "2024-01-15T10:30:00Z"
    Then a new cargo transfer request should be returned
    And the new request status should be "COMPLETED"
    And the new request completed_at should be "2024-01-15T10:30:00Z"
    And the original request should remain in "IN_PROGRESS" state

  Scenario: Complete transfer preserves cargo manifest
    Given a cargo transfer request with cargo "IRON_ORE:100"
    And the request has transport ship "TRANSPORT-1"
    When I mark the transfer as completed at "2024-01-15T10:30:00Z"
    Then the new request should have cargo "IRON_ORE:100"
    And the new request transport ship should be "TRANSPORT-1"

  # ============================================================================
  # State Query Tests
  # ============================================================================

  Scenario: IsPending returns true for pending request
    Given a cargo transfer request in "PENDING" state
    When I check if the request is pending
    Then the result should be true

  Scenario: IsPending returns false for in-progress request
    Given a cargo transfer request in "IN_PROGRESS" state
    When I check if the request is pending
    Then the result should be false

  Scenario: IsPending returns false for completed request
    Given a cargo transfer request in "COMPLETED" state
    When I check if the request is pending
    Then the result should be false

  Scenario: IsInProgress returns true for in-progress request
    Given a cargo transfer request in "IN_PROGRESS" state
    When I check if the request is in progress
    Then the result should be true

  Scenario: IsInProgress returns false for pending request
    Given a cargo transfer request in "PENDING" state
    When I check if the request is in progress
    Then the result should be false

  Scenario: IsCompleted returns true for completed request
    Given a cargo transfer request in "COMPLETED" state
    When I check if the request is completed
    Then the result should be true

  Scenario: IsCompleted returns false for pending request
    Given a cargo transfer request in "PENDING" state
    When I check if the request is completed
    Then the result should be false

  # ============================================================================
  # Total Units Calculation Tests
  # ============================================================================

  Scenario: Calculate total units for single cargo type
    Given a cargo transfer request with cargo "IRON_ORE:50"
    When I calculate the total units
    Then the total units should be 50

  Scenario: Calculate total units for multiple cargo types
    Given a cargo transfer request with cargo "IRON_ORE:50,COPPER:30,ALUMINUM:20"
    When I calculate the total units
    Then the total units should be 100

  Scenario: Calculate total units for empty manifest
    Given a cargo transfer request with no cargo items
    When I calculate the total units
    Then the total units should be 0

  # ============================================================================
  # DTO Conversion Tests
  # ============================================================================

  Scenario: ToData converts request to DTO
    Given a cargo transfer request with id "req-456" in "IN_PROGRESS" state
    When I convert the cargo transfer request to data
    Then the request data should have id "req-456"
    And the request data should have status "IN_PROGRESS"
    And the request data should have miner ship "MINER-1"
    And the request data should have transport ship "TRANSPORT-1"

  Scenario: FromData reconstructs request from DTO
    Given a cargo transfer request in "COMPLETED" state
    When I convert the cargo transfer request to data
    And I reconstruct the cargo transfer request from data
    Then the reconstructed request status should be "COMPLETED"
    And the reconstructed request should have same id
    And the reconstructed request should have same cargo

  # ============================================================================
  # Immutability Tests
  # ============================================================================

  Scenario: WithTransportShip does not modify original
    Given a cargo transfer request in "PENDING" state
    When I assign transport ship "TRANSPORT-X" to the request
    Then the original request status should be "PENDING"
    And the original request transport ship should be empty

  Scenario: WithCompleted does not modify original
    Given a cargo transfer request in "IN_PROGRESS" state
    When I mark the transfer as completed at "2024-01-15T12:00:00Z"
    Then the original request status should be "IN_PROGRESS"
    And the original request completed_at should be nil

  Scenario: Cargo manifest is deep copied in WithTransportShip
    Given a cargo transfer request with cargo "IRON_ORE:75"
    When I assign transport ship "TRANSPORT-Y" to the request
    Then modifying the new request cargo should not affect the original
