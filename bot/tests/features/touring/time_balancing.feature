Feature: Tour Time Balancing Between Scouts
  As a scout coordinator
  I want to balance actual tour TIMES between scouts
  So that no scout takes 14x longer than another

  Background:
    Given multiple scouts for market coverage
    And markets with varying geographic dispersion

  @xfail
  Scenario: Recognize extreme time imbalance with dispersed markets
    Given scout-A assigned 3 compact markets (4 minute tour)
    And scout-B assigned 3 dispersed markets (64 minute tour)
    When I calculate tour times
    Then scout-B should take approximately 14x longer than scout-A
    And imbalance should be detected as extreme

  @xfail
  Scenario: Balance tour times not just market counts
    Given scout-A has 3 compact markets (short tour)
    And scout-B has 3 dispersed markets (long tour)
    When I run balance_tour_times()
    Then markets should be redistributed to equalize tour TIMES
    And variance should be reduced by over 50 percentage points
    And no scout should take more than 3x longer than another

  @xfail
  Scenario: Variance reduction after balancing
    Given intentionally imbalanced partitions with 99% variance
    When I run balance_tour_times() with 30% variance threshold
    Then final variance should be significantly reduced
    And variance reduction should exceed 50 percentage points
    And max variance between scouts should be under 30%

  @xfail
  Scenario: Time ratio constraint after balancing
    Given scouts with extreme time imbalance (14x ratio)
    When I run balance_tour_times()
    Then final time ratio should be under 3.0x
    And both scouts should have reasonable tour times
    And market distribution should prioritize time balance over geographic clustering
