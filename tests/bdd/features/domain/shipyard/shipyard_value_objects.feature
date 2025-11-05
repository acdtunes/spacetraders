Feature: Shipyard Domain Value Objects
  As a developer
  I want immutable shipyard value objects
  So that I can represent shipyard data in the domain layer

  Scenario: Create a valid ShipListing
    Given a ship listing with type "SHIP_MINING_DRONE"
    And the listing has name "Mining Drone"
    And the listing has description "A small automated mining vessel"
    And the listing has purchase price 50000
    When I create the ship listing
    Then the ship listing should have type "SHIP_MINING_DRONE"
    And the ship listing should have name "Mining Drone"
    And the ship listing should have description "A small automated mining vessel"
    And the ship listing should have purchase price 50000

  Scenario: ShipListing is immutable
    Given a created ship listing with type "SHIP_PROBE"
    When I attempt to modify the ship listing type
    Then the modification should be rejected

  Scenario: Create a valid Shipyard
    Given a shipyard at waypoint "X1-GZ7-AB12"
    And the shipyard has ship types ["SHIP_MINING_DRONE", "SHIP_PROBE"]
    And the shipyard has modification fee 1000
    When I create the shipyard
    Then the shipyard should have symbol "X1-GZ7-AB12"
    And the shipyard should have ship types ["SHIP_MINING_DRONE", "SHIP_PROBE"]
    And the shipyard should have modification fee 1000

  Scenario: Shipyard with multiple listings
    Given a shipyard at waypoint "X1-GZ7-AB12"
    And the shipyard has a listing for "SHIP_MINING_DRONE" priced at 50000
    And the shipyard has a listing for "SHIP_PROBE" priced at 25000
    When I create the shipyard with listings
    Then the shipyard should have 2 listings
    And the shipyard should have a listing for "SHIP_MINING_DRONE"
    And the shipyard should have a listing for "SHIP_PROBE"

  Scenario: Shipyard is immutable
    Given a created shipyard at waypoint "X1-GZ7-AB12"
    When I attempt to modify the shipyard symbol
    Then the modification should be rejected

  Scenario: Shipyard with empty listings
    Given a shipyard at waypoint "X1-GZ7-AB12"
    And the shipyard has no listings
    When I create the shipyard with empty listings
    Then the shipyard should have 0 listings
