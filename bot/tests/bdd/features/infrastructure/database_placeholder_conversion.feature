Feature: Database SQL Placeholder Conversion
  As a database abstraction layer
  I need to automatically convert SQL placeholders for different backends
  So that repositories can use SQLite-style placeholders transparently

  Background:
    Given a PostgreSQL database backend

  Scenario: Convert single placeholder in SELECT query
    When I convert the SQL "SELECT * FROM players WHERE player_id = ?"
    Then the converted SQL should be "SELECT * FROM players WHERE player_id = $1"

  Scenario: Convert multiple placeholders in INSERT query
    When I convert the SQL "INSERT INTO players (agent_symbol, token, created_at) VALUES (?, ?, ?)"
    Then the converted SQL should be "INSERT INTO players (agent_symbol, token, created_at) VALUES ($1, $2, $3)"

  Scenario: Convert placeholders in UPDATE query
    When I convert the SQL "UPDATE players SET last_active = ?, metadata = ?, credits = ? WHERE player_id = ?"
    Then the converted SQL should be "UPDATE players SET last_active = $1, metadata = $2, credits = $3 WHERE player_id = $4"

  Scenario: Handle SQL with no placeholders
    When I convert the SQL "SELECT * FROM players ORDER BY created_at"
    Then the converted SQL should be "SELECT * FROM players ORDER BY created_at"

  Scenario: SQLite backend does not convert placeholders
    Given a SQLite database backend
    When I convert the SQL "SELECT * FROM players WHERE player_id = ?"
    Then the converted SQL should be "SELECT * FROM players WHERE player_id = ?"

  Scenario: Convert placeholders in complex query with multiple clauses
    When I convert the SQL "SELECT * FROM market_data WHERE player_id = ? AND good_symbol = ? AND waypoint_symbol LIKE ? ORDER BY sell_price ASC LIMIT ?"
    Then the converted SQL should be "SELECT * FROM market_data WHERE player_id = $1 AND good_symbol = $2 AND waypoint_symbol LIKE $3 ORDER BY sell_price ASC LIMIT $4"
