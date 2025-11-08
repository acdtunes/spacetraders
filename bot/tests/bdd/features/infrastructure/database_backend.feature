Feature: Database Backend Selection
  As a bot operator
  I want the database to support both SQLite and PostgreSQL
  So that I can use SQLite for tests and PostgreSQL for production

  Background:
    Given no DATABASE_URL environment variable is set

  Scenario: Use SQLite by default
    When I create a database instance with no DATABASE_URL
    Then the database should use SQLite backend
    And the database should create tables successfully

  Scenario: Use PostgreSQL when DATABASE_URL is set
    Given DATABASE_URL is set to "postgresql://spacetraders:dev_password@localhost:5432/spacetraders_test"
    When I create a database instance
    Then the database should use PostgreSQL backend
    And the database should create tables successfully

  Scenario: SQLite uses AUTOINCREMENT for primary keys
    When I create a database instance with no DATABASE_URL
    And I insert a player record
    Then the player_id should auto-increment using SQLite syntax

  Scenario: PostgreSQL uses SERIAL for primary keys
    Given DATABASE_URL is set to "postgresql://spacetraders:dev_password@localhost:5432/spacetraders_test"
    When I create a database instance
    And I insert a player record
    Then the player_id should auto-increment using PostgreSQL syntax

  Scenario: SQLite enables WAL mode for file-based databases
    When I create a database instance with file path "var/test_wal.db"
    Then the database should have WAL mode enabled

  Scenario: PostgreSQL connection uses connection pooling
    Given DATABASE_URL is set to "postgresql://spacetraders:dev_password@localhost:5432/spacetraders_test"
    When I create a database instance
    Then the database should support concurrent connections
