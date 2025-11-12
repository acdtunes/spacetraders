"""
gRPC server for routing service.

Runs as a standalone Python process using OR-Tools for route optimization.
"""
import argparse
import logging
import sys
import os
from concurrent import futures
import signal
import grpc

# Add the routing-service directory to path so we can import 'generated' and 'handlers' packages
script_dir = os.path.dirname(os.path.abspath(__file__))
routing_service_dir = os.path.join(script_dir, '..')
sys.path.insert(0, os.path.abspath(routing_service_dir))

from generated import routing_pb2_grpc

from handlers.routing_handler import RoutingServiceHandler

# Configure logging
logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s - %(name)s - %(levelname)s - %(message)s',
    handlers=[
        logging.StreamHandler(sys.stdout)
    ]
)

logger = logging.getLogger(__name__)


class RoutingServer:
    """gRPC server for routing service"""

    def __init__(self, host: str = '0.0.0.0', port: int = 50051, tsp_timeout: int = 5, vrp_timeout: int = 30):
        """
        Initialize routing server.

        Args:
            host: Host to bind to
            port: Port to bind to
            tsp_timeout: TSP solver timeout (seconds)
            vrp_timeout: VRP solver timeout (seconds)
        """
        self.host = host
        self.port = port
        self.tsp_timeout = tsp_timeout
        self.vrp_timeout = vrp_timeout
        self.server = None

    def start(self):
        """Start the gRPC server"""
        # Create server with thread pool
        self.server = grpc.server(futures.ThreadPoolExecutor(max_workers=10))

        # Add routing service handler
        routing_pb2_grpc.add_RoutingServiceServicer_to_server(
            RoutingServiceHandler(tsp_timeout=self.tsp_timeout, vrp_timeout=self.vrp_timeout),
            self.server
        )

        # Bind to address
        address = f'{self.host}:{self.port}'
        self.server.add_insecure_port(address)

        # Start server
        self.server.start()
        logger.info(f"Routing service started on {address}")
        logger.info(f"TSP timeout: {self.tsp_timeout}s, VRP timeout: {self.vrp_timeout}s")

    def stop(self, grace_period: int = 5):
        """
        Stop the gRPC server.

        Args:
            grace_period: Grace period for stopping server (seconds)
        """
        if self.server:
            logger.info("Stopping routing service...")
            self.server.stop(grace_period)
            logger.info("Routing service stopped")

    def wait_for_termination(self):
        """Block until server is terminated"""
        if self.server:
            self.server.wait_for_termination()


def main():
    """Main entry point"""
    parser = argparse.ArgumentParser(description='Routing Service - OR-Tools gRPC Server')
    parser.add_argument('--host', type=str, default='0.0.0.0', help='Host to bind to')
    parser.add_argument('--port', type=int, default=50051, help='Port to bind to')
    parser.add_argument('--tsp-timeout', type=int, default=5, help='TSP solver timeout (seconds)')
    parser.add_argument('--vrp-timeout', type=int, default=30, help='VRP solver timeout (seconds)')
    args = parser.parse_args()

    # Create and start server
    server = RoutingServer(
        host=args.host,
        port=args.port,
        tsp_timeout=args.tsp_timeout,
        vrp_timeout=args.vrp_timeout
    )

    # Handle graceful shutdown
    def signal_handler(sig, frame):
        logger.info(f"Received signal {sig}")
        server.stop()
        sys.exit(0)

    signal.signal(signal.SIGINT, signal_handler)
    signal.signal(signal.SIGTERM, signal_handler)

    try:
        server.start()
        logger.info("Press Ctrl+C to stop the server")
        server.wait_for_termination()
    except KeyboardInterrupt:
        logger.info("Keyboard interrupt received")
        server.stop()
    except Exception as e:
        logger.error(f"Server error: {e}", exc_info=True)
        server.stop()
        sys.exit(1)


if __name__ == '__main__':
    main()
