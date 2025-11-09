Feature: Database Schema Without Ships Table
  As a system architect
  I want the database to not cache ship data
  So that ship state is always fetched fresh from the API

  Scenario: Database does not have ships table
    Given a fresh database instance
    When I inspect the database schema
    Then the ships table should not exist
    And the ship_assignments table should exist
    But the routes table should not exist
