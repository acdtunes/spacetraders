#!/usr/bin/env python3
from __future__ import annotations

"""
Ship Controller - Consolidated ship operations
Handles navigation, docking, refueling, extraction, cargo management
"""

import time
from typing import Any, Dict, Optional

from .api_client import APIClient
from .utils import (
    calculate_arrival_wait_time,
    calculate_distance,
    estimate_fuel_cost,
    parse_waypoint_symbol,
    select_flight_mode,
    timestamp,
)


class ShipController:
    """Manages all ship operations"""

    def __init__(self, api_client: APIClient, ship_symbol: str):
        self.api = api_client
        self.ship_symbol = ship_symbol

    def log(self, message: str, level: str = "INFO"):
        """Print timestamped log message (for backward compatibility)"""
        formatted_msg = f"[{timestamp()}] {message}"
        print(formatted_msg)

    # =============================================================================
    # CORE SHIP STATUS
    # =============================================================================

    def get_status(self) -> Optional[Dict[str, Any]]:
        """Get current ship status"""
        status = self.api.get_ship(self.ship_symbol)
        if not status:
            return None
        return status

    def get_location(self) -> Optional[str]:
        """Get current waypoint"""
        ship = self.get_status()
        return ship['nav']['waypointSymbol'] if ship else None

    def get_nav_status(self) -> Optional[str]:
        """Get navigation status (DOCKED, IN_ORBIT, IN_TRANSIT)"""
        ship = self.get_status()
        return ship['nav']['status'] if ship else None

    def get_fuel(self) -> Optional[Dict[str, int]]:
        """Get fuel status"""
        ship = self.get_status()
        return ship['fuel'] if ship else None

    def get_cargo(self) -> Optional[Dict[str, Any]]:
        """Get cargo status"""
        ship = self.get_status()
        return ship['cargo'] if ship else None

    # =============================================================================
    # BASIC SHIP OPERATIONS
    # =============================================================================

    def dock(self) -> bool:
        """Dock ship at current waypoint - waits for arrival if IN_TRANSIT"""
        # Check if ship is in transit first
        ship = self.get_status()
        if not ship:
            self.log("❌ Failed to get ship status", level="ERROR")
            return False

        nav_status = ship['nav']['status']

        # If ship is IN_TRANSIT, wait for arrival first
        if nav_status == "IN_TRANSIT":
            arrival = ship['nav']['route']['arrival']
            destination = ship['nav']['route']['destination']['symbol']
            self.log(f"⏳ Ship in transit to {destination}, waiting for arrival before docking...")
            wait_time = calculate_arrival_wait_time(arrival)
            self._wait_for_arrival(wait_time + 3)
            self.log(f"✅ Arrived at {destination}, now docking...")

        self.log("🛬 Docking ship...")

        result = self.api.post(f"/my/ships/{self.ship_symbol}/dock")

        if result:
            self.log("✅ Ship docked")
            return True

        self.log("❌ Failed to dock", level="ERROR")
        return False

    def orbit(self) -> bool:
        """Put ship into orbit"""
        self.log("🛫 Entering orbit...")

        result = self.api.post(f"/my/ships/{self.ship_symbol}/orbit")

        if result:
            self.log("✅ Ship in orbit")
            return True

        self.log("❌ Failed to enter orbit", level="ERROR")
        return False

    def refuel(self, units: Optional[int] = None) -> bool:
        """
        Refuel ship to full or specified amount

        Args:
            units: Number of units to refuel (None = full tank)

        Returns:
            True if successful
        """
        ship = self.get_status()
        if not ship:
            return False

        fuel = ship['fuel']
        current = fuel['current']
        capacity = fuel['capacity']

        # Check if already full
        if current >= capacity * 0.95:
            self.log(f"✅ Fuel sufficient: {current}/{capacity}")
            return True

        # Ensure docked
        if ship['nav']['status'] != 'DOCKED':
            if not self.dock():
                return False

        # Calculate units to refuel
        if units is None:
            units = capacity - current

        self.log(f"⛽ Refueling {units} units...")

        result = self.api.post(f"/my/ships/{self.ship_symbol}/refuel", {"units": units})

        if result and 'data' in result:
            transaction = result['data']['transaction']
            new_fuel = result['data']['fuel']
            self.log(f"✅ Refueled: {new_fuel['current']}/{new_fuel['capacity']} (cost: {transaction['totalPrice']:,} credits)")
            return True

        self.log("❌ Failed to refuel", level="ERROR")
        return False

    # =============================================================================
    # NAVIGATION
    # =============================================================================

    def navigate(
        self,
        waypoint: str,
        flight_mode: Optional[str] = None,
        auto_refuel: bool = True
    ) -> bool:
        """
        Navigate to waypoint with intelligent fuel management

        Args:
            waypoint: Destination waypoint symbol
            flight_mode: CRUISE, DRIFT, BURN (auto-selected if None)
            auto_refuel: Automatically refuel if needed

        Returns:
            True if navigation successful
        """
        ship = self.get_status()
        if not ship:
            self.log("❌ Failed to get ship status", level="ERROR")
            return False

        current_location = ship['nav']['waypointSymbol']
        nav_status = ship['nav']['status']

        # Handle IN_TRANSIT state
        if nav_status == "IN_TRANSIT":
            destination = ship['nav']['route']['destination']['symbol']
            arrival = ship['nav']['route']['arrival']

            if destination == waypoint:
                self.log(f"⏳ Already in transit to {waypoint}")
                wait_time = calculate_arrival_wait_time(arrival)
                self._wait_for_arrival(wait_time + 3)
                self.log(f"✅ Arrived at {waypoint}")
                return True
            else:
                self.log(f"⏳ Waiting for arrival at {destination}...")
                wait_time = calculate_arrival_wait_time(arrival)
                self._wait_for_arrival(wait_time + 3)
                # Recursive call after arrival
                return self.navigate(waypoint, flight_mode, auto_refuel)

        # Already at destination
        if current_location == waypoint:
            self.log(f"✅ Already at {waypoint}")
            return True

        # Get coordinates for distance calculation
        current_system, _ = parse_waypoint_symbol(current_location)
        dest_system, _ = parse_waypoint_symbol(waypoint)

        current_wp = self.api.get_waypoint(current_system, current_location)
        dest_wp = self.api.get_waypoint(dest_system, waypoint)

        if not current_wp or not dest_wp:
            self.log("❌ Failed to get waypoint coordinates", level="ERROR")
            return False

        distance = calculate_distance(current_wp, dest_wp)
        self.log(f"📏 Distance to {waypoint}: {distance:.1f} units")

        # Select flight mode if not specified
        if flight_mode is None:
            fuel = ship['fuel']
            flight_mode = select_flight_mode(
                fuel['current'],
                fuel['capacity'],
                distance,
                require_return=False
            )

        self.log(f"✈️  Selected flight mode: {flight_mode}")

        # Check fuel and refuel if needed
        if auto_refuel:
            estimated_fuel = estimate_fuel_cost(distance, flight_mode)
            fuel = ship['fuel']
            fuel_percent = (fuel['current'] / fuel['capacity'] * 100) if fuel['capacity'] > 0 else 0

            # Refuel if fuel is low (<75%) OR if insufficient for journey
            if fuel_percent < 75 or fuel['current'] < estimated_fuel * 1.2:
                self.log(f"⚠️  Low fuel ({fuel['current']}/{fuel['capacity']} = {fuel_percent:.1f}%), refueling...", level="WARNING")
                if not self.refuel():
                    return False

        # Ensure in orbit
        if nav_status == "DOCKED":
            if not self.orbit():
                return False

        # Set flight mode
        mode_result = self.api.patch(f"/my/ships/{self.ship_symbol}/nav", {"flightMode": flight_mode})
        if not mode_result:
            self.log("❌ Failed to set flight mode", level="ERROR")
            return False

        # Navigate
        self.log(f"🚀 Navigating {current_location} → {waypoint} ({flight_mode})...")

        result = self.api.post(f"/my/ships/{self.ship_symbol}/navigate", {"waypointSymbol": waypoint})

        if not result or 'data' not in result:
            self.log("❌ Navigation failed", level="ERROR")
            return False

        nav_data = result['data']['nav']
        fuel_data = result['data']['fuel']
        arrival = nav_data['route']['arrival']

        self.log(f"⛽ Fuel consumed: {fuel_data['consumed']['amount']} (remaining: {fuel_data['current']}/{fuel_data['capacity']})")
        self.log(f"⏳ ETA: {arrival}")

        # Wait for arrival
        wait_time = calculate_arrival_wait_time(arrival)
        self._wait_for_arrival(wait_time + 3)

        self.log(f"✅ Arrived at {waypoint}")
        return True

    def _wait_for_arrival(self, seconds: int):
        """Wait for ship arrival with progress updates"""
        if seconds <= 0:
            return

        self.log(f"⏳ Waiting {seconds}s for arrival...")
        intervals = min(10, seconds)
        interval_time = seconds / intervals

        for i in range(1, intervals + 1):
            time.sleep(interval_time)
            if i % 3 == 0 or i == intervals:
                remaining = seconds - (i * interval_time)
                progress = (i / intervals) * 100
                self.log(f"  {progress:.0f}% complete ({remaining:.0f}s remaining)")

    # =============================================================================
    # MINING OPERATIONS
    # =============================================================================

    def extract(self) -> Optional[Dict[str, Any]]:
        """
        Extract resources at current location

        Returns:
            Dict with extraction data or None on failure
        """
        self.log("⛏️  Extracting resources...")

        result = self.api.post(f"/my/ships/{self.ship_symbol}/extract")

        if result and 'data' in result:
            extraction = result['data']['extraction']
            cargo = result['data']['cargo']
            cooldown = result['data']['cooldown']

            yield_data = extraction['yield']
            self.log(f"✅ Extracted: {yield_data['symbol']} x{yield_data['units']}")
            self.log(f"📦 Cargo: {cargo['units']}/{cargo['capacity']}")

            return {
                "symbol": yield_data['symbol'],
                "units": yield_data['units'],
                "cargo_units": cargo['units'],
                "cargo_capacity": cargo['capacity'],
                "cooldown": cooldown['remainingSeconds']
            }

        self.log("❌ Extraction failed")
        return None

    def wait_for_cooldown(self, seconds: int):
        """Wait for extraction cooldown"""
        if seconds <= 0:
            return

        self.log(f"⏳ Cooldown: {seconds}s...")
        intervals = min(8, seconds)
        interval_time = seconds / intervals

        for i in range(1, intervals + 1):
            time.sleep(interval_time)
            if i % 2 == 0 or i == intervals:
                remaining = seconds - (i * interval_time)
                self.log(f"  Cooldown {(i/intervals)*100:.0f}% ({remaining:.0f}s remaining)")

    # =============================================================================
    # CARGO OPERATIONS
    # =============================================================================

    def sell(self, symbol: str, units: int, max_per_transaction: int = None, check_market_prices: bool = False, min_acceptable_price: int = None) -> Optional[Dict[str, Any]]:
        """
        Sell cargo at current market with automatic batch handling and optional live price monitoring

        Args:
            symbol: Trade symbol to sell
            units: Number of units to sell
            max_per_transaction: Maximum units per transaction (handles market limits)
            check_market_prices: If True, check live market prices between batches and abort if price crashes
            min_acceptable_price: Minimum acceptable price per unit (abort if market price drops below this)

        Returns:
            Transaction data with aggregated totals or None on failure
        """
        if max_per_transaction and units > max_per_transaction:
            # Handle transaction limits by selling in batches with optional price monitoring
            self.log(f"💰 Selling {units} x {symbol} in batches (limit: {max_per_transaction}/transaction)...")
            total_sold = 0
            total_revenue = 0
            batches = (units + max_per_transaction - 1) // max_per_transaction

            for i in range(batches):
                # LIVE MARKET CHECK: Monitor prices between sell batches (if enabled)
                if check_market_prices and i > 0 and min_acceptable_price:  # Skip first batch
                    try:
                        ship = self.get_status()
                        if ship:
                            system = ship['nav']['systemSymbol']
                            waypoint = ship['nav']['waypointSymbol']

                            live_market = self.api.get_market(system, waypoint)
                            if live_market:
                                # Find current sell price (purchasePrice = what market pays us)
                                live_sell_price = None
                                for good in live_market.get('tradeGoods', []):
                                    if good['symbol'] == symbol:
                                        live_sell_price = good.get('purchasePrice')
                                        break

                                if live_sell_price:
                                    price_drop_pct = ((min_acceptable_price - live_sell_price) / min_acceptable_price) * 100 if min_acceptable_price > 0 else 0

                                    self.log(f"   📊 Batch {i+1}/{batches}: Current sell price: {live_sell_price:,} cr/unit (was {min_acceptable_price:,})")

                                    if price_drop_pct > 20:
                                        self.log(f"   ⚠️  Sell price dropped {price_drop_pct:.1f}% - OUR SALES MOVED THE MARKET!", level="WARNING")

                                    # ABORT if price dropped significantly
                                    if live_sell_price < min_acceptable_price * 0.7:  # Abort if <70% of acceptable price
                                        self.log("="*70, level="WARNING")
                                        self.log(f"🛑 ABORTING SALES - Market price collapsed!", level="WARNING")
                                        self.log("="*70, level="WARNING")
                                        self.log(f"  Price dropped: {min_acceptable_price:,} → {live_sell_price:,} (-{price_drop_pct:.1f}%)", level="WARNING")
                                        self.log(f"  Already sold: {total_sold}/{units} units", level="WARNING")
                                        self.log(f"  Remaining: {units - total_sold} units (will jettison or hold)", level="WARNING")
                                        self.log("="*70, level="WARNING")

                                        # Return what we've sold so far
                                        if total_sold > 0:
                                            avg_price = total_revenue // total_sold
                                            return {
                                                'units': total_sold,
                                                'tradeSymbol': symbol,
                                                'totalPrice': total_revenue,
                                                'pricePerUnit': avg_price,
                                                'aborted': True,
                                                'remaining_units': units - total_sold
                                            }
                                        return None

                                    # Update min_acceptable_price for next check
                                    min_acceptable_price = live_sell_price
                    except Exception as e:
                        self.log(f"   ⚠️  Live market check failed: {e}, continuing...", level="WARNING")

                batch_size = min(max_per_transaction, units - total_sold)
                result = self.api.post(f"/my/ships/{self.ship_symbol}/sell", {
                    "symbol": symbol,
                    "units": batch_size
                })

                if result and 'data' in result:
                    transaction = result['data']['transaction']
                    total_sold += transaction['units']
                    total_revenue += transaction['totalPrice']
                    self.log(f"   Batch {i+1}/{batches}: Sold {transaction['units']} @ {transaction['pricePerUnit']} = {transaction['totalPrice']:,} credits")
                    time.sleep(0.6)  # Rate limiting between batches
                else:
                    self.log(f"❌ Batch {i+1} failed")
                    if total_sold == 0:
                        return None
                    break

            # Return aggregated transaction data
            if total_sold > 0:
                avg_price = total_revenue // total_sold
                self.log(f"✅ Total sold: {total_sold} x {symbol} = {total_revenue:,} credits")
                return {
                    'units': total_sold,
                    'tradeSymbol': symbol,
                    'totalPrice': total_revenue,
                    'pricePerUnit': avg_price
                }
            return None
        else:
            # Single transaction
            self.log(f"💰 Selling {units} x {symbol}...")
            result = self.api.post(f"/my/ships/{self.ship_symbol}/sell", {
                "symbol": symbol,
                "units": units
            })

            # Check for successful transaction
            if result and 'data' in result:
                transaction = result['data']['transaction']
                self.log(f"✅ Sold {transaction['units']} x {transaction['tradeSymbol']} @ {transaction['pricePerUnit']} = {transaction['totalPrice']:,} credits")
                return transaction

            # Check if error is due to transaction limit
            if result and 'error' in result:
                error = result['error']
                error_code = error.get('code')
                error_message = error.get('message', '')

                if error_code == 4604 and 'limit' in error_message.lower():
                    # Extract limit from error message and retry
                    import re
                    match = re.search(r'limit of (\d+)', error_message)
                    if match:
                        limit = int(match.group(1))
                        self.log(f"⚠️  Market limit detected: {limit} units/transaction, retrying with batches...")
                        return self.sell(symbol, units, max_per_transaction=limit)

            self.log("❌ Sale failed")
            return None

    def sell_all(self) -> int:
        """
        Sell all cargo at current market

        Returns:
            Total revenue from sales
        """
        ship = self.get_status()
        if not ship:
            return 0

        cargo = ship['cargo']
        if cargo['units'] == 0:
            self.log("ℹ️  No cargo to sell")
            return 0

        total_revenue = 0
        self.log(f"💰 Selling {cargo['units']} units of cargo...")

        # Create a copy of the inventory list to avoid modifying during iteration
        inventory_copy = list(cargo['inventory'])
        for item in inventory_copy:
            transaction = self.sell(item['symbol'], item['units'])
            if transaction:
                total_revenue += transaction['totalPrice']
            time.sleep(0.6)  # Rate limiting

        self.log(f"💵 Total revenue: {total_revenue:,} credits")
        return total_revenue

    def buy(self, symbol: str, units: int) -> Optional[Dict[str, Any]]:
        """
        Purchase cargo at current market

        Args:
            symbol: Trade symbol to buy
            units: Number of units to buy

        Returns:
            Transaction data or None on failure
        """
        self.log(f"💰 Buying {units} x {symbol}...")

        result = self.api.post(f"/my/ships/{self.ship_symbol}/purchase", {
            "symbol": symbol,
            "units": units
        })

        if result and 'data' in result:
            transaction = result['data']['transaction']
            self.log(f"✅ Bought {transaction['units']} x {transaction['tradeSymbol']} @ {transaction['pricePerUnit']} = {transaction['totalPrice']:,} credits")
            return transaction

        self.log("❌ Purchase failed")
        return None

    def jettison(self, symbol: str, units: int) -> bool:
        """Jettison cargo into space"""
        self.log(f"🚮 Jettisoning {units} x {symbol}...")

        result = self.api.post(f"/my/ships/{self.ship_symbol}/jettison", {
            "symbol": symbol,
            "units": units
        })

        if result:
            self.log(f"✅ Jettisoned {units} x {symbol}")
            return True

        self.log("❌ Jettison failed")
        return False

    def jettison_wrong_cargo(self, target_resource: str, cargo_threshold: float = 0.8) -> Dict[str, int]:
        """
        Jettison non-target resources when cargo is filling up

        Args:
            target_resource: The resource we want to keep (e.g., "ALUMINUM_ORE")
            cargo_threshold: Jettison when cargo exceeds this percentage (default 80%)

        Returns:
            Dict with jettisoned items: {"IRON_ORE": 5, "COPPER_ORE": 3, ...}
        """
        ship = self.get_status()
        if not ship:
            return {}

        cargo = ship['cargo']
        cargo_percent = cargo['units'] / cargo['capacity'] if cargo['capacity'] > 0 else 0

        # Only jettison if cargo exceeds threshold
        if cargo_percent < cargo_threshold:
            return {}

        jettisoned = {}
        self.log(f"⚠️  Cargo at {cargo_percent*100:.1f}% - jettisoning non-target resources")

        # Jettison all non-target resources
        for item in list(cargo['inventory']):
            if item['symbol'] != target_resource:
                if self.jettison(item['symbol'], item['units']):
                    jettisoned[item['symbol']] = item['units']
                    self.log(f"🚮 Jettisoned {item['units']} x {item['symbol']} (keeping space for {target_resource})")
                time.sleep(0.6)  # Rate limiting

        if jettisoned:
            total_jettisoned = sum(jettisoned.values())
            self.log(f"📦 Freed {total_jettisoned} cargo units by jettisoning {len(jettisoned)} item types")

        return jettisoned
