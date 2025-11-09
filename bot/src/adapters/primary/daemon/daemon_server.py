"""Daemon server with Unix socket and JSON-RPC 2.0"""
import asyncio
import json
import logging
import os
import signal
import threading
from datetime import datetime
from pathlib import Path
from typing import Dict, Optional

from .container_manager import ContainerManager, ContainerStatus

logger = logging.getLogger(__name__)


class DaemonServer:
    """Daemon server for long-running operations

    - Unix socket at var/daemon.sock (or SPACETRADERS_DAEMON_SOCKET env var)
    - JSON-RPC 2.0 protocol
    - Graceful shutdown
    - Health monitoring
    """

    # Allow override via environment variable for testing
    SOCKET_PATH = Path(os.environ.get('SPACETRADERS_DAEMON_SOCKET', 'var/daemon.sock'))

    def __init__(self):
        """Initialize daemon server"""
        # Import here to avoid circular dependencies
        from configuration.container import (
            get_database,
            get_mediator,
            get_ship_repository,
            get_ship_assignment_repository,
            set_container_manager
        )

        database = get_database()
        self._database = database
        self._container_mgr = ContainerManager(get_mediator(), database)
        self._assignment_repo = get_ship_assignment_repository()
        self._ship_repo = get_ship_repository()
        self._server: Optional[asyncio.Server] = None
        self._running = False

        # Make container manager globally accessible for handlers running inside containers
        set_container_manager(self._container_mgr)

    async def start(self):
        """Start daemon server"""
        # Cleanup old socket
        if self.SOCKET_PATH.exists():
            self.SOCKET_PATH.unlink()

        self.SOCKET_PATH.parent.mkdir(parents=True, exist_ok=True)

        # Create Unix socket server
        self._server = await asyncio.start_unix_server(
            self._handle_connection,
            path=str(self.SOCKET_PATH)
        )

        # Set permissions (owner read/write)
        self.SOCKET_PATH.chmod(0o660)
        self._running = True

        # 1. Release all active ship assignments from previous daemon instance
        await self.release_all_active_assignments()

        # 2. Recover RUNNING containers from database
        await self.recover_running_containers()

        logger.info(f"Daemon server started on {self.SOCKET_PATH}")

        # Register signal handlers (only works in main thread)
        if threading.current_thread() is threading.main_thread():
            for sig in (signal.SIGTERM, signal.SIGINT):
                asyncio.get_event_loop().add_signal_handler(
                    sig,
                    lambda: asyncio.create_task(self.stop())
                )
        else:
            logger.debug("Skipping signal handlers (not in main thread)")

        # Start health monitor
        asyncio.create_task(self._health_monitor())

        # Serve forever
        async with self._server:
            await self._server.serve_forever()

    async def stop(self):
        """Graceful shutdown"""
        if not self._running:
            return

        logger.info("Shutting down daemon server...")
        self._running = False

        # Stop all containers
        for container in self._container_mgr.list_containers():
            try:
                await self._container_mgr.stop_container(container.container_id)
            except Exception as e:
                logger.error(f"Error stopping container {container.container_id}: {e}")

        # Close server immediately
        if self._server:
            self._server.close()
            # NOTE: We do NOT call await self._server.wait_closed() here because:
            # 1. It waits for all client connections to acknowledge closure
            # 2. This can add unnecessary delays during daemon shutdown
            # 3. server.close() is sufficient - it stops accepting new connections
            # 4. Active connections will finish their current operations
            # 5. The OS will handle final cleanup asynchronously

        # Cleanup socket
        if self.SOCKET_PATH.exists():
            self.SOCKET_PATH.unlink()

        logger.info("Daemon server stopped")

    async def _handle_connection(
        self,
        reader: asyncio.StreamReader,
        writer: asyncio.StreamWriter
    ):
        """Handle client connection"""
        try:
            data = await reader.read(65536)
            request = json.loads(data.decode())

            logger.info(f"Received request: {request.get('method')} (id={request.get('id')})")

            # Process JSON-RPC request
            response = await self._process_request(request)

            # Send response with proper JSON serialization
            # Use ensure_ascii=True for safe transmission, separators for minimal size
            response_json = json.dumps(
                response,
                ensure_ascii=True,
                separators=(',', ':')
            )
            response_bytes = response_json.encode('utf-8')

            # Write all data and ensure it's flushed before closing
            writer.write(response_bytes)
            await writer.drain()

            logger.info(f"Sent response: {len(response_bytes)} bytes (id={request.get('id')})")

        except Exception as e:
            logger.error(f"Error handling connection: {e}", exc_info=True)
            error_response = {
                "jsonrpc": "2.0",
                "error": {"code": -32603, "message": str(e)},
                "id": None
            }
            error_json = json.dumps(
                error_response,
                ensure_ascii=True,
                separators=(',', ':')
            )
            writer.write(error_json.encode('utf-8'))
            await writer.drain()

        finally:
            # Ensure all data is sent before closing
            try:
                await writer.drain()
            except Exception:
                pass  # Ignore errors during final drain

            # Close the connection immediately
            writer.close()
            # NOTE: We do NOT call await writer.wait_closed() here because:
            # 1. It waits for the client to acknowledge the socket closure
            # 2. This can take 60+ seconds if client is slow or network has issues
            # 3. writer.close() is sufficient for cleanup - it closes the socket
            # 4. The OS will handle final TCP handshake asynchronously
            # 5. This ensures instant RPC response times for MCP tools

    async def _process_request(self, request: Dict) -> Dict:
        """Process JSON-RPC request

        Args:
            request: JSON-RPC 2.0 request

        Returns:
            JSON-RPC 2.0 response
        """
        method = request.get("method")
        params = request.get("params", {})
        request_id = request.get("id")

        try:
            if method == "container.create":
                result = await self._create_container(params)
            elif method == "container.stop":
                result = await self._stop_container(params)
            elif method == "container.inspect":
                result = self._inspect_container(params)
            elif method == "container.list":
                result = self._list_containers(params)
            elif method == "container.logs":
                result = self._get_container_logs(params)
            elif method == "container.remove":
                result = await self._remove_container(params)
            else:
                raise ValueError(f"Unknown method: {method}")

            return {
                "jsonrpc": "2.0",
                "result": result,
                "id": request_id
            }

        except Exception as e:
            logger.error(f"Error processing request {method}: {e}", exc_info=True)
            return {
                "jsonrpc": "2.0",
                "error": {"code": -32603, "message": str(e)},
                "id": request_id
            }

    async def _create_container(self, params: Dict) -> Dict:
        """Create container with validation

        Args:
            params: Container parameters

        Returns:
            Dict with container_id and status
        """
        container_id = params["container_id"]
        player_id = params["player_id"]
        container_type = params["container_type"]
        config = params.get("config", {})
        restart_policy = params.get("restart_policy", "no")

        # Validate ship exists and is available
        ship_symbol = config.get('params', {}).get('ship_symbol')
        if ship_symbol:
            ship = self._ship_repo.find_by_symbol(ship_symbol, player_id)
            if not ship:
                raise ValueError(f"Ship {ship_symbol} not found")

            # Assign ship
            if not self._assignment_repo.assign(
                player_id, ship_symbol, container_id, container_type
            ):
                raise ValueError(f"Ship {ship_symbol} already assigned")

        # Create container
        info = await self._container_mgr.create_container(
            container_id=container_id,
            player_id=player_id,
            container_type=container_type,
            config=config,
            restart_policy=restart_policy
        )

        return {
            "container_id": info.container_id,
            "status": info.status.value
        }

    async def _stop_container(self, params: Dict) -> Dict:
        """Stop container and release ship

        Args:
            params: Parameters with container_id

        Returns:
            Dict with container_id and status
        """
        container_id = params["container_id"]
        await self._container_mgr.stop_container(container_id)

        # Release ship assignment
        info = self._container_mgr.get_container(container_id)
        if info:
            ship_symbol = info.config.get('params', {}).get('ship_symbol')
            if ship_symbol:
                self._assignment_repo.release(
                    info.player_id,
                    ship_symbol,
                    reason="stopped"
                )

        return {
            "container_id": container_id,
            "status": "stopped"
        }

    def _inspect_container(self, params: Dict) -> Dict:
        """Inspect container

        Args:
            params: Parameters with container_id, optional limit for logs

        Returns:
            Dict with container details including logs from database
        """
        container_id = params["container_id"]
        info = self._container_mgr.get_container(container_id)

        # If not in memory, try to load from database
        if not info:
            with self._database.connection() as conn:
                cursor = conn.cursor()
                cursor.execute("""
                    SELECT container_id, player_id, container_type, command_type, status,
                           started_at, stopped_at, restart_count, exit_code
                    FROM containers
                    WHERE container_id = ?
                """, (container_id,))
                row = cursor.fetchone()

                if not row:
                    raise ValueError(f"Container {container_id} not found")

                # Create a minimal info object from database data
                from dataclasses import dataclass
                from datetime import datetime
                from adapters.primary.daemon.container_manager import ContainerStatus

                @dataclass
                class DbContainerInfo:
                    container_id: str
                    player_id: int
                    container_type: str
                    command_type: str
                    status: ContainerStatus
                    started_at: datetime
                    stopped_at: datetime
                    iteration: int  # Not in DB, default to 0
                    restart_count: int
                    exit_code: int

                # Import _parse_datetime helper for PostgreSQL compatibility
                from adapters.secondary.persistence.mappers import _parse_datetime

                info = DbContainerInfo(
                    container_id=row['container_id'],
                    player_id=row['player_id'],
                    container_type=row['container_type'],
                    command_type=row['command_type'],
                    status=ContainerStatus(row['status']),
                    started_at=_parse_datetime(row['started_at']),
                    stopped_at=_parse_datetime(row['stopped_at']),
                    iteration=0,  # Not stored in DB
                    restart_count=row['restart_count'] or 0,
                    exit_code=row['exit_code']
                )

        # Get logs from database
        log_limit = params.get("log_limit", 50)
        logs = self._database.get_container_logs(
            container_id=container_id,
            player_id=info.player_id,
            limit=log_limit
        )

        # Extract command_type from config if available
        command_type = None
        if hasattr(info, 'config') and isinstance(info.config, dict):
            command_type = info.config.get('command_type')
        elif hasattr(info, 'command_type'):
            command_type = info.command_type

        return {
            "container_id": info.container_id,
            "player_id": info.player_id,
            "type": info.container_type,
            "command_type": command_type,
            "status": info.status.value,
            "iteration": info.iteration if hasattr(info, 'iteration') else 0,
            "restart_count": info.restart_count,
            "started_at": info.started_at.isoformat() if info.started_at else None,
            "stopped_at": info.stopped_at.isoformat() if info.stopped_at else None,
            "exit_code": info.exit_code,
            "logs": logs
        }

    def _list_containers(self, params: Dict) -> Dict:
        """List containers

        Args:
            params: Optional parameters with player_id

        Returns:
            Dict with containers list
        """
        player_id = params.get("player_id")
        containers = self._container_mgr.list_containers(player_id)

        return {
            "containers": [
                {
                    "container_id": c.container_id,
                    "player_id": c.player_id,
                    "type": c.container_type,
                    "command_type": c.config.get('command_type') if isinstance(c.config, dict) else None,
                    "status": c.status.value
                }
                for c in containers
            ]
        }

    async def _remove_container(self, params: Dict) -> Dict:
        """Remove container

        Args:
            params: Parameters with container_id

        Returns:
            Dict with container_id
        """
        container_id = params["container_id"]
        await self._container_mgr.remove_container(container_id)
        return {"container_id": container_id}

    def _get_container_logs(self, params: Dict) -> Dict:
        """Get container logs from database

        Args:
            params: Parameters with container_id, player_id, optional limit, level, since

        Returns:
            Dict with logs list
        """
        container_id = params["container_id"]
        player_id = params["player_id"]
        limit = params.get("limit", 100)
        level = params.get("level")
        since = params.get("since")

        logs = self._database.get_container_logs(
            container_id=container_id,
            player_id=player_id,
            limit=limit,
            level=level,
            since=since
        )

        return {
            "container_id": container_id,
            "player_id": player_id,
            "logs": logs
        }

    async def _health_monitor(self):
        """Monitor container health and clean up stale assignments"""
        while self._running:
            await asyncio.sleep(60)

            # Log container status
            containers = self._container_mgr.list_containers()
            if containers:
                logger.debug(f"Health check: {len(containers)} containers running")

            # Check for stale ship assignments (assigned but container not running)
            await self.cleanup_stale_assignments()

    async def release_all_active_assignments(self):
        """Release all active ship assignments on daemon startup

        Called during daemon startup to clean up zombie assignments from
        previous daemon instances that crashed or were killed.
        """
        try:
            count = self._assignment_repo.release_all_active_assignments(
                reason="daemon_restart"
            )
            if count > 0:
                logger.info(f"Released {count} zombie assignment(s) on daemon startup")
        except Exception as e:
            logger.error(f"Error releasing zombie assignments: {e}")

    async def recover_running_containers(self):
        """Recover RUNNING containers from database on daemon startup

        Queries the database for containers with status='RUNNING' and attempts
        to recreate them in the ContainerManager. Handles edge cases:
        - Ships that no longer exist (mark as FAILED)
        - Invalid configuration (mark as FAILED)
        - Ship assignment conflicts (already handled by release_all_active_assignments)

        This ensures operations survive daemon restarts and maintain business continuity.
        """
        try:
            # Query database for RUNNING containers
            with self._database.connection() as conn:
                cursor = conn.cursor()
                cursor.execute("""
                    SELECT container_id, player_id, container_type, config,
                           restart_policy, restart_count
                    FROM containers
                    WHERE status = 'RUNNING'
                    ORDER BY started_at ASC
                """)
                running_containers = cursor.fetchall()

            if not running_containers:
                logger.info("No RUNNING containers to recover")
                return

            logger.info(f"Recovering {len(running_containers)} RUNNING container(s)")

            for row in running_containers:
                container_id = row['container_id']
                player_id = row['player_id']
                container_type = row['container_type']
                restart_policy = row['restart_policy'] or 'no'
                restart_count = row['restart_count'] or 0

                try:
                    # Parse configuration
                    config = json.loads(row['config'])

                    # Validate ship exists if container uses a ship
                    ship_symbol = config.get('params', {}).get('ship_symbol')
                    if ship_symbol:
                        ship = self._ship_repo.find_by_symbol(ship_symbol, player_id)
                        if not ship:
                            logger.warning(
                                f"Cannot recover container {container_id}: "
                                f"ship {ship_symbol} no longer exists"
                            )
                            self._mark_container_failed(
                                container_id, player_id, "ship_not_found"
                            )
                            continue

                        # Assign ship (should succeed since we released all zombie assignments)
                        if not self._assignment_repo.assign(
                            player_id, ship_symbol, container_id, container_type
                        ):
                            logger.warning(
                                f"Cannot recover container {container_id}: "
                                f"ship {ship_symbol} assignment conflict"
                            )
                            self._mark_container_failed(
                                container_id, player_id, "assignment_conflict"
                            )
                            continue

                    # Recreate container in ContainerManager (without re-inserting to DB)
                    await self._recreate_container(
                        container_id=container_id,
                        player_id=player_id,
                        container_type=container_type,
                        config=config,
                        restart_policy=restart_policy,
                        restart_count=restart_count
                    )

                    logger.info(f"Recovered container {container_id}")

                except json.JSONDecodeError as e:
                    logger.error(
                        f"Cannot recover container {container_id}: "
                        f"invalid JSON configuration: {e}"
                    )
                    self._mark_container_failed(
                        container_id, player_id, "invalid_config"
                    )
                except Exception as e:
                    logger.error(
                        f"Failed to recover container {container_id}: {e}",
                        exc_info=True
                    )
                    self._mark_container_failed(
                        container_id, player_id, f"recovery_error: {str(e)}"
                    )

        except Exception as e:
            logger.error(f"Error during container recovery: {e}", exc_info=True)

    async def _recreate_container(
        self,
        container_id: str,
        player_id: int,
        container_type: str,
        config: dict,
        restart_policy: str,
        restart_count: int
    ):
        """Recreate container in ContainerManager without DB insert

        Used during recovery to restore containers from database into memory.
        Does not insert new database record since container already exists.

        Args:
            container_id: Container ID
            player_id: Player ID
            container_type: Container type (e.g., 'command')
            config: Container configuration dict
            restart_policy: Restart policy string
            restart_count: Current restart count
        """
        from adapters.primary.daemon.types import ContainerInfo, RestartPolicy
        from copy import deepcopy

        # Create container info
        config_copy = deepcopy(config)
        info = ContainerInfo(
            container_id=container_id,
            player_id=player_id,
            container_type=container_type,
            status=ContainerStatus.STARTING,
            restart_policy=RestartPolicy(restart_policy),
            restart_count=restart_count,
            max_restarts=3,
            config=config_copy,
            task=None,
            logs=[],
            started_at=datetime.now(),
            stopped_at=None,
            exit_code=None,
            exit_reason=None
        )

        # Create container instance
        container_class = self._container_mgr._container_types.get(container_type)
        if not container_class:
            raise ValueError(f"Unknown container type: {container_type}")

        container = container_class(
            container_id=container_id,
            player_id=player_id,
            config=config_copy,
            mediator=self._container_mgr._mediator,
            database=self._container_mgr._database,
            container_info=info
        )

        # Start task
        info.task = asyncio.create_task(
            self._container_mgr._run_container(info, container)
        )

        # Add to in-memory container manager (without DB insert)
        async with self._container_mgr._lock:
            self._container_mgr._containers[container_id] = info

    def _mark_container_failed(self, container_id: str, player_id: int, reason: str):
        """Mark a container as FAILED in the database

        Args:
            container_id: Container ID
            player_id: Player ID
            reason: Failure reason for logging
        """
        from datetime import datetime

        try:
            self._database.update_container_status(
                container_id=container_id,
                player_id=player_id,
                status='FAILED',
                stopped_at=datetime.now().isoformat(),
                exit_code=1,
                exit_reason=reason
            )
            logger.info(f"Marked container {container_id} as FAILED: {reason}")
        except Exception as e:
            logger.error(
                f"Failed to mark container {container_id} as FAILED: {e}"
            )

    async def cleanup_stale_assignments(self):
        """Clean up ship assignments where container no longer exists (PUBLIC API)

        This handles cases where:
        - Container failed but didn't release assignment
        - Container was forcefully stopped
        - Daemon crashed mid-operation
        """
        try:
            # Get all active assignments from database
            with self._database.connection() as conn:
                cursor = conn.cursor()
                cursor.execute("""
                    SELECT ship_symbol, player_id, container_id, operation
                    FROM ship_assignments
                    WHERE status = 'active'
                """)
                active_assignments = cursor.fetchall()

            # Get list of running container IDs
            running_containers = self._container_mgr.list_containers()
            running_container_ids = {c.container_id for c in running_containers}

            # Release assignments for non-existent containers
            stale_count = 0
            for assignment in active_assignments:
                if assignment["container_id"] not in running_container_ids:
                    logger.warning(
                        f"Cleaning up stale assignment: {assignment['ship_symbol']} "
                        f"was assigned to {assignment['container_id']} (not running)"
                    )
                    self._assignment_repo.release(
                        assignment["player_id"],
                        assignment["ship_symbol"],
                        reason="stale_cleanup"
                    )
                    stale_count += 1

            if stale_count > 0:
                logger.info(f"Cleaned up {stale_count} stale ship assignment(s)")

        except Exception as e:
            logger.error(f"Error cleaning up stale assignments: {e}")


def main():
    """Entry point for daemon server"""
    logging.basicConfig(
        level=logging.INFO,
        format='%(asctime)s - %(name)s - %(levelname)s - %(message)s'
    )

    server = DaemonServer()

    try:
        asyncio.run(server.start())
    except KeyboardInterrupt:
        logger.info("Interrupted by user")


if __name__ == "__main__":
    main()
