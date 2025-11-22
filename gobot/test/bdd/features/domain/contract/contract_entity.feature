Feature: Contract Entity
  As a SpaceTraders bot
  I want to manage contract entities with proper validation and state transitions
  So that I can accept, deliver, and fulfill contracts profitably

  # ============================================================================
  # Contract Creation and Validation
  # ============================================================================

  Scenario: Create valid contract with single delivery
    Given a contract with:
      | contract_id | player_id | faction  | type      |
      | CONTRACT-1  | 1         | COMMERCE | PROCUREMENT |
    And contract deliveries:
      | trade_symbol | destination | units_required | units_fulfilled |
      | IRON_ORE     | X1-MARKET   | 100            | 0               |
    And contract payment:
      | on_accepted | on_fulfilled |
      | 10000       | 50000        |
    And contract deadlines:
      | deadline_to_accept         | deadline                   |
      | 2025-12-31T23:59:59Z      | 2026-01-15T23:59:59Z      |
    When I create the contract
    Then the contract should be valid
    And the contract should not be accepted
    And the contract should not be fulfilled

  Scenario: Reject contract with empty ID
    Given a contract with:
      | contract_id | player_id | faction  | type      |
      |             | 1         | COMMERCE | PROCUREMENT |
    And contract deliveries:
      | trade_symbol | destination | units_required | units_fulfilled |
      | IRON_ORE     | X1-MARKET   | 100            | 0               |
    And contract payment:
      | on_accepted | on_fulfilled |
      | 10000       | 50000        |
    And contract deadlines:
      | deadline_to_accept         | deadline                   |
      | 2025-12-31T23:59:59Z      | 2026-01-15T23:59:59Z      |
    When I attempt to create the contract
    Then contract creation should fail with error "contract ID cannot be empty"

  Scenario: Reject contract with invalid player ID
    Given a contract with:
      | contract_id | player_id | faction  | type      |
      | CONTRACT-1  | 0         | COMMERCE | PROCUREMENT |
    And contract deliveries:
      | trade_symbol | destination | units_required | units_fulfilled |
      | IRON_ORE     | X1-MARKET   | 100            | 0               |
    And contract payment:
      | on_accepted | on_fulfilled |
      | 10000       | 50000        |
    And contract deadlines:
      | deadline_to_accept         | deadline                   |
      | 2025-12-31T23:59:59Z      | 2026-01-15T23:59:59Z      |
    When I attempt to create the contract
    Then contract creation should fail with error "invalid player ID"

  Scenario: Reject contract with empty faction symbol
    Given a contract with:
      | contract_id | player_id | faction | type      |
      | CONTRACT-1  | 1         |         | PROCUREMENT |
    And contract deliveries:
      | trade_symbol | destination | units_required | units_fulfilled |
      | IRON_ORE     | X1-MARKET   | 100            | 0               |
    And contract payment:
      | on_accepted | on_fulfilled |
      | 10000       | 50000        |
    And contract deadlines:
      | deadline_to_accept         | deadline                   |
      | 2025-12-31T23:59:59Z      | 2026-01-15T23:59:59Z      |
    When I attempt to create the contract
    Then contract creation should fail with error "faction symbol cannot be empty"

  Scenario: Reject contract with no deliveries
    Given a contract with:
      | contract_id | player_id | faction  | type      |
      | CONTRACT-1  | 1         | COMMERCE | PROCUREMENT |
    And contract payment:
      | on_accepted | on_fulfilled |
      | 10000       | 50000        |
    And contract deadlines:
      | deadline_to_accept         | deadline                   |
      | 2025-12-31T23:59:59Z      | 2026-01-15T23:59:59Z      |
    When I attempt to create the contract with no deliveries
    Then contract creation should fail with error "contract must have at least one delivery"

  Scenario: Create contract with multiple deliveries
    Given a contract with:
      | contract_id | player_id | faction  | type      |
      | CONTRACT-2  | 1         | COMMERCE | PROCUREMENT |
    And contract deliveries:
      | trade_symbol | destination | units_required | units_fulfilled |
      | IRON_ORE     | X1-MARKET   | 100            | 0               |
      | COPPER_ORE   | X1-MARKET   | 50             | 0               |
      | ALUMINUM_ORE | X1-MARKET   | 75             | 0               |
    And contract payment:
      | on_accepted | on_fulfilled |
      | 20000       | 100000       |
    And contract deadlines:
      | deadline_to_accept         | deadline                   |
      | 2025-12-31T23:59:59Z      | 2026-01-15T23:59:59Z      |
    When I create the contract
    Then the contract should be valid
    And the contract should have 3 deliveries

  # ============================================================================
  # Accept Contract
  # ============================================================================

  Scenario: Accept new contract
    Given a valid unaccepted contract
    When I accept the contract
    Then the contract should be accepted
    And the contract should not be fulfilled

  Scenario: Cannot accept already accepted contract
    Given a valid accepted contract
    When I attempt to accept the contract
    Then the contract operation should fail with error "contract already accepted"

  Scenario: Cannot accept already fulfilled contract
    Given a valid fulfilled contract
    When I attempt to accept the contract
    Then the contract operation should fail with error "contract already fulfilled"

  # ============================================================================
  # Deliver Cargo
  # ============================================================================

  Scenario: Deliver cargo for accepted contract
    Given a valid accepted contract with delivery:
      | trade_symbol | destination | units_required | units_fulfilled |
      | IRON_ORE     | X1-MARKET   | 100            | 0               |
    When I deliver 25 units of "IRON_ORE"
    Then the delivery should show 25 units fulfilled

  Scenario: Deliver cargo multiple times
    Given a valid accepted contract with delivery:
      | trade_symbol | destination | units_required | units_fulfilled |
      | IRON_ORE     | X1-MARKET   | 100            | 0               |
    When I deliver 25 units of "IRON_ORE"
    And I deliver 30 units of "IRON_ORE"
    And I deliver 45 units of "IRON_ORE"
    Then the delivery should show 100 units fulfilled

  Scenario: Cannot deliver cargo for unaccepted contract
    Given a valid unaccepted contract with delivery:
      | trade_symbol | destination | units_required | units_fulfilled |
      | IRON_ORE     | X1-MARKET   | 100            | 0               |
    When I attempt to deliver 25 units of "IRON_ORE"
    Then the contract operation should fail with error "contract not accepted"

  Scenario: Cannot deliver unknown trade symbol
    Given a valid accepted contract with delivery:
      | trade_symbol | destination | units_required | units_fulfilled |
      | IRON_ORE     | X1-MARKET   | 100            | 0               |
    When I attempt to deliver 25 units of "GOLD_ORE"
    Then the contract operation should fail with error "trade symbol not in contract"

  Scenario: Cannot deliver more than required units
    Given a valid accepted contract with delivery:
      | trade_symbol | destination | units_required | units_fulfilled |
      | IRON_ORE     | X1-MARKET   | 100            | 0               |
    When I attempt to deliver 101 units of "IRON_ORE"
    Then the contract operation should fail with error "units exceed required"

  Scenario: Cannot deliver exceeding units across multiple deliveries
    Given a valid accepted contract with delivery:
      | trade_symbol | destination | units_required | units_fulfilled |
      | IRON_ORE     | X1-MARKET   | 100            | 0               |
    When I deliver 60 units of "IRON_ORE"
    And I attempt to deliver 50 units of "IRON_ORE"
    Then the contract operation should fail with error "units exceed required"

  Scenario: Deliver cargo for specific trade symbol in multi-delivery contract
    Given a valid accepted contract
    And the contract has deliveries:
      | trade_symbol | destination | units_required | units_fulfilled |
      | IRON_ORE     | X1-MARKET   | 100            | 0               |
      | COPPER_ORE   | X1-MARKET   | 50             | 0               |
    When I deliver 40 units of "COPPER_ORE"
    Then the delivery for "COPPER_ORE" should show 40 units fulfilled
    And the delivery for "IRON_ORE" should show 0 units fulfilled

  # ============================================================================
  # CanFulfill Check
  # ============================================================================

  Scenario: CanFulfill returns false for incomplete single delivery
    Given a valid accepted contract with delivery:
      | trade_symbol | destination | units_required | units_fulfilled |
      | IRON_ORE     | X1-MARKET   | 100            | 75              |
    When I check if contract can be fulfilled
    Then the contract cannot be fulfilled

  Scenario: CanFulfill returns true when all deliveries complete
    Given a valid accepted contract with delivery:
      | trade_symbol | destination | units_required | units_fulfilled |
      | IRON_ORE     | X1-MARKET   | 100            | 100             |
    When I check if contract can be fulfilled
    Then the contract can be fulfilled

  Scenario: CanFulfill checks all deliveries in multi-delivery contract
    Given a valid accepted contract
    And the contract has deliveries:
      | trade_symbol | destination | units_required | units_fulfilled |
      | IRON_ORE     | X1-MARKET   | 100            | 100             |
      | COPPER_ORE   | X1-MARKET   | 50             | 30              |
    When I check if contract can be fulfilled
    Then the contract cannot be fulfilled

  Scenario: CanFulfill returns true when all multi-deliveries complete
    Given a valid accepted contract
    And the contract has deliveries:
      | trade_symbol | destination | units_required | units_fulfilled |
      | IRON_ORE     | X1-MARKET   | 100            | 100             |
      | COPPER_ORE   | X1-MARKET   | 50             | 50              |
      | ALUMINUM_ORE | X1-MARKET   | 75             | 75              |
    When I check if contract can be fulfilled
    Then the contract can be fulfilled

  # ============================================================================
  # Fulfill Contract
  # ============================================================================

  Scenario: Fulfill contract when all deliveries complete
    Given a valid accepted contract with delivery:
      | trade_symbol | destination | units_required | units_fulfilled |
      | IRON_ORE     | X1-MARKET   | 100            | 100             |
    When I fulfill the contract
    Then the contract should be fulfilled

  Scenario: Cannot fulfill unaccepted contract
    Given a valid unaccepted contract with delivery:
      | trade_symbol | destination | units_required | units_fulfilled |
      | IRON_ORE     | X1-MARKET   | 100            | 100             |
    When I attempt to fulfill the contract
    Then the contract operation should fail with error "contract not accepted"

  Scenario: Cannot fulfill contract with incomplete deliveries
    Given a valid accepted contract with delivery:
      | trade_symbol | destination | units_required | units_fulfilled |
      | IRON_ORE     | X1-MARKET   | 100            | 75              |
    When I attempt to fulfill the contract
    Then the contract operation should fail with error "deliveries not complete"

  # ============================================================================
  # IsExpired Check
  # ============================================================================

  Scenario: Contract not expired before deadline
    Given a contract with deadline "2099-12-31T23:59:59Z"
    When I check if contract is expired
    Then the contract should not be expired

  Scenario: Contract expired after deadline
    Given a contract with deadline "2020-01-01T00:00:00Z"
    When I check if contract is expired
    Then the contract should be expired

  Scenario: Contract with invalid deadline format is not expired
    Given a contract with deadline "invalid-date"
    When I check if contract is expired
    Then the contract should not be expired

