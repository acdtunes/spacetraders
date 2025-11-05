"""
Application settings and configuration.

Provides centralized configuration for the SpaceTraders bot.
"""
from pathlib import Path
from dataclasses import dataclass


@dataclass
class Settings:
    """
    Application settings.

    Attributes:
        db_path: Path to SQLite database file
    """
    db_path: Path = Path("var/spacetraders.db")


# Global settings instance
settings = Settings()
