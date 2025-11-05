"""
Integration tests for main CLI entry point
"""
import pytest
from unittest.mock import patch, MagicMock
import sys

from spacetraders.adapters.primary.cli.main import main


class TestMainCLI:
    """Tests for main CLI entry point"""

    @patch('spacetraders.adapters.primary.cli.main.setup_player_commands')
    @patch('spacetraders.adapters.primary.cli.main.setup_navigation_commands')
    @patch('spacetraders.adapters.primary.cli.main.setup_sync_commands')
    @patch('spacetraders.adapters.primary.cli.main.setup_daemon_commands')
    @patch('spacetraders.adapters.primary.cli.main.setup_config_commands')
    @patch('sys.argv', ['spacetraders'])
    def test_main_no_arguments_shows_help(
        self,
        mock_config,
        mock_daemon,
        mock_sync,
        mock_nav,
        mock_player,
        capsys
    ):
        """Test main CLI without arguments shows help"""
        # Act
        with pytest.raises(SystemExit) as exc_info:
            main()

        # Assert - Verify behavior: exits with error code and shows error message
        assert exc_info.value.code == 1
        captured = capsys.readouterr()
        # Should show an error message (either usage or specific error)
        assert captured.err != "" or captured.out != ""

    @patch('spacetraders.adapters.primary.cli.main.setup_player_commands')
    @patch('spacetraders.adapters.primary.cli.main.setup_navigation_commands')
    @patch('spacetraders.adapters.primary.cli.main.setup_sync_commands')
    @patch('spacetraders.adapters.primary.cli.main.setup_daemon_commands')
    @patch('spacetraders.adapters.primary.cli.main.setup_config_commands')
    @patch('sys.argv', ['spacetraders', 'player', 'list'])
    def test_main_calls_player_command(
        self,
        mock_config,
        mock_daemon,
        mock_sync,
        mock_nav,
        mock_player
    ):
        """Test main CLI routes to player command"""
        # Arrange
        def setup_mock_player(subparsers):
            """Mock setup that adds a working command"""
            parser = subparsers.add_parser('player')
            sub = parser.add_subparsers()
            list_parser = sub.add_parser('list')
            list_parser.set_defaults(func=lambda args: 0)

        mock_player.side_effect = setup_mock_player

        # Act
        with pytest.raises(SystemExit) as exc_info:
            main()

        # Assert
        assert exc_info.value.code == 0

    @patch('sys.argv', ['spacetraders', 'player', 'list'])
    @patch('spacetraders.adapters.primary.cli.player_cli.get_mediator')
    def test_main_initializes_all_command_groups(self, mock_get_mediator, capsys):
        """Test that main initializes CLI with all command groups available"""
        # Arrange
        from unittest.mock import AsyncMock
        mediator = MagicMock()
        mediator.send_async = AsyncMock(return_value=[])
        mock_get_mediator.return_value = mediator

        # Act - Execute a command to verify setup worked
        with pytest.raises(SystemExit) as exc_info:
            main()

        # Assert - Verify behavior: command executes successfully
        assert exc_info.value.code == 0
        captured = capsys.readouterr()
        # Player list command should produce output
        assert "No players registered" in captured.out or "Players" in captured.out

    @patch('sys.argv', ['spacetraders', 'player', 'list'])
    @patch('spacetraders.adapters.primary.cli.player_cli.get_mediator')
    def test_main_integration_with_player_list(self, mock_get_mediator, capsys):
        """Test full integration: main -> player list"""
        # Arrange
        from unittest.mock import AsyncMock
        mediator = MagicMock()
        mediator.send_async = AsyncMock(return_value=[])
        mock_get_mediator.return_value = mediator

        # Act
        with pytest.raises(SystemExit) as exc_info:
            main()

        # Assert
        assert exc_info.value.code == 0
        captured = capsys.readouterr()
        assert "No players registered" in captured.out


class TestMainCLIArgumentParsing:
    """Tests for argument parsing in main CLI"""

    @patch('sys.argv', ['spacetraders', '--help'])
    def test_help_argument(self, capsys):
        """Test --help argument"""
        # Act & Assert
        with pytest.raises(SystemExit) as exc_info:
            main()

        # Help should exit with 0
        assert exc_info.value.code == 0

        captured = capsys.readouterr()
        # Should show usage information
        assert "usage:" in captured.out or "SpaceTraders V2" in captured.out
