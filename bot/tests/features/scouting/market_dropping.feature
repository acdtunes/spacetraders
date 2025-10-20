Feature: Scout Market Dropping Prevention
  As a scout coordinator
  I want to ensure markets are not dropped during tour optimization
  So that all markets continue to receive price updates

  Background:
    Given a scout coordinator managing market tours
    And multiple markets requiring price monitoring

  @xfail
  Scenario: Prevent markets from being dropped during optimization
    Given all markets are initially included in scout tours
    And tour optimization is performed
    When checking market coverage after optimization
    Then no markets should be dropped
    And all markets should appear in at least one scout tour

  @xfail
  Scenario: Detect dropped markets
    Given initial market list with 10 markets
    And optimized tours are generated
    When validating tour coverage
    Then system should detect any dropped markets
    And dropped markets should be reported
    And tour should be rejected if markets are missing

  @xfail
  Scenario: Markets list consistency fix
    Given scout coordinator receives market list
    And tour optimization processes the list
    When comparing input markets to tour markets
    Then output tours should include all input markets
    And no markets should be silently dropped
