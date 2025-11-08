Feature: ConnectionWrapper supports psycopg2 connections
  As a database user
  I need ConnectionWrapper to work with both SQLite and psycopg2
  So that repositories can use the same API regardless of backend

  Scenario: ConnectionWrapper handles psycopg2 execute via cursor
    Given a ConnectionWrapper for PostgreSQL backend
    When I call execute with "SELECT * FROM players WHERE player_id = ?"
    Then the wrapper should create a cursor and convert SQL to PostgreSQL format

  Scenario: ConnectionWrapper handles SQLite execute directly
    Given a ConnectionWrapper for SQLite backend
    When I call execute with "SELECT * FROM players WHERE player_id = ?"
    Then the wrapper should execute directly with no conversion

  Scenario: ConnectionWrapper provides cursor with conversion
    Given a ConnectionWrapper for PostgreSQL backend
    When I get a cursor from the wrapper
    And I execute "SELECT * FROM players WHERE player_id = ?" on the cursor
    Then the cursor should convert SQL to PostgreSQL format
