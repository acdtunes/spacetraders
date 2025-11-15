Feature: Contract Entity

  Scenario: Create a valid contract with single delivery
    Given a contract with ID "contract-1" for player 1
    And the contract has faction "COSMIC"
    And the contract type is "PROCUREMENT"
    And payment is 1000 credits on acceptance and 5000 on fulfillment
    And a delivery of 100 units of "IRON_ORE" to "X1-GZ7-A1" is required
    When I create the contract
    Then the contract should be created successfully
    And the contract ID should be "contract-1"
    And the contract should not be accepted
    And the contract should not be fulfilled

  Scenario: Accept a pending contract
    Given a valid unaccepted contract
    When I accept the contract
    Then the contract should be accepted

  Scenario: Cannot accept an already accepted contract
    Given a valid accepted contract
    When I try to accept the contract
    Then I should get a contract error "contract already accepted"

  Scenario: Deliver cargo for valid delivery
    Given a valid accepted contract with delivery of 100 "IRON_ORE" to "X1-GZ7-A1"
    When I deliver 50 units of "IRON_ORE"
    Then the delivery should show 50 units fulfilled
    And the contract should not be fulfilled

  Scenario: Deliver remaining cargo completes delivery
    Given a contract with 50 of 100 "IRON_ORE" units already delivered
    When I deliver 50 units of "IRON_ORE"
    Then the delivery should show 100 units fulfilled

  Scenario: Cannot deliver more than required
    Given a valid accepted contract with delivery of 100 "IRON_ORE" to "X1-GZ7-A1"
    When I try to deliver 150 units of "IRON_ORE"
    Then I should get a contract error "units exceed required"

  Scenario: Cannot deliver invalid trade symbol
    Given a valid accepted contract with delivery of 100 "IRON_ORE" to "X1-GZ7-A1"
    When I try to deliver 50 units of "COPPER_ORE"
    Then I should get a contract error "trade symbol not in contract"

  Scenario: Check if contract can be fulfilled (all complete)
    Given a contract with all deliveries fulfilled
    When I check if contract can be fulfilled
    Then the contract can be fulfilled

  Scenario: Check if contract cannot be fulfilled (incomplete)
    Given a contract with incomplete deliveries
    When I check if contract can be fulfilled
    Then the contract cannot be fulfilled

  Scenario: Fulfill a complete contract
    Given a contract with all deliveries fulfilled
    When I fulfill the contract
    Then the contract should be fulfilled

  Scenario: Cannot fulfill incomplete contract
    Given a contract with incomplete deliveries
    When I try to fulfill the contract
    Then I should get a contract error "deliveries not complete"

  # ============================================================================
  # Contract Validation Edge Cases
  # ============================================================================

  Scenario: Cannot create contract with empty ID
    When I attempt to create a contract with empty contract_id
    Then contract creation should fail with error "contract_id cannot be empty"

  Scenario: Cannot create contract with zero player ID
    When I attempt to create a contract with player_id 0
    Then contract creation should fail with error "player_id must be positive"

  Scenario: Cannot create contract with negative player ID
    When I attempt to create a contract with player_id -1
    Then contract creation should fail with error "player_id must be positive"

  Scenario: Cannot create contract with empty faction
    When I attempt to create a contract with empty faction_symbol
    Then contract creation should fail with error "faction_symbol cannot be empty"

  Scenario: Cannot create contract with no deliveries
    Given a contract with ID "contract-1" for player 1
    And the contract has faction "COSMIC"
    And the contract type is "PROCUREMENT"
    And payment is 1000 credits on acceptance and 5000 on fulfillment
    When I attempt to create the contract with no deliveries
    Then contract creation should fail with error "must have at least one delivery"

  # ============================================================================
  # Multi-Delivery Contract Edge Cases
  # ============================================================================

  Scenario: Create contract with multiple deliveries
    Given a contract with ID "contract-2" for player 1
    And the contract has faction "COSMIC"
    And the contract type is "PROCUREMENT"
    And payment is 2000 credits on acceptance and 10000 on fulfillment
    And a delivery of 100 units of "IRON_ORE" to "X1-GZ7-A1" is required
    And a delivery of 50 units of "COPPER_ORE" to "X1-GZ7-A1" is required
    When I create the contract
    Then the contract should be created successfully
    And the contract should have 2 deliveries

  Scenario: Partially fulfill multi-delivery contract
    Given a valid accepted contract with multiple deliveries:
      | trade_symbol | units_required | destination |
      | IRON_ORE     | 100            | X1-GZ7-A1   |
      | COPPER_ORE   | 50             | X1-GZ7-A1   |
    When I deliver 100 units of "IRON_ORE"
    And I deliver 25 units of "COPPER_ORE"
    Then the contract should not be fulfilled
    And "IRON_ORE" delivery should be complete
    And "COPPER_ORE" delivery should have 25 of 50 units fulfilled

  Scenario: Fulfill all deliveries in multi-delivery contract
    Given a valid accepted contract with multiple deliveries:
      | trade_symbol | units_required | destination |
      | IRON_ORE     | 100            | X1-GZ7-A1   |
      | COPPER_ORE   | 50             | X1-GZ7-A1   |
    When I deliver 100 units of "IRON_ORE"
    And I deliver 50 units of "COPPER_ORE"
    Then the contract should be fulfillable
    And I can fulfill the contract successfully

  # ============================================================================
  # Delivery Edge Cases
  # ============================================================================

  Scenario: Deliver zero units does nothing
    Given a valid accepted contract with delivery of 100 "IRON_ORE" to "X1-GZ7-A1"
    When I deliver 0 units of "IRON_ORE"
    Then the delivery should show 0 units fulfilled

  Scenario: Deliver negative units raises error
    Given a valid accepted contract with delivery of 100 "IRON_ORE" to "X1-GZ7-A1"
    When I try to deliver -10 units of "IRON_ORE"
    Then I should get a contract error "units must be positive"

  Scenario: Deliver to contract before accepting raises error
    Given a valid unaccepted contract
    When I try to deliver 50 units of "IRON_ORE"
    Then I should get a contract error "contract not accepted"

  Scenario: Deliver exact remaining amount completes delivery
    Given a contract with 99 of 100 "IRON_ORE" units already delivered
    When I deliver 1 unit of "IRON_ORE"
    Then the delivery should show 100 units fulfilled
    And the contract should be fulfillable

  Scenario: Multiple deliveries to same contract accumulate
    Given a valid accepted contract with delivery of 100 "IRON_ORE" to "X1-GZ7-A1"
    When I deliver 25 units of "IRON_ORE"
    And I deliver 25 units of "IRON_ORE"
    And I deliver 25 units of "IRON_ORE"
    And I deliver 25 units of "IRON_ORE"
    Then the delivery should show 100 units fulfilled

  # ============================================================================
  # Contract Expiration Edge Cases
  # ============================================================================

  Scenario: Check if contract is expired
    Given a contract with deadline "2020-01-01T00:00:00Z"
    When I check if contract is expired
    Then the contract should be expired

  Scenario: Check if contract is not expired
    Given a contract with deadline "2030-01-01T00:00:00Z"
    When I check if contract is expired
    Then the contract should not be expired

  Scenario: Check if contract with no deadline is not expired
    Given a contract with no deadline
    When I check if contract is expired
    Then the contract should not be expired

  # ============================================================================
  # Contract State Validation
  # ============================================================================

  Scenario: Cannot accept a fulfilled contract
    Given a fully fulfilled contract
    When I try to accept the contract
    Then I should get a contract error "contract already fulfilled"

  Scenario: Cannot fulfill an unaccepted contract
    Given a valid unaccepted contract with all deliveries complete
    When I try to fulfill the contract
    Then I should get a contract error "contract not accepted"

  Scenario: Fulfilling a contract marks it as fulfilled
    Given a contract with all deliveries fulfilled
    When I fulfill the contract
    Then the contract should be fulfilled
    And the contract should be accepted

  # ============================================================================
  # Contract Payment Information
  # ============================================================================

  Scenario: Contract has correct acceptance payment
    Given a contract with ID "contract-1" for player 1
    And the contract has faction "COSMIC"
    And the contract type is "PROCUREMENT"
    And payment is 1000 credits on acceptance and 5000 on fulfillment
    And a delivery of 100 units of "IRON_ORE" to "X1-GZ7-A1" is required
    When I create the contract
    Then the contract acceptance payment should be 1000 credits
    And the contract fulfillment payment should be 5000 credits

  Scenario: Contract type is correctly set
    Given a contract with ID "contract-1" for player 1
    And the contract has faction "COSMIC"
    And the contract type is "TRANSPORT"
    And payment is 1000 credits on acceptance and 5000 on fulfillment
    And a delivery of 100 units of "IRON_ORE" to "X1-GZ7-A1" is required
    When I create the contract
    Then the contract type should be "TRANSPORT"

  Scenario: Contract faction is correctly set
    Given a contract with ID "contract-1" for player 1
    And the contract has faction "UNITED"
    And the contract type is "PROCUREMENT"
    And payment is 1000 credits on acceptance and 5000 on fulfillment
    And a delivery of 100 units of "IRON_ORE" to "X1-GZ7-A1" is required
    When I create the contract
    Then the contract faction should be "UNITED"
