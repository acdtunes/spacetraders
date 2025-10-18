Feature: Visualizer Coordinate Handling
  As a tour visualizer
  I want to use correct waypoint coordinates from the database
  So that tour paths are displayed accurately without false crossings

  Background:
    Given the X1-VH85 system with waypoint coordinates from the database

  Scenario: Identify waypoints sharing the same coordinates
    Given waypoints in the X1-VH85 system
    When I analyze coordinate groups
    Then some waypoints should share coordinates (orbitals around same parent)
    And the system should have orbital groups at:
      | coordinate  | waypoints                         |
      | (19, 15)    | A1, A2, A3, A4                   |
      | (2, 87)     | D48, D49, D50, D51               |
      | (34, -42)   | E53, E54                         |
      | (24, 72)    | F55, F56                         |
      | (-36, 24)   | H59, H60, H61, H62               |

  Scenario: Calculate tour leg distances using database coordinates
    Given a cached tour order for X1-VH85
    When I calculate distances for each tour leg
    Then some legs should have zero distance (orbital transitions)
    And the total tour distance should match geometric calculations

  Scenario: Generate correct visualizer data format
    Given a tour order with waypoint coordinates
    When I prepare data for the visualizer
    Then the data should include the tour order
    And the data should include coordinate mapping for all waypoints
    And the data should include the calculated total distance
