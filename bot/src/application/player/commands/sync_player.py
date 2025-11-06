"""
Sync Player Command - Fetch player/agent data from SpaceTraders API and update database
"""
from dataclasses import dataclass
import logging

from pymediatr import Request, RequestHandler
from ports.repositories import IPlayerRepository
from domain.shared.player import Player

logger = logging.getLogger(__name__)


@dataclass(frozen=True)
class SyncPlayerCommand(Request[Player]):
    """
    Command to sync player/agent data from SpaceTraders API to local database.

    Fetches agent info (credits, headquarters, metadata) from the API and updates
    the player record in the local database.
    """
    player_id: int


class SyncPlayerHandler(RequestHandler[SyncPlayerCommand, Player]):
    """Handler for syncing player data from API to database"""

    def __init__(self, player_repository: IPlayerRepository):
        self._player_repo = player_repository

    async def handle(self, request: SyncPlayerCommand) -> Player:
        """
        Sync player data from API to database.

        Steps:
        1. Get player from database to retrieve token
        2. Get API client for this player
        3. Fetch agent info from API
        4. Update player credits and metadata
        5. Save updated player to database
        6. Return updated player

        Returns:
            Updated Player entity
        """
        logger.info(f"Syncing player data for player {request.player_id}")

        # 1. Get player from database
        player = self._player_repo.find_by_id(request.player_id)
        if not player:
            raise ValueError(f"Player {request.player_id} not found")

        # 2. Get API client for this player (import here to avoid circular dependency)
        from configuration.container import get_api_client_for_player
        api_client = get_api_client_for_player(request.player_id)

        # 3. Fetch agent info from API
        agent_response = api_client.get_agent()
        agent_data = agent_response.get('data', {})

        logger.info(f"Agent: {agent_data.get('symbol')}, Credits: {agent_data.get('credits')}")

        # 4. Update player credits
        new_credits = agent_data.get('credits', 0)
        # Calculate the difference and apply it
        credit_diff = new_credits - player.credits
        if credit_diff > 0:
            player.add_credits(credit_diff)
        elif credit_diff < 0:
            player.spend_credits(abs(credit_diff))

        # 5. Update player metadata with agent info (preserving existing metadata)
        metadata_updates = {}
        if 'headquarters' in agent_data:
            metadata_updates['headquarters'] = agent_data['headquarters']
        if 'shipCount' in agent_data:
            metadata_updates['shipCount'] = agent_data['shipCount']
        if 'accountId' in agent_data:
            metadata_updates['accountId'] = agent_data['accountId']

        if metadata_updates:
            player.update_metadata(metadata_updates, replace=False)

        # 6. Save updated player to database
        self._player_repo.update(player)

        logger.info(f"Successfully synced player {request.player_id}: {new_credits} credits")
        return player
