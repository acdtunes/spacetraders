"""
Integration tests for Sync CLI commands.

These tests verify the full integration stack:
- CLI command functions
- Mediator dispatch
- SyncShips handler
- API client integration (mocked - external dependency)
- Ship repository operations
- Database persistence

We test OBSERVABLE BEHAVIOR:
- Exit codes (0 = success, 1 = error)
- Console output (user-facing messages)
- Database state changes (ships synced to DB)

We do NOT test:
- Internal method calls
- Mediator invocations
- Implementation details
"""
import pytest
import argparse
from unittest.mock import patch, AsyncMock, Mock
from datetime import datetime, UTC

from spacetraders.adapters.primary.cli.sync_cli import (
    sync_ships_command,
    setup_sync_commands
)
from spacetraders.adapters.primary.cli.player_selector import PlayerSelectionError
from spacetraders.domain.shared.player import Player
from spacetraders.domain.shared.ship import Ship
from spacetraders.domain.shared.value_objects import Waypoint, Fuel


class TestSyncShipsCommand:
    """Integration tests for sync ships command"""

    @patch('spacetraders.configuration.container.get_api_client_for_player')
    def test_sync_ships_successfully(self, mock_get_api_client, capsys, player_repo, ship_repo):
        """
        GIVEN a player exists with API token
        WHEN user syncs ships from API
        THEN ships are saved to database and shown in output
        """
        # Setup: Create real player in database
        player = Player(
            player_id=None,
            agent_symbol="SYNC_TEST",
            token="test-api-token",
            created_at=datetime.now(UTC),
            last_active=datetime.now(UTC)
        )
        saved_player = player_repo.create(player)

        # Mock ONLY external API client (external dependency)
        mock_api = Mock()
        mock_api.get_agent.return_value = {
            "data": {
                "symbol": "SYNC_TEST",
                "accountId": "test-account",
                "headquarters": "X1-TEST-A1"
            }
        }
        mock_api.get_ships.return_value = {
            "data": [
                {
                    "symbol": "SYNC_TEST-1",
                    "nav": {
                        "waypointSymbol": "X1-TEST-A1",
                        "status": "DOCKED",
                        "flightMode": "CRUISE",
                        "route": {
                            "origin": {"symbol": "X1-TEST-A1", "x": 0, "y": 0, "type": "PLANET"},
                            "destination": {"symbol": "X1-TEST-A1", "x": 0, "y": 0, "type": "PLANET"}
                        }
                    },
                    "fuel": {"current": 100, "capacity": 100},
                    "cargo": {"units": 0, "capacity": 50},
                    "frame": {"symbol": "FRAME_FRIGATE"},
                    "reactor": {"symbol": "REACTOR_SOLAR_I"},
                    "engine": {"symbol": "ENGINE_IMPULSE_I", "speed": 10}
                }
            ]
        }
        mock_api.get_waypoint.return_value = {
            "data": {
                "symbol": "X1-TEST-A1",
                "x": 0,
                "y": 0,
                "type": "PLANET",
                "traits": [{"symbol": "MARKETPLACE"}]
            }
        }
        mock_get_api_client.return_value = mock_api

        # Execute real CLI command
        args = argparse.Namespace(player_id=saved_player.player_id, agent=None)
        result = sync_ships_command(args)

        # Verify observable behavior: exit code
        assert result == 0

        # Verify observable behavior: console output
        output = capsys.readouterr().out
        assert "✅ Successfully synced 1 ships" in output
        assert "SYNC_TEST-1" in output
        assert "X1-TEST-A1" in output
        assert "100%" in output  # Fuel percentage

        # Verify database state: ship was actually saved
        ships = ship_repo.find_all_by_player(saved_player.player_id)
        assert len(ships) == 1
        assert ships[0].ship_symbol == "SYNC_TEST-1"
        assert ships[0].current_location.symbol == "X1-TEST-A1"
        assert ships[0].fuel.current == 100
        assert ships[0].nav_status == Ship.DOCKED

    @patch('spacetraders.configuration.container.get_api_client_for_player')
    def test_sync_multiple_ships(self, mock_get_api_client, capsys, player_repo, ship_repo):
        """
        GIVEN a player has multiple ships
        WHEN user syncs ships from API
        THEN all ships are saved to database and displayed
        """
        # Setup: Create real player
        player = Player(
            player_id=None,
            agent_symbol="MULTI_SHIP",
            token="multi-token",
            created_at=datetime.now(UTC),
            last_active=datetime.now(UTC)
        )
        saved_player = player_repo.create(player)

        # Mock API with multiple ships
        mock_api = Mock()
        mock_api.get_agent.return_value = {
            "data": {"symbol": "MULTI_SHIP", "accountId": "test"}
        }
        mock_api.get_ships.return_value = {
            "data": [
                {
                    "symbol": "MULTI_SHIP-1",
                    "nav": {
                        "waypointSymbol": "X1-A1",
                        "status": "DOCKED",
                        "flightMode": "CRUISE",
                        "route": {
                            "origin": {"symbol": "X1-A1", "x": 0, "y": 0, "type": "PLANET"},
                            "destination": {"symbol": "X1-A1", "x": 0, "y": 0, "type": "PLANET"}
                        }
                    },
                    "fuel": {"current": 50, "capacity": 100},
                    "cargo": {"units": 10, "capacity": 50},
                    "frame": {"symbol": "FRAME_FRIGATE"},
                    "reactor": {"symbol": "REACTOR_SOLAR_I"},
                    "engine": {"symbol": "ENGINE_IMPULSE_I", "speed": 10}
                },
                {
                    "symbol": "MULTI_SHIP-2",
                    "nav": {
                        "waypointSymbol": "X1-B1",
                        "status": "IN_ORBIT",
                        "flightMode": "CRUISE",
                        "route": {
                            "origin": {"symbol": "X1-B1", "x": 10, "y": 10, "type": "ASTEROID"},
                            "destination": {"symbol": "X1-B1", "x": 10, "y": 10, "type": "ASTEROID"}
                        }
                    },
                    "fuel": {"current": 100, "capacity": 100},
                    "cargo": {"units": 0, "capacity": 30},
                    "frame": {"symbol": "FRAME_PROBE"},
                    "reactor": {"symbol": "REACTOR_SOLAR_I"},
                    "engine": {"symbol": "ENGINE_IMPULSE_I", "speed": 10}
                }
            ]
        }
        mock_api.get_waypoint.return_value = {
            "data": {"symbol": "X1-A1", "x": 0, "y": 0, "type": "PLANET", "traits": []}
        }
        mock_get_api_client.return_value = mock_api

        # Execute
        args = argparse.Namespace(player_id=saved_player.player_id, agent=None)
        result = sync_ships_command(args)

        # Verify exit code
        assert result == 0

        # Verify output shows both ships
        output = capsys.readouterr().out
        assert "✅ Successfully synced 2 ships" in output
        assert "MULTI_SHIP-1" in output
        assert "MULTI_SHIP-2" in output
        assert "50%" in output  # Ship 1 fuel
        assert "100%" in output  # Ship 2 fuel

        # Verify database state: both ships saved
        ships = ship_repo.find_all_by_player(saved_player.player_id)
        assert len(ships) == 2
        ship_symbols = {s.ship_symbol for s in ships}
        assert "MULTI_SHIP-1" in ship_symbols
        assert "MULTI_SHIP-2" in ship_symbols

    @patch('spacetraders.configuration.container.get_api_client_for_player')
    def test_sync_ships_empty(self, mock_get_api_client, capsys, player_repo, ship_repo):
        """
        GIVEN a player has no ships
        WHEN user syncs ships
        THEN success is reported with 0 ships
        """
        # Setup: Create player
        player = Player(
            player_id=None,
            agent_symbol="NO_SHIPS",
            token="no-ships-token",
            created_at=datetime.now(UTC),
            last_active=datetime.now(UTC)
        )
        saved_player = player_repo.create(player)

        # Mock API with empty ships list
        mock_api = Mock()
        mock_api.get_agent.return_value = {
            "data": {"symbol": "NO_SHIPS", "accountId": "test"}
        }
        mock_api.get_ships.return_value = {"data": []}
        mock_get_api_client.return_value = mock_api

        # Execute
        args = argparse.Namespace(player_id=saved_player.player_id, agent=None)
        result = sync_ships_command(args)

        # Verify exit code
        assert result == 0

        # Verify output
        output = capsys.readouterr().out
        assert "✅ Successfully synced 0 ships" in output

        # Verify database state: no ships
        ships = ship_repo.find_all_by_player(saved_player.player_id)
        assert len(ships) == 0

    def test_sync_ships_player_not_found_error(self, capsys, player_repo):
        """
        GIVEN no players exist in database
        WHEN user tries to sync without specifying player
        THEN error is shown
        """
        # No setup - database is empty

        args = argparse.Namespace(player_id=None, agent=None)
        result = sync_ships_command(args)

        # Verify error exit code
        assert result == 1

        # Verify error message
        output = capsys.readouterr().out
        assert "❌" in output

    def test_sync_ships_invalid_player_id(self, capsys, player_repo):
        """
        GIVEN a non-existent player_id
        WHEN user tries to sync
        THEN error is shown
        """
        # No setup - player_id 999 doesn't exist

        args = argparse.Namespace(player_id=999, agent=None)
        result = sync_ships_command(args)

        # Verify error exit code
        assert result == 1

        # Verify error message
        output = capsys.readouterr().out
        assert "❌" in output


class TestSetupSyncCommands:
    """Tests for setup_sync_commands"""

    def test_setup_creates_sync_parser(self):
        """Test that setup creates sync parser"""
        # Arrange
        parser = argparse.ArgumentParser()
        subparsers = parser.add_subparsers(dest="command")

        # Act
        setup_sync_commands(subparsers)

        # Assert
        args = parser.parse_args(['sync', 'ships'])
        assert hasattr(args, 'func')

    def test_setup_accepts_player_id_arg(self):
        """Test that sync ships accepts player_id argument"""
        # Arrange
        parser = argparse.ArgumentParser()
        subparsers = parser.add_subparsers(dest="command")

        # Act
        setup_sync_commands(subparsers)

        # Assert
        args = parser.parse_args(['sync', 'ships', '--player-id', '2'])
        assert args.player_id == 2

    def test_setup_accepts_agent_arg(self):
        """Test that sync ships accepts agent argument"""
        # Arrange
        parser = argparse.ArgumentParser()
        subparsers = parser.add_subparsers(dest="command")

        # Act
        setup_sync_commands(subparsers)

        # Assert
        args = parser.parse_args(['sync', 'ships', '--agent', 'TEST'])
        assert args.agent == 'TEST'
