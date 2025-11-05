"""
Integration tests for Player CLI commands.

These tests verify the full integration stack:
- CLI command functions
- Mediator dispatch
- Command/Query handlers
- Repository operations
- Database persistence

We test OBSERVABLE BEHAVIOR:
- Exit codes (0 = success, 1 = error)
- Console output (user-facing messages)
- Database state changes

We do NOT test:
- Internal method calls
- Mediator invocations
- Implementation details
"""
import pytest
import argparse
import json
from datetime import datetime, UTC

from spacetraders.adapters.primary.cli.player_cli import (
    register_player_command,
    list_players_command,
    player_info_command,
    setup_player_commands
)
from spacetraders.domain.shared.player import Player


class TestRegisterPlayerCommand:
    """Integration tests for player register command"""

    def test_register_player_successfully(self, capsys, player_repo):
        """
        GIVEN no existing players
        WHEN user registers a new player via CLI
        THEN player is created in database and success message is shown
        """
        # Execute real CLI command with real container
        args = argparse.Namespace(
            agent_symbol="TEST_AGENT",
            token="test-token-123",
            metadata=None
        )

        result = register_player_command(args)

        # Verify observable behavior: exit code
        assert result == 0

        # Verify observable behavior: console output
        output = capsys.readouterr().out
        assert "✅ Registered player" in output
        assert "TEST_AGENT" in output

        # Verify database state: player was actually saved
        player = player_repo.find_by_agent_symbol("TEST_AGENT")
        assert player is not None
        assert player.agent_symbol == "TEST_AGENT"
        assert player.token == "test-token-123"
        assert player.metadata == {}

    def test_register_player_with_metadata(self, capsys, player_repo):
        """
        GIVEN metadata JSON provided
        WHEN user registers a player with metadata
        THEN metadata is stored in database
        """
        # Setup
        metadata = {"faction": "COSMIC", "headquarters": "X1-GZ7-A1"}
        args = argparse.Namespace(
            agent_symbol="COSMIC_TRADER",
            token="cosmic-token-456",
            metadata=json.dumps(metadata)
        )

        # Execute
        result = register_player_command(args)

        # Verify exit code
        assert result == 0

        # Verify output
        output = capsys.readouterr().out
        assert "✅ Registered player" in output
        assert "COSMIC_TRADER" in output

        # Verify database state: metadata was stored
        player = player_repo.find_by_agent_symbol("COSMIC_TRADER")
        assert player is not None
        assert player.metadata == metadata
        assert player.metadata["faction"] == "COSMIC"
        assert player.metadata["headquarters"] == "X1-GZ7-A1"

    def test_register_duplicate_player(self, capsys, player_repo):
        """
        GIVEN a player already exists
        WHEN user tries to register same agent symbol
        THEN error is shown and database is unchanged
        """
        # Setup: Create existing player in database
        existing_player = Player(
            player_id=None,
            agent_symbol="EXISTING_AGENT",
            token="original-token",
            created_at=datetime.now(UTC),
            last_active=datetime.now(UTC)
        )
        saved_player = player_repo.create(existing_player)

        # Try to register duplicate
        args = argparse.Namespace(
            agent_symbol="EXISTING_AGENT",
            token="new-token-should-fail",
            metadata=None
        )

        result = register_player_command(args)

        # Verify error handling
        assert result == 1

        # Verify error message shown to user
        output = capsys.readouterr().out
        assert "❌ Error:" in output
        assert "EXISTING_AGENT" in output

        # Verify database unchanged: token is still original
        player = player_repo.find_by_agent_symbol("EXISTING_AGENT")
        assert player.token == "original-token"  # Not changed to new token


class TestListPlayersCommand:
    """Integration tests for player list command"""

    def test_list_players_with_one_player(self, capsys, player_repo):
        """
        GIVEN one player exists in database
        WHEN user lists players
        THEN player is shown in output
        """
        # Setup: Create real player in database
        player = Player(
            player_id=None,
            agent_symbol="SOLO_AGENT",
            token="solo-token",
            created_at=datetime.now(UTC),
            last_active=datetime.now(UTC)
        )
        saved_player = player_repo.create(player)

        # Execute
        args = argparse.Namespace()
        result = list_players_command(args)

        # Verify exit code
        assert result == 0

        # Verify output shows the player
        output = capsys.readouterr().out
        assert "Registered players (1):" in output
        assert f"[{saved_player.player_id}] SOLO_AGENT" in output

    def test_list_players_empty(self, capsys, player_repo):
        """
        GIVEN no players in database
        WHEN user lists players
        THEN empty message is shown
        """
        # No setup needed - database is empty by default

        # Execute
        args = argparse.Namespace()
        result = list_players_command(args)

        # Verify exit code
        assert result == 0

        # Verify empty state message
        output = capsys.readouterr().out
        assert "No players registered" in output

    def test_list_multiple_players(self, capsys, player_repo):
        """
        GIVEN multiple players exist
        WHEN user lists players
        THEN all players are shown with correct player_ids
        """
        # Setup: Create multiple players in database
        player1 = Player(
            player_id=None,
            agent_symbol="ALPHA_TRADER",
            token="token1",
            created_at=datetime.now(UTC),
            last_active=datetime.now(UTC)
        )
        player2 = Player(
            player_id=None,
            agent_symbol="BETA_MINER",
            token="token2",
            created_at=datetime.now(UTC),
            last_active=datetime.now(UTC)
        )

        saved1 = player_repo.create(player1)
        saved2 = player_repo.create(player2)

        # Execute
        args = argparse.Namespace()
        result = list_players_command(args)

        # Verify exit code
        assert result == 0

        # Verify output shows both players
        output = capsys.readouterr().out
        assert "Registered players (2):" in output
        assert f"[{saved1.player_id}] ALPHA_TRADER" in output
        assert f"[{saved2.player_id}] BETA_MINER" in output


class TestPlayerInfoCommand:
    """Integration tests for player info command"""

    def test_get_player_info_by_id(self, capsys, player_repo):
        """
        GIVEN a player exists in database
        WHEN user queries by player_id
        THEN player details are displayed
        """
        # Setup: Create real player
        player = Player(
            player_id=None,
            agent_symbol="INFO_TEST_AGENT",
            token="info-token",
            created_at=datetime.now(UTC),
            last_active=datetime.now(UTC)
        )
        saved_player = player_repo.create(player)

        # Execute query by player_id
        args = argparse.Namespace(
            player_id=saved_player.player_id,
            agent_symbol=None
        )

        result = player_info_command(args)

        # Verify exit code
        assert result == 0

        # Verify output shows player details
        output = capsys.readouterr().out
        assert f"Player {saved_player.player_id}:" in output
        assert "Agent: INFO_TEST_AGENT" in output
        assert "Created:" in output
        assert "Last Active:" in output

    def test_get_player_info_by_agent_symbol(self, capsys, player_repo):
        """
        GIVEN a player exists in database
        WHEN user queries by agent_symbol
        THEN player details are displayed
        """
        # Setup: Create real player
        player = Player(
            player_id=None,
            agent_symbol="QUERY_BY_NAME",
            token="name-query-token",
            created_at=datetime.now(UTC),
            last_active=datetime.now(UTC)
        )
        saved_player = player_repo.create(player)

        # Execute query by agent_symbol
        args = argparse.Namespace(
            player_id=None,
            agent_symbol="QUERY_BY_NAME"
        )

        result = player_info_command(args)

        # Verify exit code
        assert result == 0

        # Verify output shows player details
        output = capsys.readouterr().out
        assert f"Player {saved_player.player_id}:" in output
        assert "Agent: QUERY_BY_NAME" in output

    def test_get_player_info_not_found(self, capsys, player_repo):
        """
        GIVEN no player with given ID exists
        WHEN user queries for non-existent player
        THEN error message is shown
        """
        # No setup - query non-existent player

        args = argparse.Namespace(
            player_id=999,
            agent_symbol=None
        )

        result = player_info_command(args)

        # Verify error exit code
        assert result == 1

        # Verify error message shown
        output = capsys.readouterr().out
        assert "❌ Error:" in output

    def test_get_player_info_with_metadata(self, capsys, player_repo):
        """
        GIVEN a player with metadata exists
        WHEN user queries player info
        THEN metadata is displayed in output
        """
        # Setup: Create player with metadata
        metadata = {"faction": "COSMIC", "headquarters": "X1-GZ7-A1"}
        player = Player(
            player_id=None,
            agent_symbol="METADATA_AGENT",
            token="meta-token",
            created_at=datetime.now(UTC),
            last_active=datetime.now(UTC),
            metadata=metadata
        )
        saved_player = player_repo.create(player)

        # Execute
        args = argparse.Namespace(
            player_id=saved_player.player_id,
            agent_symbol=None
        )

        result = player_info_command(args)

        # Verify exit code
        assert result == 0

        # Verify metadata shown in output
        output = capsys.readouterr().out
        assert "Metadata:" in output
        assert "faction" in output
        assert "COSMIC" in output
        assert "headquarters" in output


class TestSetupPlayerCommands:
    """Tests for setup_player_commands"""

    def test_setup_creates_player_parser(self):
        """Test that setup creates player parser with subcommands"""
        # Arrange
        parser = argparse.ArgumentParser()
        subparsers = parser.add_subparsers(dest="command")

        # Act
        setup_player_commands(subparsers)

        # Assert
        # Parse a valid register command
        args = parser.parse_args(['player', 'register', '--agent', 'TEST', '--token', 'abc'])
        assert hasattr(args, 'func')
        assert args.agent_symbol == 'TEST'
        assert args.token == 'abc'

    def test_setup_creates_list_subcommand(self):
        """Test that setup creates list subcommand"""
        # Arrange
        parser = argparse.ArgumentParser()
        subparsers = parser.add_subparsers(dest="command")

        # Act
        setup_player_commands(subparsers)

        # Assert
        args = parser.parse_args(['player', 'list'])
        assert hasattr(args, 'func')

    def test_setup_creates_info_subcommand(self):
        """Test that setup creates info subcommand with player-id arg"""
        # Arrange
        parser = argparse.ArgumentParser()
        subparsers = parser.add_subparsers(dest="command")

        # Act
        setup_player_commands(subparsers)

        # Assert
        args = parser.parse_args(['player', 'info', '--player-id', '1'])
        assert hasattr(args, 'func')
        assert args.player_id == 1

    def test_setup_info_accepts_agent_arg(self):
        """Test that info subcommand accepts agent argument"""
        # Arrange
        parser = argparse.ArgumentParser()
        subparsers = parser.add_subparsers(dest="command")

        # Act
        setup_player_commands(subparsers)

        # Assert
        args = parser.parse_args(['player', 'info', '--agent', 'TEST'])
        assert hasattr(args, 'func')
        assert args.agent_symbol == 'TEST'
