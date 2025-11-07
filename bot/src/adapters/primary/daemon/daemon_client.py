"""Daemon client for JSON-RPC communication via Unix socket"""
import json
import socket
from pathlib import Path
from typing import Dict, Optional


class DaemonClient:
    """Client for daemon communication via Unix socket"""

    SOCKET_PATH = Path("var/daemon.sock")

    def create_container(self, config: Dict) -> Dict:
        """Create container

        Args:
            config: Container configuration with fields:
                - container_id: Unique container ID
                - player_id: Player ID
                - container_type: Type of container (e.g., 'command')
                - config: Container-specific config
                - restart_policy: Optional restart policy

        Returns:
            Dict with container_id and status
        """
        return self._send_request({
            "jsonrpc": "2.0",
            "method": "container.create",
            "params": config,
            "id": 1
        })

    def stop_container(self, container_id: str) -> Dict:
        """Stop running container

        Args:
            container_id: Container to stop

        Returns:
            Dict with container_id and status
        """
        return self._send_request({
            "jsonrpc": "2.0",
            "method": "container.stop",
            "params": {"container_id": container_id},
            "id": 1
        })

    def inspect_container(self, container_id: str) -> Dict:
        """Inspect container details

        Args:
            container_id: Container to inspect

        Returns:
            Dict with container details (id, status, type, iteration, etc.)
        """
        return self._send_request({
            "jsonrpc": "2.0",
            "method": "container.inspect",
            "params": {"container_id": container_id},
            "id": 1
        })

    def list_containers(self, player_id: Optional[int] = None) -> Dict:
        """List containers, optionally filtered by player

        Args:
            player_id: Optional player ID to filter by

        Returns:
            Dict with 'containers' list
        """
        params = {"player_id": player_id} if player_id else {}
        return self._send_request({
            "jsonrpc": "2.0",
            "method": "container.list",
            "params": params,
            "id": 1
        })

    def remove_container(self, container_id: str) -> Dict:
        """Remove stopped container

        Args:
            container_id: Container to remove

        Returns:
            Dict with container_id
        """
        return self._send_request({
            "jsonrpc": "2.0",
            "method": "container.remove",
            "params": {"container_id": container_id},
            "id": 1
        })

    def get_container_logs(
        self,
        container_id: str,
        player_id: int,
        limit: int = 100,
        level: Optional[str] = None,
        since: Optional[str] = None
    ) -> Dict:
        """Get container logs from database

        Args:
            container_id: Container to get logs for
            player_id: Player ID
            limit: Maximum number of logs to retrieve (default 100)
            level: Optional log level filter (INFO, WARNING, ERROR, DEBUG)
            since: Optional timestamp filter - only logs after this timestamp

        Returns:
            Dict with container_id, player_id, and logs list
        """
        params = {
            "container_id": container_id,
            "player_id": player_id,
            "limit": limit
        }
        if level:
            params["level"] = level
        if since:
            params["since"] = since

        return self._send_request({
            "jsonrpc": "2.0",
            "method": "container.logs",
            "params": params,
            "id": 1
        })

    def _send_request(self, request: Dict) -> Dict:
        """Send JSON-RPC request via Unix socket

        Args:
            request: JSON-RPC 2.0 request dict

        Returns:
            Result from response

        Raises:
            Exception: If daemon returns error or socket fails
        """
        sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        try:
            sock.connect(str(self.SOCKET_PATH))
            sock.sendall(json.dumps(request).encode())

            # Read all data in chunks until socket is closed
            # (Server closes after sending complete response)
            chunks = []
            while True:
                chunk = sock.recv(65536)
                if not chunk:
                    break
                chunks.append(chunk)

            response_data = b''.join(chunks)

            # Decode and parse JSON with better error handling
            try:
                response_str = response_data.decode('utf-8')
                response = json.loads(response_str)
            except json.JSONDecodeError as e:
                # Provide detailed error information for debugging
                error_context = response_str[max(0, e.pos - 100):e.pos + 100] if 'response_str' in locals() else 'N/A'
                raise Exception(
                    f"JSON parsing error at position {e.pos}: {e.msg}\n"
                    f"Context: ...{error_context}...\n"
                    f"Response size: {len(response_data)} bytes\n"
                    f"First 200 chars: {response_str[:200] if 'response_str' in locals() else 'N/A'}"
                ) from e
            except UnicodeDecodeError as e:
                raise Exception(
                    f"UTF-8 decoding error: {e}\n"
                    f"Response size: {len(response_data)} bytes\n"
                    f"First 100 bytes (hex): {response_data[:100].hex()}"
                ) from e

            if "error" in response:
                raise Exception(response["error"]["message"])

            return response.get("result", {})
        finally:
            sock.close()
