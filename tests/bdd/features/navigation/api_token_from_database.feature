Feature: API operations use player token from database
  As an autonomous bot
  I need API operations to use the player's token from the database
  So that no environment variables are required

  Scenario: Navigate ship using player token from database
    Given CHROMESAMURAI is registered with a valid token in the database
    And the SPACETRADERS_TOKEN environment variable is NOT set
    When I navigate a ship for that player
    Then the navigation should use the token from the database
    And the API call should succeed with the player's token
