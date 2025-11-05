Feature: Shipyard Domain Exceptions
  As a domain layer
  The shipyard-related exceptions should properly communicate business rule violations
  Through clear error messages and inheritance from DomainException

  # ============================================================================
  # InsufficientCreditsError Tests
  # ============================================================================

  Scenario: InsufficientCreditsError is a DomainException subclass
    When I check if InsufficientCreditsError inherits from DomainException
    Then it should be True

  Scenario: InsufficientCreditsError can be raised with a message
    When I raise InsufficientCreditsError with message "Not enough credits to purchase ship"
    Then the exception message should be "Not enough credits to purchase ship"

  Scenario: InsufficientCreditsError can be caught as DomainException
    When I raise InsufficientCreditsError with message "Insufficient credits"
    Then it should be catchable as DomainException

  # ============================================================================
  # ShipNotAvailableError Tests
  # ============================================================================

  Scenario: ShipNotAvailableError is a DomainException subclass
    When I check if ShipNotAvailableError inherits from DomainException
    Then it should be True

  Scenario: ShipNotAvailableError can be raised with a message
    When I raise ShipNotAvailableError with message "Ship type MINING_DRONE not available at shipyard"
    Then the exception message should be "Ship type MINING_DRONE not available at shipyard"

  Scenario: ShipNotAvailableError can be caught as DomainException
    When I raise ShipNotAvailableError with message "Ship unavailable"
    Then it should be catchable as DomainException

  # ============================================================================
  # ShipyardNotFoundError Tests
  # ============================================================================

  Scenario: ShipyardNotFoundError is a DomainException subclass
    When I check if ShipyardNotFoundError inherits from DomainException
    Then it should be True

  Scenario: ShipyardNotFoundError can be raised with a message
    When I raise ShipyardNotFoundError with message "Shipyard X1-A1-B2 not found"
    Then the exception message should be "Shipyard X1-A1-B2 not found"

  Scenario: ShipyardNotFoundError can be caught as DomainException
    When I raise ShipyardNotFoundError with message "Shipyard not found"
    Then it should be catchable as DomainException
