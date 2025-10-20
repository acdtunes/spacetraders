Feature: Mining operations
  As a mining operator
  I want to track mining statistics and validate operations
  So that I can monitor mining efficiency and profitability

  Background:
    Given a mining statistics tracker

  Scenario: Initialize mining statistics
    Given a new mining operation starts
    When I check initial statistics
    Then cycles completed should be 0
    And total extracted should be 0
    And total sold should be 0
    And total revenue should be 0

  Scenario: Update statistics after extraction
    Given mining statistics show 0 cycles and 0 units extracted
    When I record extraction of 35 units
    Then total extracted should be 35
    And cycles completed should remain 0

  Scenario: Update statistics after completing cycle
    Given mining statistics show 0 cycles completed
    When I record cycle completion with 40 units extracted
    And I record sale of 40 units for 60000 credits
    Then cycles completed should be 1
    And total extracted should be 40
    And total sold should be 40
    And total revenue should be 60000

  Scenario: Accumulate statistics across multiple cycles
    Given mining statistics show 2 cycles with 80 extracted and 120000 revenue
    When I record cycle completion with 40 units extracted
    And I record sale of 40 units for 60000 credits
    Then cycles completed should be 3
    And total extracted should be 120
    And total sold should be 120
    And total revenue should be 180000

  Scenario: Calculate mining efficiency metrics
    Given mining statistics show 5 cycles completed
    And total extracted is 200 units
    And total sold is 200 units
    And total revenue is 300000 credits
    When I calculate average revenue per cycle
    Then average revenue should be 60000 credits
    And average units per cycle should be 40

  Scenario: Track partial cargo extraction
    Given mining statistics show 0 units extracted
    When I record extraction of 10 units
    And I record extraction of 15 units
    And I record extraction of 12 units
    Then total extracted should be 37
    And extraction count should be 3

  Scenario: Validate mining cycle requirements
    Given a mining cycle configuration
    When I validate cycle has asteroid waypoint
    And I validate cycle has market waypoint
    And I validate cycle has ship controller
    And I validate cycle has navigator
    Then cycle configuration should be valid

  Scenario: Calculate fuel cost for mining route
    Given asteroid is 80 units from market
    And ship uses 1 fuel per unit distance in CRUISE mode
    When I calculate round-trip fuel cost
    Then fuel cost should be 160 units
    And fuel cost for 3 cycles should be 480 units

  Scenario: Estimate mining profitability
    Given cargo capacity is 40 units
    And market price is 1500 credits per unit
    And extraction takes 640 seconds to fill cargo
    And round-trip fuel cost is 160 units at 100 credits per unit
    When I estimate profit per cycle
    Then gross revenue should be 60000 credits
    And fuel cost should be 16000 credits
    And net profit should be 44000 credits
    And profit per hour should be approximately 247500 credits

  Scenario: Track consecutive failed extractions
    Given mining statistics show 0 failed extractions
    When I record failed extraction attempt
    And I record failed extraction attempt
    And I record failed extraction attempt
    Then failed extraction count should be 3
    And failure rate should be tracked
