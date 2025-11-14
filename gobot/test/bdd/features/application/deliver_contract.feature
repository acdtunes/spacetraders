Feature: Deliver Contract Command

  As a SpaceTraders player
  I want to deliver cargo for contracts
  So that I can fulfill contract requirements and earn rewards

  Background:
    Given a mediator is initialized
    And a mock API client
    And a contract repository
    And a player repository
    And a ship repository

  Scenario: Deliver cargo for valid delivery
    Given a player with ID 1 and token "test-token-1"
    And an accepted contract "CONTRACT-1" for player 1 with delivery of 100 "IRON_ORE" to "X1-GZ7-A1"
    And a ship "SHIP-1" owned by player 1 at waypoint "X1-GZ7-A1" with 50 "IRON_ORE" in cargo
    And the API will return successful delivery with 50 units delivered
    When I send DeliverContractCommand with contract "CONTRACT-1", ship "SHIP-1", trade "IRON_ORE", 50 units, player 1
    Then the command should succeed
    And 50 units should be delivered
    And the contract should show 50 units fulfilled for "IRON_ORE"

  Scenario: Deliver remaining cargo completes delivery
    Given a player with ID 1 and token "test-token-1"
    And an accepted contract "CONTRACT-1" for player 1 with 50 of 100 "IRON_ORE" already delivered to "X1-GZ7-A1"
    And a ship "SHIP-1" owned by player 1 at waypoint "X1-GZ7-A1" with 50 "IRON_ORE" in cargo
    And the API will return successful delivery with 50 units delivered
    When I send DeliverContractCommand with contract "CONTRACT-1", ship "SHIP-1", trade "IRON_ORE", 50 units, player 1
    Then the command should succeed
    And 50 units should be delivered
    And the contract should show 100 units fulfilled for "IRON_ORE"

  Scenario: Cannot deliver more than required
    Given a player with ID 1 and token "test-token-1"
    And an accepted contract "CONTRACT-1" for player 1 with delivery of 100 "IRON_ORE" to "X1-GZ7-A1"
    And a ship "SHIP-1" owned by player 1 at waypoint "X1-GZ7-A1" with 150 "IRON_ORE" in cargo
    When I send DeliverContractCommand with contract "CONTRACT-1", ship "SHIP-1", trade "IRON_ORE", 150 units, player 1
    Then the command should fail with error "units exceed required"

  Scenario: Cannot deliver invalid trade symbol
    Given a player with ID 1 and token "test-token-1"
    And an accepted contract "CONTRACT-1" for player 1 with delivery of 100 "IRON_ORE" to "X1-GZ7-A1"
    And a ship "SHIP-1" owned by player 1 at waypoint "X1-GZ7-A1" with 50 "COPPER_ORE" in cargo
    When I send DeliverContractCommand with contract "CONTRACT-1", ship "SHIP-1", trade "COPPER_ORE", 50 units, player 1
    Then the command should fail with error "trade symbol not in contract"

  Scenario: Cannot deliver when contract not found
    Given a player with ID 1 and token "test-token-1"
    And a ship "SHIP-1" owned by player 1 at waypoint "X1-GZ7-A1" with 50 "IRON_ORE" in cargo
    When I send DeliverContractCommand with contract "NONEXISTENT", ship "SHIP-1", trade "IRON_ORE", 50 units, player 1
    Then the command should fail with error "contract not found"

  Scenario: Cannot deliver when player not found
    Given a ship "SHIP-1" owned by player 999 at waypoint "X1-GZ7-A1" with 50 "IRON_ORE" in cargo
    When I send DeliverContractCommand with contract "CONTRACT-1", ship "SHIP-1", trade "IRON_ORE", 50 units, player 999
    Then the command should fail with error "player not found"
