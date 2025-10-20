Feature: Mining Profit Calculation with Realistic Yields
  As a mining operator
  I want profit calculations based on realistic yield probabilities
  So that I can make accurate mining opportunity decisions

  Background:
    Given realistic yield probabilities for deposit types
    And typical market prices for materials

  Scenario Outline: Calculate weighted average price for deposit types
    Given a deposit type of "<deposit_type>"
    When I calculate the weighted average price
    Then the weighted price should be approximately <expected_price> credits per unit
    And the weighted price should be less than <percent>% of the best material price

    Examples:
      | deposit_type              | expected_price | percent |
      | RARE_METAL_DEPOSITS       | 127            | 15      |
      | PRECIOUS_METAL_DEPOSITS   | 46             | 40      |
      | COMMON_METAL_DEPOSITS     | 43             | 100     |
      | MINERAL_DEPOSITS          | 22             | 100     |

  Scenario: Calculate expected cargo value for RARE_METAL_DEPOSITS
    Given a deposit type of "RARE_METAL_DEPOSITS"
    And a cargo capacity of 15 units
    When I calculate the expected cargo value
    Then the cargo value should be approximately 1900 credits
    And the cargo value should be less than 15% of the buggy calculation

  Scenario Outline: Calculate profit per hour for different scenarios
    Given a deposit type of "<deposit_type>"
    And a cargo capacity of 15 units
    And a cycle time of <cycle_minutes> minutes
    When I calculate profit per hour
    Then the profit per hour should be between <min_profit> and <max_profit> credits
    And the buggy calculation should overestimate by more than <overestimate_factor>x

    Examples:
      | deposit_type              | cycle_minutes | min_profit | max_profit | overestimate_factor |
      | RARE_METAL_DEPOSITS       | 384.6         | 250        | 350        | 8.0                 |
      | PRECIOUS_METAL_DEPOSITS   | 15.2          | 2500       | 3500       | 2.5                 |
      | COMMON_METAL_DEPOSITS     | 33.8          | 1000       | 1400       | 1.2                 |

  @xfail
  Scenario: Short COMMON_METAL route ranks higher than long RARE_METAL route
    Given a COMMON_METAL opportunity with 50 unit distance and 14 minute cycle
    And a RARE_METAL opportunity with 800 unit distance and 384.6 minute cycle
    When I calculate profit per hour for both opportunities
    Then the COMMON_METAL opportunity should have profit at least 5x higher than RARE_METAL
    And COMMON_METAL profit should be between 2500 and 3000 credits per hour
    And RARE_METAL profit should be between 250 and 350 credits per hour
