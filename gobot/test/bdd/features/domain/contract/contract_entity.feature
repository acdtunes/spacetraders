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
