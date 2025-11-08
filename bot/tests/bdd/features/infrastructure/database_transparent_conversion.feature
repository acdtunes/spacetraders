Feature: Database Transparent SQL Placeholder Conversion
  As a repository implementation
  I need the database connection to transparently convert SQL placeholders
  So that I can use SQLite-style placeholders regardless of backend

  Background:
    Given a PostgreSQL database backend is configured

  Scenario: Execute query through connection context manager
    Given a database connection
    When I execute "SELECT * FROM players WHERE player_id = ?" with parameters (1,)
    Then the SQL should be automatically converted to PostgreSQL format

  Scenario: Execute query through transaction context manager
    Given a database transaction
    When I execute "INSERT INTO players (agent_symbol, token, created_at) VALUES (?, ?, ?)" with parameters ("TEST", "token123", "2025-01-01T00:00:00")
    Then the SQL should be automatically converted to PostgreSQL format

  Scenario: SQLite backend uses placeholders unchanged
    Given a SQLite database backend is configured
    And a database connection
    When I execute "SELECT * FROM players WHERE player_id = ?" with parameters (1,)
    Then the SQL should use the original placeholders
