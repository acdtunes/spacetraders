from abc import ABC, abstractmethod
from typing import Dict, Optional

class ISpaceTradersAPI(ABC):
    """Port for SpaceTraders game API"""

    @abstractmethod
    def get_agent(self) -> Dict:
        """Get agent info"""
        pass

    @abstractmethod
    def get_ship(self, ship_symbol: str) -> Dict:
        """Get ship details"""
        pass

    @abstractmethod
    def get_ships(self) -> Dict:
        """Get all ships for the agent"""
        pass

    @abstractmethod
    def navigate_ship(self, ship_symbol: str, waypoint: str) -> Dict:
        """Navigate ship to waypoint"""
        pass

    @abstractmethod
    def dock_ship(self, ship_symbol: str) -> Dict:
        """Dock ship at current waypoint"""
        pass

    @abstractmethod
    def orbit_ship(self, ship_symbol: str) -> Dict:
        """Put ship in orbit"""
        pass

    @abstractmethod
    def refuel_ship(self, ship_symbol: str) -> Dict:
        """Refuel ship at current waypoint"""
        pass

    @abstractmethod
    def set_flight_mode(self, ship_symbol: str, flight_mode: str) -> Dict:
        """Set ship flight mode (CRUISE, DRIFT, BURN, STEALTH)"""
        pass

    @abstractmethod
    def list_waypoints(
        self,
        system_symbol: str,
        page: int = 1,
        limit: int = 20
    ) -> Dict:
        """List waypoints in system"""
        pass

    @abstractmethod
    def get_shipyard(self, system_symbol: str, waypoint_symbol: str) -> Dict:
        """Get shipyard details at a waypoint.

        Args:
            system_symbol: The system symbol (e.g., "X1-GZ7")
            waypoint_symbol: The waypoint symbol (e.g., "X1-GZ7-AB12")

        Returns:
            Dict containing shipyard information including available ships
        """
        pass

    @abstractmethod
    def purchase_ship(self, ship_type: str, waypoint_symbol: str) -> Dict:
        """Purchase a ship at a shipyard.

        Args:
            ship_type: The type of ship to purchase (e.g., "SHIP_MINING_DRONE")
            waypoint_symbol: The waypoint symbol where the shipyard is located

        Returns:
            Dict containing the newly purchased ship data and transaction info
        """
        pass

    @abstractmethod
    def get_market(self, system: str, waypoint: str) -> Dict:
        """Get market data for a waypoint.

        Args:
            system: System symbol (e.g., "X1-GZ7")
            waypoint: Waypoint symbol (e.g., "X1-GZ7-A1")

        Returns:
            Dict containing market data including tradeGoods list
        """
        pass

    @abstractmethod
    def get_contracts(self, page: int = 1, limit: int = 20) -> Dict:
        """Get all contracts with pagination.

        Args:
            page: Page number (default: 1)
            limit: Results per page (default: 20)

        Returns:
            Dict containing contracts list
        """
        pass

    @abstractmethod
    def get_contract(self, contract_id: str) -> Dict:
        """Get specific contract details.

        Args:
            contract_id: Contract identifier

        Returns:
            Dict containing contract details
        """
        pass

    @abstractmethod
    def accept_contract(self, contract_id: str) -> Dict:
        """Accept a contract.

        Args:
            contract_id: Contract identifier

        Returns:
            Dict containing updated contract and agent data
        """
        pass

    @abstractmethod
    def deliver_contract(self, contract_id: str, ship_symbol: str, trade_symbol: str, units: int) -> Dict:
        """Deliver goods to fulfill contract.

        Args:
            contract_id: Contract identifier
            ship_symbol: Ship making the delivery
            trade_symbol: Good being delivered
            units: Number of units to deliver

        Returns:
            Dict containing updated contract and cargo data
        """
        pass

    @abstractmethod
    def fulfill_contract(self, contract_id: str) -> Dict:
        """Complete and fulfill a contract.

        Args:
            contract_id: Contract identifier

        Returns:
            Dict containing fulfilled contract and payment data
        """
        pass

    @abstractmethod
    def negotiate_contract(self, ship_symbol: str) -> Dict:
        """Negotiate a new contract with ship.

        Args:
            ship_symbol: Ship negotiating the contract

        Returns:
            Dict containing new contract data
        """
        pass

    @abstractmethod
    def purchase_cargo(self, ship_symbol: str, trade_symbol: str, units: int) -> Dict:
        """Purchase cargo from market.

        Args:
            ship_symbol: Ship making the purchase
            trade_symbol: Good to purchase (e.g., "IRON_ORE")
            units: Number of units to purchase

        Returns:
            Dict containing transaction details including updated cargo and credits
        """
        pass

    @abstractmethod
    def sell_cargo(self, ship_symbol: str, trade_symbol: str, units: int) -> Dict:
        """Sell cargo at market.

        Args:
            ship_symbol: Ship selling the cargo
            trade_symbol: Good to sell (e.g., "IRON_ORE")
            units: Number of units to sell

        Returns:
            Dict containing transaction details including updated cargo and credits
        """
        pass

    @abstractmethod
    def jettison_cargo(self, ship_symbol: str, cargo_symbol: str, units: int) -> Dict:
        """Jettison cargo from ship into space.

        Args:
            ship_symbol: Ship jettisoning the cargo
            cargo_symbol: Good to jettison (e.g., "IRON_ORE")
            units: Number of units to jettison

        Returns:
            Dict containing updated cargo data
        """
        pass
