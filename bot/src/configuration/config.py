"""
User configuration management for SpaceTraders CLI.

Handles reading and writing user preferences to ~/.spacetraders/config.json
"""
import json
import logging
from pathlib import Path
from typing import Optional, Dict, Any

logger = logging.getLogger(__name__)


class Config:
    """
    User configuration manager.

    Stores user preferences like default player ID in ~/.spacetraders/config.json
    """

    def __init__(self, config_path: Optional[Path] = None):
        """
        Initialize config manager.

        Args:
            config_path: Path to config file. Defaults to ~/.spacetraders/config.json
        """
        if config_path:
            self.config_path = config_path
        else:
            self.config_path = Path.home() / ".spacetraders" / "config.json"

        self._config: Dict[str, Any] = {}
        self._load()

    def _load(self):
        """Load configuration from file"""
        if not self.config_path.exists():
            logger.debug(f"Config file not found at {self.config_path}, using defaults")
            self._config = {}
            return

        try:
            with open(self.config_path, 'r') as f:
                self._config = json.load(f)
            logger.debug(f"Loaded config from {self.config_path}")
        except json.JSONDecodeError as e:
            logger.warning(f"Invalid JSON in config file: {e}, using defaults")
            self._config = {}
        except Exception as e:
            logger.warning(f"Error loading config: {e}, using defaults")
            self._config = {}

    def _save(self):
        """Save configuration to file"""
        try:
            # Create directory if it doesn't exist
            self.config_path.parent.mkdir(parents=True, exist_ok=True)

            with open(self.config_path, 'w') as f:
                json.dump(self._config, f, indent=2)

            logger.debug(f"Saved config to {self.config_path}")
        except Exception as e:
            logger.error(f"Error saving config: {e}")
            raise

    @property
    def default_player_id(self) -> Optional[int]:
        """Get default player ID"""
        return self._config.get('default_player_id')

    @default_player_id.setter
    def default_player_id(self, player_id: Optional[int]):
        """Set default player ID"""
        if player_id is None:
            self._config.pop('default_player_id', None)
        else:
            self._config['default_player_id'] = player_id
        self._save()

    @property
    def default_agent(self) -> Optional[str]:
        """Get default agent symbol"""
        return self._config.get('default_agent')

    @default_agent.setter
    def default_agent(self, agent_symbol: Optional[str]):
        """Set default agent symbol"""
        if agent_symbol is None:
            self._config.pop('default_agent', None)
        else:
            self._config['default_agent'] = agent_symbol
        self._save()

    def set_default_player(self, player_id: int, agent_symbol: str):
        """
        Set default player by ID and agent symbol.

        Args:
            player_id: Player ID to set as default
            agent_symbol: Agent symbol to set as default
        """
        self._config['default_player_id'] = player_id
        self._config['default_agent'] = agent_symbol
        self._save()
        logger.info(f"Set default player to {agent_symbol} (ID: {player_id})")

    def clear_default_player(self):
        """Clear default player setting"""
        self._config.pop('default_player_id', None)
        self._config.pop('default_agent', None)
        self._save()
        logger.info("Cleared default player")

    def get(self, key: str, default: Any = None) -> Any:
        """
        Get arbitrary config value.

        Args:
            key: Config key
            default: Default value if key not found

        Returns:
            Config value or default
        """
        return self._config.get(key, default)

    def set(self, key: str, value: Any):
        """
        Set arbitrary config value.

        Args:
            key: Config key
            value: Value to set
        """
        self._config[key] = value
        self._save()


# Global config instance
_config: Optional[Config] = None


def get_config() -> Config:
    """
    Get global config instance.

    Returns:
        Config: Global config instance
    """
    global _config
    if _config is None:
        _config = Config()
    return _config


def reset_config():
    """Reset global config instance (useful for testing)"""
    global _config
    _config = None
