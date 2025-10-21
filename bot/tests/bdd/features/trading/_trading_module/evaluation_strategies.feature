Feature: Trade Evaluation Strategies - Market evaluation for route planning
  As a route planner
  I want to evaluate markets using different strategies
  So that I can find the most profitable trading opportunities

  Background:
    Given a test database with market data
    And the following markets exist:
      | waypoint  | x   | y   |
      | X1-TEST-A | 0   | 0   |
      | X1-TEST-B | 100 | 0   |
      | X1-TEST-C | 200 | 0   |
    And the following trade opportunities:
      | buy_waypoint | sell_waypoint | good       | buy_price | sell_price | spread | volume |
      | X1-TEST-A    | X1-TEST-B     | IRON_ORE   | 100       | 200        | 100    | 50     |
      | X1-TEST-B    | X1-TEST-C     | COPPER_ORE | 150       | 300        | 150    | 40     |

  Scenario: ProfitFirstStrategy - Basic sell and buy evaluation
    Given a ProfitFirstStrategy
    And ship has 30 units of "IRON_ORE" in cargo
    And ship has 10,000 credits available
    And ship has cargo capacity 50
    And fuel cost is 100 credits
    When I evaluate market "X1-TEST-B"
    Then evaluation should include SELL action for "IRON_ORE"
    And evaluation should include BUY action for "COPPER_ORE"
    And net profit should be greater than 0
    And credits after should reflect both sell revenue and buy cost
    And cargo after should show "COPPER_ORE" only

  Scenario: Sell-only segment - No profitable purchases available
    Given a ProfitFirstStrategy
    And ship has 40 units of "IRON_ORE" in cargo
    And ship has 5,000 credits available
    And ship has cargo capacity 50
    And market "X1-TEST-B" only buys "IRON_ORE" (no sells)
    And fuel cost is 100 credits
    When I evaluate market "X1-TEST-B"
    Then evaluation should include SELL action for "IRON_ORE"
    And evaluation should have no BUY actions
    And net profit should equal sell revenue minus fuel cost
    And cargo after should be empty
    And credits after should show increased credits

  Scenario: Buy-only segment - No cargo to sell
    Given a ProfitFirstStrategy
    And ship has empty cargo
    And ship has 15,000 credits available
    And ship has cargo capacity 50
    And market "X1-TEST-A" only sells "IRON_ORE" (no buys)
    And fuel cost is 100 credits
    When I evaluate market "X1-TEST-A"
    Then evaluation should have no SELL actions
    And evaluation should include BUY action for "IRON_ORE"
    And net profit should be potential future revenue minus purchase cost and fuel
    And cargo after should contain "IRON_ORE"
    And credits after should show decreased credits

  Scenario: Insufficient credits - Cannot afford desired purchase
    Given a ProfitFirstStrategy
    And ship has empty cargo
    And ship has 500 credits available
    And ship has cargo capacity 50
    And market sells "IRON_ORE" at 100 credits per unit
    And fuel cost is 100 credits
    When I evaluate market "X1-TEST-A"
    Then BUY action should be limited by available credits
    And units purchased should be 4 or less
    And net profit should account for limited purchase

  Scenario: Insufficient cargo space - Cannot fit all desired goods
    Given a ProfitFirstStrategy
    And ship has 40 units of "ALUMINUM_ORE" in cargo
    And ship has 20,000 credits available
    And ship has cargo capacity 50
    And market sells "IRON_ORE" at 100 credits per unit with volume 100
    And fuel cost is 100 credits
    When I evaluate market "X1-TEST-A"
    Then BUY action should be limited to 10 units
    And cargo after should total 50 units maximum
    And evaluation should respect cargo capacity constraint

  Scenario: Trade volume limits - Market has limited supply
    Given a ProfitFirstStrategy
    And ship has empty cargo
    And ship has 50,000 credits available
    And ship has cargo capacity 100
    And market sells "IRON_ORE" with trade_volume 20
    And fuel cost is 100 credits
    When I evaluate market "X1-TEST-A"
    Then BUY action should be limited to 20 units
    And evaluation should respect market trade_volume
    And units purchased should not exceed trade_volume

  Scenario: Future revenue estimation - Selling purchased goods
    Given a ProfitFirstStrategy
    And ship has empty cargo
    And ship has 10,000 credits available
    And ship has cargo capacity 50
    And I buy "IRON_ORE" at "X1-TEST-A" for 100 credits per unit
    And future opportunities show "IRON_ORE" sells for 200 at "X1-TEST-B"
    And fuel cost is 100 credits
    When I evaluate market "X1-TEST-A"
    Then potential future revenue should be calculated
    And net profit should include estimated future sales
    And evaluation should show positive profitability

  Scenario: Negative net profit - Fuel cost exceeds opportunity
    Given a ProfitFirstStrategy
    And ship has 10 units of "IRON_ORE" in cargo
    And ship has 1,000 credits available
    And ship has cargo capacity 50
    And market buys "IRON_ORE" for 110 credits per unit
    And purchase price was 100 credits per unit
    And fuel cost is 5,000 credits
    When I evaluate market "X1-TEST-B"
    Then net profit should be negative
    And evaluation should show unprofitable due to fuel cost

  Scenario: Mixed cargo evaluation - Sell some, keep some, buy new
    Given a ProfitFirstStrategy
    And ship has 20 units of "IRON_ORE" in cargo
    And ship has 10 units of "COPPER_ORE" in cargo
    And ship has 10,000 credits available
    And ship has cargo capacity 50
    And market buys "IRON_ORE" for 200 credits
    And market sells "ALUMINUM_ORE" for 150 credits
    And fuel cost is 100 credits
    When I evaluate market "X1-TEST-B"
    Then SELL actions should only include "IRON_ORE"
    And "COPPER_ORE" should remain in cargo after
    And BUY action should include "ALUMINUM_ORE"
    And cargo after should contain both "COPPER_ORE" and "ALUMINUM_ORE"

  Scenario: Empty market - No trading opportunities
    Given a ProfitFirstStrategy
    And ship has 30 units of "IRON_ORE" in cargo
    And ship has 5,000 credits available
    And ship has cargo capacity 50
    And market has no trade opportunities
    And fuel cost is 100 credits
    When I evaluate market "X1-TEST-B"
    Then evaluation should have no actions
    And net profit should equal negative fuel cost
    And cargo after should equal cargo before
    And evaluation should indicate unprofitable market

  Scenario: Credits exactly equal to purchase cost
    Given a ProfitFirstStrategy
    And ship has empty cargo
    And ship has 5,000 credits available
    And ship has cargo capacity 50
    And market sells "IRON_ORE" for 100 credits with volume 50
    And fuel cost is 100 credits
    When I evaluate market "X1-TEST-A"
    Then BUY action should purchase 49 units maximum
    And credits after should be approximately 100 (fuel cost reserve)
    And evaluation should leave safety margin for fuel

  Scenario: Zero fuel cost - Free navigation
    Given a ProfitFirstStrategy
    And ship has 20 units of "IRON_ORE" in cargo
    And ship has 5,000 credits available
    And ship has cargo capacity 50
    And market buys "IRON_ORE" for 200 credits
    And fuel cost is 0 credits
    When I evaluate market "X1-TEST-B"
    Then net profit should equal sell revenue only
    And evaluation should not deduct fuel costs
    And profitability should be higher than with fuel cost
