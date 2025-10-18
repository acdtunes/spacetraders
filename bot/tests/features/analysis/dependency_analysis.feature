Feature: Trade Route Dependency Analysis
  As a trader
  I want cargo dependencies to be correctly tracked across multi-leg routes
  So that the circuit breaker smart skip system can make safe decisions

  Background:
    Given the dependency analysis system

  Scenario: Track cargo flow through net-zero segments
    Given a 4-segment trade route:
      | segment | from       | to         | actions                                    | cargo_after                                      |
      | 0       | X1-TEST-A1 | X1-TEST-B2 | BUY 18 SHIP_PARTS, BUY 22 MEDICINE        | SHIP_PARTS:18, MEDICINE:22                      |
      | 1       | X1-TEST-B2 | X1-TEST-C3 | SELL 20 MEDICINE, BUY 20 DRUGS            | SHIP_PARTS:18, MEDICINE:2, DRUGS:20             |
      | 2       | X1-TEST-C3 | X1-TEST-D4 | SELL 6 SHIP_PARTS, BUY 6 SHIP_PARTS       | SHIP_PARTS:18, MEDICINE:2, DRUGS:20             |
      | 3       | X1-TEST-D4 | X1-TEST-E5 | SELL 6 SHIP_PARTS                         | SHIP_PARTS:12, MEDICINE:2, DRUGS:20             |
    When I analyze route dependencies
    Then segment 0 should be marked as INDEPENDENT
    And segment 1 should depend on segment 0 for MEDICINE cargo
    And segment 2 should depend on segment 0 for SHIP_PARTS cargo
    And segment 3 should depend on segment 0 for SHIP_PARTS cargo
    And segment 3 should NOT depend on segment 2
    And segment 3 should require 6 SHIP_PARTS

  Scenario: Abort when source segment fails with net-zero middle segment
    Given a 4-segment trade route:
      | segment | from       | to         | actions                                    | cargo_after                                      |
      | 0       | X1-TEST-A1 | X1-TEST-B2 | BUY 18 SHIP_PARTS, BUY 22 MEDICINE        | SHIP_PARTS:18, MEDICINE:22                      |
      | 1       | X1-TEST-B2 | X1-TEST-C3 | SELL 20 MEDICINE, BUY 20 DRUGS            | SHIP_PARTS:18, MEDICINE:2, DRUGS:20             |
      | 2       | X1-TEST-C3 | X1-TEST-D4 | SELL 6 SHIP_PARTS, BUY 6 SHIP_PARTS       | SHIP_PARTS:18, MEDICINE:2, DRUGS:20             |
      | 3       | X1-TEST-D4 | X1-TEST-E5 | SELL 6 SHIP_PARTS                         | SHIP_PARTS:12, MEDICINE:2, DRUGS:20             |
    When I analyze route dependencies
    And segment 0 fails due to "BUY price spike"
    And I check if segment 0 can be skipped
    Then segment 0 should NOT be skippable
    And the reason should contain "All remaining segments depend on failed segment"

  Scenario: Track cargo flow with partial sells
    Given a 4-segment trade route:
      | segment | from       | to         | actions                     | cargo_after |
      | 0       | X1-TEST-A1 | X1-TEST-B2 | BUY 30 IRON                | IRON:30     |
      | 1       | X1-TEST-B2 | X1-TEST-C3 | SELL 10 IRON               | IRON:20     |
      | 2       | X1-TEST-C3 | X1-TEST-D4 | SELL 10 IRON               | IRON:10     |
      | 3       | X1-TEST-D4 | X1-TEST-E5 | SELL 10 IRON               | IRON:0      |
    When I analyze route dependencies
    Then all segments 1, 2, and 3 should depend on segment 0
    And all segments 1, 2, and 3 should have CARGO dependency type
