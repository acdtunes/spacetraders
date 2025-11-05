"""
Integration tests for Shipyard CLI commands.

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
from datetime import datetime, UTC
from unittest.mock import MagicMock, patch

from spacetraders.adapters.primary.cli.shipyard_cli import (
    list_shipyard_command,
    purchase_ship_command,
    batch_purchase_command,
    setup_shipyard_commands
)
from spacetraders.domain.shared.player import Player
from spacetraders.domain.shared.ship import Ship
from spacetraders.domain.shared.value_objects import Waypoint, Fuel
from spacetraders.domain.shared.shipyard import Shipyard, ShipListing


class TestListShipyardCommand:
    """Integration tests for shipyard list command"""

    def test_list_shipyard_with_listings(self, capsys, player_repo):
        """
        GIVEN a player exists and shipyard has available ships
        WHEN user lists shipyard via CLI
        THEN available ships are displayed with prices
        """
        # Setup: Create player in database
        player = Player(
            player_id=None,
            agent_symbol="TEST_AGENT",
            token="test-token",
            created_at=datetime.now(UTC),
            last_active=datetime.now(UTC)
        )
        saved_player = player_repo.create(player)

        # Mock GetShipyardListingsQuery to return test data
        mock_shipyard = Shipyard(
            symbol="X1-TEST-AB12",
            ship_types=["SHIP_MINING_DRONE", "SHIP_PROBE"],
            listings=[
                ShipListing(
                    ship_type="SHIP_MINING_DRONE",
                    name="Mining Drone",
                    description="Basic mining vessel",
                    purchase_price=50000,
                    frame={"symbol": "FRAME_DRONE"},
                    reactor={"symbol": "REACTOR_CHEMICAL_I"},
                    engine={"symbol": "ENGINE_IMPULSE_DRIVE_I"},
                    modules=[],
                    mounts=[]
                ),
                ShipListing(
                    ship_type="SHIP_PROBE",
                    name="Probe Satellite",
                    description="Scout vessel",
                    purchase_price=25000,
                    frame={"symbol": "FRAME_PROBE"},
                    reactor={"symbol": "REACTOR_SOLAR_I"},
                    engine={"symbol": "ENGINE_ION_DRIVE_I"},
                    modules=[],
                    mounts=[]
                )
            ],
            transactions=[],
            modification_fee=0
        )

        # Patch get_mediator in the shipyard_cli module where it's imported
        with patch('spacetraders.adapters.primary.cli.shipyard_cli.get_mediator') as mock_get_mediator:
            # Create an async mock for send_async
            async def mock_send_async(query):
                return mock_shipyard

            mock_mediator = MagicMock()
            mock_mediator.send_async = mock_send_async
            mock_get_mediator.return_value = mock_mediator

            # Execute
            args = argparse.Namespace(
                waypoint="X1-TEST-AB12",
                player_id=saved_player.player_id,
                agent=None
            )

            result = list_shipyard_command(args)

        # Verify exit code
        assert result == 0

        # Verify output contains ship listings
        output = capsys.readouterr().out
        assert "Mining Drone" in output
        assert "50,000" in output or "50000" in output  # Formatted with commas
        assert "Probe Satellite" in output
        assert "25,000" in output or "25000" in output

    def test_list_shipyard_not_found(self, capsys, player_repo):
        """
        GIVEN a player exists
        WHEN user lists non-existent shipyard
        THEN error message is shown
        """
        # Setup: Create player
        player = Player(
            player_id=None,
            agent_symbol="TEST_AGENT",
            token="test-token",
            created_at=datetime.now(UTC),
            last_active=datetime.now(UTC)
        )
        saved_player = player_repo.create(player)

        # Mock GetShipyardListingsQuery to raise ShipyardNotFoundError
        from spacetraders.domain.shared.exceptions import ShipyardNotFoundError

        with patch('spacetraders.configuration.container.get_mediator') as mock_get_mediator:
            mock_mediator = MagicMock()
            mock_mediator.send_async.side_effect = ShipyardNotFoundError(
                "No shipyard found at waypoint X1-NONE-ZZ99"
            )
            mock_get_mediator.return_value = mock_mediator

            # Execute
            args = argparse.Namespace(
                waypoint="X1-NONE-ZZ99",
                player_id=saved_player.player_id,
                agent=None
            )

            result = list_shipyard_command(args)

        # Verify error exit code
        assert result == 1

        # Verify error message shown
        output = capsys.readouterr().out
        assert "Error" in output or "error" in output


class TestPurchaseShipCommand:
    """Integration tests for shipyard purchase command"""

    def test_purchase_ship_creates_daemon_container(self, capsys, player_repo):
        """
        GIVEN a player exists
        WHEN user purchases ship via CLI
        THEN daemon container is created and container ID is shown
        """
        # Setup: Create player
        player = Player(
            player_id=None,
            agent_symbol="TEST_AGENT",
            token="test-token",
            created_at=datetime.now(UTC),
            last_active=datetime.now(UTC)
        )
        saved_player = player_repo.create(player)

        # Mock daemon client in the shipyard_cli module where it's imported
        with patch('spacetraders.adapters.primary.cli.shipyard_cli.get_daemon_client') as mock_get_daemon:
            mock_daemon = MagicMock()
            mock_daemon.create_container.return_value = {
                'container_id': 'purchase-ship-test123',
                'status': 'created'
            }
            mock_get_daemon.return_value = mock_daemon

            # Execute
            args = argparse.Namespace(
                ship="TEST-SHIP-1",
                shipyard="X1-TEST-AB12",
                type="SHIP_MINING_DRONE",
                player_id=saved_player.player_id,
                agent=None
            )

            result = purchase_ship_command(args)

        # Verify success exit code
        assert result == 0

        # Verify daemon container was created with correct parameters
        mock_daemon.create_container.assert_called_once()
        call_args = mock_daemon.create_container.call_args[0][0]
        assert call_args['container_type'] == 'command'
        assert call_args['config']['command_type'] == 'PurchaseShipCommand'
        assert call_args['config']['params']['ship_symbol'] == "TEST-SHIP-1"
        assert call_args['config']['params']['shipyard_waypoint'] == "X1-TEST-AB12"
        assert call_args['config']['params']['ship_type'] == "SHIP_MINING_DRONE"

    def test_purchase_ship_player_not_found(self, capsys, player_repo):
        """
        GIVEN no player exists
        WHEN user tries to purchase ship
        THEN error message is shown
        """
        # No player setup - test with non-existent player

        # Mock player selector to raise error
        from spacetraders.adapters.primary.cli.player_selector import PlayerSelectionError

        with patch('spacetraders.adapters.primary.cli.shipyard_cli.get_player_id_from_args') as mock_get_player:
            mock_get_player.side_effect = PlayerSelectionError(
                "No players registered. Register a player first."
            )

            # Execute
            args = argparse.Namespace(
                ship="TEST-SHIP-1",
                shipyard="X1-TEST-AB12",
                type="SHIP_MINING_DRONE",
                player_id=None,
                agent=None
            )

            result = purchase_ship_command(args)

        # Verify error exit code
        assert result == 1

        # Verify error message shown
        output = capsys.readouterr().out
        assert "No players registered" in output


class TestBatchPurchaseCommand:
    """Integration tests for batch purchase command"""

    def test_batch_purchase_creates_daemon_container(self, capsys, player_repo):
        """
        GIVEN a player exists
        WHEN user batch purchases ships via CLI
        THEN daemon container is created with correct parameters
        """
        # Setup: Create player
        player = Player(
            player_id=None,
            agent_symbol="TEST_AGENT",
            token="test-token",
            created_at=datetime.now(UTC),
            last_active=datetime.now(UTC)
        )
        saved_player = player_repo.create(player)

        # Mock daemon client in the shipyard_cli module where it's imported
        with patch('spacetraders.adapters.primary.cli.shipyard_cli.get_daemon_client') as mock_get_daemon:
            mock_daemon = MagicMock()
            mock_daemon.create_container.return_value = {
                'container_id': 'batch-purchase-test456',
                'status': 'created'
            }
            mock_get_daemon.return_value = mock_daemon

            # Execute
            args = argparse.Namespace(
                ship="TEST-SHIP-1",
                shipyard="X1-TEST-AB12",
                type="SHIP_MINING_DRONE",
                quantity=5,
                max_budget=500000,
                player_id=saved_player.player_id,
                agent=None
            )

            result = batch_purchase_command(args)

        # Verify success exit code
        assert result == 0

        # Verify daemon container was created
        mock_daemon.create_container.assert_called_once()
        call_args = mock_daemon.create_container.call_args[0][0]
        assert call_args['container_type'] == 'command'
        assert call_args['config']['command_type'] == 'BatchPurchaseShipsCommand'
        assert call_args['config']['params']['purchasing_ship_symbol'] == "TEST-SHIP-1"
        assert call_args['config']['params']['quantity'] == 5
        assert call_args['config']['params']['max_budget'] == 500000


class TestSetupShipyardCommands:
    """Tests for setup_shipyard_commands"""

    def test_setup_creates_shipyard_parser(self):
        """Test that setup creates shipyard parser with subcommands"""
        # Arrange
        parser = argparse.ArgumentParser()
        subparsers = parser.add_subparsers(dest="command")

        # Act
        setup_shipyard_commands(subparsers)

        # Assert - Parse valid list command
        args = parser.parse_args(['shipyard', 'list', '--waypoint', 'X1-TEST-AB12'])
        assert hasattr(args, 'func')
        assert args.waypoint == 'X1-TEST-AB12'

    def test_setup_creates_purchase_subcommand(self):
        """Test that setup creates purchase subcommand"""
        # Arrange
        parser = argparse.ArgumentParser()
        subparsers = parser.add_subparsers(dest="command")

        # Act
        setup_shipyard_commands(subparsers)

        # Assert
        args = parser.parse_args([
            'shipyard', 'purchase',
            '--ship', 'TEST-1',
            '--shipyard', 'X1-TEST-AB12',
            '--type', 'SHIP_MINING_DRONE'
        ])
        assert hasattr(args, 'func')
        assert args.ship == 'TEST-1'
        assert args.shipyard == 'X1-TEST-AB12'
        assert args.type == 'SHIP_MINING_DRONE'

    def test_setup_creates_batch_subcommand(self):
        """Test that setup creates batch subcommand with quantity and budget"""
        # Arrange
        parser = argparse.ArgumentParser()
        subparsers = parser.add_subparsers(dest="command")

        # Act
        setup_shipyard_commands(subparsers)

        # Assert
        args = parser.parse_args([
            'shipyard', 'batch',
            '--ship', 'TEST-1',
            '--shipyard', 'X1-TEST-AB12',
            '--type', 'SHIP_MINING_DRONE',
            '--quantity', '10',
            '--max-budget', '1000000'
        ])
        assert hasattr(args, 'func')
        assert args.quantity == 10
        assert args.max_budget == 1000000
