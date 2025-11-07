Feature: API Client Jettison Cargo Interface
  As a cargo management system
  I need the API client to have a jettison_cargo method
  So that ships can remove unwanted cargo via the SpaceTraders API

  Scenario: API interface declares jettison_cargo method
    Given the ISpaceTradersAPI interface exists
    Then the interface should have a jettison_cargo method
    And the method should accept ship_symbol as a parameter
    And the method should accept cargo_symbol as a parameter
    And the method should accept units as a parameter

  Scenario: API client implements jettison_cargo method
    Given the SpaceTradersAPIClient exists
    Then the client should implement jettison_cargo method
    And the method should call POST endpoint for jettison
