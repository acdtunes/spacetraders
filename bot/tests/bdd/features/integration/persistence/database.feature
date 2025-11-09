Feature: Database Initialization and Management
  As a persistence layer
  I want to manage SQLite database connections
  So that I can reliably store and retrieve data

  Scenario: Database initialization creates file and schema
    When I initialize a new database
    Then the database file should exist
    And the "players" table should exist
    And the "system_graphs" table should exist
    But the "ships" table should not exist

  Scenario: Database creates parent directories
    When I initialize a database in nested directories
    Then the database file should exist
    And the parent directories should exist

  Scenario: Connection context manager works correctly
    Given an initialized database
    When I open a connection
    Then I should be able to execute queries
    And the query should return results

  Scenario: Connection auto-closes after context exits
    Given an initialized database
    When I open and close a connection
    Then the connection should be closed
    And using the connection should raise an error

  Scenario: Transaction commits on success
    Given an initialized database
    When I insert data in a transaction
    Then the data should be persisted
    And the data should be retrievable

  Scenario: Transaction rolls back on error
    Given an initialized database
    When I insert data and raise an error in a transaction
    Then the data should not be persisted
    And the table should be empty

  Scenario: Transaction connection auto-closes
    Given an initialized database
    When I open and close a transaction
    Then the connection should be closed

  Scenario: WAL mode is enabled
    Given an initialized database
    When I check the journal mode
    Then the journal mode should be "WAL"

  Scenario: Foreign keys are enabled
    Given an initialized database
    When I check foreign keys setting
    Then foreign keys should be enabled

  Scenario: Row factory returns dict-like rows
    Given an initialized database
    When I execute a query with named columns
    Then I should access results by column name

  Scenario: Players table has correct schema
    Given an initialized database
    When I check the players table schema
    Then it should have column "player_id"
    And it should have column "agent_symbol"
    And it should have column "token"
    And it should have column "created_at"
    And it should have column "last_active"
    And it should have column "metadata"

  # Ships table removed - ship data now fetched directly from API
  # Routes table removed - routing is handled in-memory by OR-Tools engine

  Scenario: System graphs table has correct schema
    Given an initialized database
    When I check the system_graphs table schema
    Then it should have column "system_symbol"
    And it should have column "graph_data"
    And it should have column "last_updated"

  Scenario: Player unique constraint prevents duplicates
    Given an initialized database
    When I insert a player with agent_symbol "TEST_AGENT"
    And I attempt to insert another player with agent_symbol "TEST_AGENT"
    Then the second insert should fail with IntegrityError

  # Foreign key cascade test removed - ships table no longer exists

  Scenario: Multiple connections can be opened simultaneously
    Given an initialized database
    When I open two connections simultaneously
    Then both connections should work independently

  Scenario: Indexes are created for performance
    Given an initialized database
    When I check database indexes
    Then index "idx_player_agent" should exist
    But index "idx_ships_player" should not exist
    And index "idx_routes_ship" should not exist

  Scenario: Database uses path from SPACETRADERS_DB_PATH environment variable
    Given the environment variable "SPACETRADERS_DB_PATH" is set to a test path
    When I initialize a database without providing a path
    Then the database should be created at the environment variable path
    And the database file should exist

  Scenario: Database falls back to default path when environment variable not set
    Given the environment variable "SPACETRADERS_DB_PATH" is not set
    When I initialize a database without providing a path
    Then the database should be created at the default path "var/spacetraders.db"
    And the database file should exist

  Scenario: Explicit path parameter overrides environment variable
    Given the environment variable "SPACETRADERS_DB_PATH" is set to a test path
    When I initialize a database with an explicit path
    Then the database should be created at the explicit path
    And not at the environment variable path
