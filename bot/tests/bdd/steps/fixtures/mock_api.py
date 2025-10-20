#!/usr/bin/env python3
"""
Mock API Client for testing

Simulates SpaceTraders API responses conforming to OpenAPI spec
"""

import json
from typing import Optional, Dict, Any, List
from datetime import datetime, timedelta, timezone


def _utc_now() -> datetime:
    return datetime.now(timezone.utc)


def _utc_now_iso() -> str:
    return _utc_now().isoformat()


def _utc_now_iso_z() -> str:
    return _to_iso_z(_utc_now())


def _expiration_iso_z(seconds: int) -> str:
    return _to_iso_z(_utc_now() + timedelta(seconds=seconds))


def _to_iso_z(dt: datetime) -> str:
    return dt.isoformat().replace('+00:00', 'Z')


class MockAPIClient:
    """
    Mock implementation of APIClient conforming to SpaceTraders OpenAPI spec

    Usage:
        mock_api = MockAPIClient()
        mock_api.set_ship_location("SHIP-1", "X1-HU87-A1")
        mock_api.set_ship_fuel("SHIP-1", 200, 400)

        ship = mock_api.get_ship("SHIP-1")
    """

    def __init__(self):
        self.ships = {}
        self.waypoints = {}
        self.markets = {}
        self.contracts = {}
        self.agent = {
            "accountId": "test-account",
            "symbol": "TEST_AGENT",
            "headquarters": "X1-HU87-A1",
            "credits": 100000,
            "startingFaction": "COSMIC"
        }

        # Track API calls for testing
        self.call_log = []

        # Endpoint to fail (for testing error scenarios)
        self.fail_endpoint = None

        # Individual failure flags for fine-grained control
        self.fail_next_get_ship = False
        self.fail_next_dock = False
        self.fail_next_orbit = False
        self.fail_next_refuel = False
        self.fail_next_patch = False
        self.fail_first_sell = False
        self.sell_call_count = 0

    def _log_call(self, method: str, endpoint: str, data: Any = None):
        """Log API call for verification in tests"""
        self.call_log.append({
            "method": method,
            "endpoint": endpoint,
            "data": data,
            "timestamp": _utc_now_iso()
        })

    # Setup methods for test scenarios

    def set_ship_location(self, ship_symbol: str, waypoint: str, status: str = "IN_ORBIT"):
        """Set ship location and status"""
        if ship_symbol not in self.ships:
            self.ships[ship_symbol] = self._create_default_ship(ship_symbol)

        self.ships[ship_symbol]["nav"]["waypointSymbol"] = waypoint
        self.ships[ship_symbol]["nav"]["status"] = status
        self.ships[ship_symbol]["nav"]["systemSymbol"] = waypoint.rsplit('-', 1)[0]

    def set_ship_fuel(self, ship_symbol: str, current: int, capacity: int):
        """Set ship fuel"""
        if ship_symbol not in self.ships:
            self.ships[ship_symbol] = self._create_default_ship(ship_symbol)

        self.ships[ship_symbol]["fuel"]["current"] = current
        self.ships[ship_symbol]["fuel"]["capacity"] = capacity

    def set_ship_cargo(self, ship_symbol: str, items: list, capacity: int = 40):
        """Set ship cargo"""
        if ship_symbol not in self.ships:
            self.ships[ship_symbol] = self._create_default_ship(ship_symbol)

        units = sum(item["units"] for item in items)
        self.ships[ship_symbol]["cargo"] = {
            "capacity": capacity,
            "units": units,
            "inventory": items
        }

    def set_ship_in_transit(self, ship_symbol: str, destination: str, arrival_seconds: int = 60):
        """Put ship in IN_TRANSIT state"""
        if ship_symbol not in self.ships:
            self.ships[ship_symbol] = self._create_default_ship(ship_symbol)

        departure_time = _utc_now()
        arrival_time = departure_time + timedelta(seconds=arrival_seconds)

        current_waypoint = self.ships[ship_symbol]["nav"]["waypointSymbol"]

        self.ships[ship_symbol]["nav"]["status"] = "IN_TRANSIT"
        self.ships[ship_symbol]["nav"]["route"] = {
            "destination": {
                "symbol": destination,
                "type": "PLANET",
                "systemSymbol": destination.rsplit('-', 1)[0],
                "x": 0,
                "y": 0
            },
            "departure": {
                "symbol": current_waypoint,
                "type": "PLANET",
                "systemSymbol": current_waypoint.rsplit('-', 1)[0],
                "x": 0,
                "y": 0
            },
            "departureTime": _to_iso_z(departure_time),
            "arrival": _to_iso_z(arrival_time)
        }

    def set_ship_cooldown(self, ship_symbol: str, seconds: int):
        """Set ship cooldown"""
        if ship_symbol not in self.ships:
            self.ships[ship_symbol] = self._create_default_ship(ship_symbol)

        self.ships[ship_symbol]["cooldown"] = {
            "shipSymbol": ship_symbol,
            "totalSeconds": seconds,
            "remainingSeconds": seconds,
            "expiration": _expiration_iso_z(seconds)
        }

    def add_waypoint(self, symbol: str, type: str = "PLANET", x: int = 0, y: int = 0, traits: list = None):
        """Add waypoint to system"""
        self.waypoints[symbol] = {
            "symbol": symbol,
            "type": type,
            "systemSymbol": symbol.rsplit('-', 1)[0],
            "x": x,
            "y": y,
            "orbitals": [],
            "traits": [{"symbol": t} for t in (traits or [])]
        }

    def add_market(self, waypoint: str, imports: list = None, exports: list = None):
        """Add market data"""
        self.markets[waypoint] = {
            "symbol": waypoint,
            "imports": [{"symbol": s} for s in (imports or [])],
            "exports": [{"symbol": s} for s in (exports or [])],
            "tradeGoods": []
        }

    def add_contract(self, contract_id: str, trade_symbol: str, units_required: int,
                    destination: str, accepted: bool = True, units_fulfilled: int = 0,
                    on_accepted: int = 10000, on_fulfilled: int = 50000):
        """Add contract data"""
        self.contracts[contract_id] = {
            "id": contract_id,
            "factionSymbol": "COSMIC",
            "type": "PROCUREMENT",
            "accepted": accepted,
            "fulfilled": False,
            "terms": {
                "deadline": "2025-12-31T00:00:00.000Z",
                "payment": {
                    "onAccepted": on_accepted,
                    "onFulfilled": on_fulfilled
                },
                "deliver": [
                    {
                        "tradeSymbol": trade_symbol,
                        "destinationSymbol": destination,
                        "unitsRequired": units_required,
                        "unitsFulfilled": units_fulfilled
                    }
                ]
            },
            "deadlineToAccept": "2025-12-15T00:00:00.000Z"
        }

    # API Client interface

    def get_agent(self) -> Dict:
        """Get agent data"""
        self._log_call("GET", "/my/agent")
        return {"data": self.agent}

    def get_ship(self, ship_symbol: str) -> Optional[Dict]:
        """Get ship data - conforms to GET /my/ships/{shipSymbol} response"""
        self._log_call("GET", f"/my/ships/{ship_symbol}")

        # Check for failure flag
        if self.fail_next_get_ship:
            self.fail_next_get_ship = False
            return None

        ship = self.ships.get(ship_symbol)
        if ship:
            # Auto-complete navigation if arrival time has passed
            if ship['nav']['status'] == 'IN_TRANSIT' and ship['nav'].get('route'):
                arrival_str = ship['nav']['route'].get('arrival')
                if arrival_str:
                    arrival_time = datetime.fromisoformat(arrival_str.replace('Z', '+00:00'))
                    if _utc_now() >= arrival_time:
                        # Navigation complete - update to destination
                        destination = ship['nav']['route']['destination']['symbol']
                        ship['nav']['waypointSymbol'] = destination
                        ship['nav']['status'] = 'IN_ORBIT'

        return ship if ship else None

    def get_waypoint(self, system: str, waypoint: str) -> Optional[Dict]:
        """Get waypoint data"""
        self._log_call("GET", f"/systems/{system}/waypoints/{waypoint}")
        return self.waypoints.get(waypoint)

    def get_contract(self, contract_id: str) -> Optional[Dict]:
        """Get contract details"""
        self._log_call("GET", f"/my/contracts/{contract_id}")
        contract = self.contracts.get(contract_id)
        return contract if contract else None

    def get_market(self, system: str, waypoint: str) -> Optional[Dict]:
        """Get market data"""
        self._log_call("GET", f"/systems/{system}/waypoints/{waypoint}/market")
        return self.markets.get(waypoint)

    def list_waypoints(self, system: str, limit: int = 20, page: int = 1, traits: str = None) -> Dict:
        """List waypoints in system"""
        self._log_call("GET", f"/systems/{system}/waypoints", {"limit": limit, "page": page, "traits": traits})

        matching = []
        for wp in self.waypoints.values():
            if wp["systemSymbol"] != system:
                continue

            if traits:
                wp_traits = [t["symbol"] for t in wp["traits"]]
                if traits not in wp_traits:
                    continue

            matching.append(wp)

        # Pagination
        start_idx = (page - 1) * limit
        end_idx = start_idx + limit
        page_data = matching[start_idx:end_idx]

        return {
            "data": page_data,
            "meta": {"total": len(matching), "page": page, "limit": limit}
        }

    def post(self, endpoint: str, data: Dict = None) -> Optional[Dict]:
        """POST request"""
        self._log_call("POST", endpoint, data)

        # Check if endpoint should fail
        if self.fail_endpoint and self.fail_endpoint in endpoint:
            return None

        # Navigate - conforms to POST /my/ships/{shipSymbol}/navigate response
        if "/navigate" in endpoint:
            ship_symbol = endpoint.split("/")[3]
            waypoint = data["waypointSymbol"]

            # Simulate navigation
            ship = self.ships[ship_symbol]
            current_wp = self.waypoints.get(ship["nav"]["waypointSymbol"])
            dest_wp = self.waypoints.get(waypoint)

            if not current_wp or not dest_wp:
                return None

            # Calculate distance
            distance = ((current_wp["x"] - dest_wp["x"])**2 +
                       (current_wp["y"] - dest_wp["y"])**2)**0.5

            # Calculate fuel cost based on flight mode
            flight_mode = ship["nav"].get("flightMode", "CRUISE")
            if flight_mode == "DRIFT":
                fuel_cost = max(1, int(distance / 300))  # DRIFT: ~1 fuel per 300 units
            elif flight_mode == "BURN":
                fuel_cost = int(distance * 2)  # BURN: 2x fuel cost
            else:  # CRUISE
                fuel_cost = int(distance)  # CRUISE: 1 fuel/unit

            if ship["fuel"]["current"] < fuel_cost:
                return None  # Not enough fuel

            # Update ship
            ship["fuel"]["current"] -= fuel_cost
            ship["nav"]["waypointSymbol"] = waypoint
            ship["nav"]["status"] = "IN_ORBIT"

            # Update route
            departure_time = _utc_now()
            ship["nav"]["route"] = {
                "destination": {
                    "symbol": waypoint,
                    "type": dest_wp["type"],
                    "systemSymbol": dest_wp["systemSymbol"],
                    "x": dest_wp["x"],
                    "y": dest_wp["y"]
                },
                "departure": {
                    "symbol": current_wp["symbol"],
                    "type": current_wp["type"],
                    "systemSymbol": current_wp["systemSymbol"],
                    "x": current_wp["x"],
                    "y": current_wp["y"]
                },
                "departureTime": departure_time.isoformat().replace('+00:00', 'Z'),
                "arrival": departure_time.isoformat().replace('+00:00', 'Z')
            }

            return {
                "data": {
                    "nav": ship["nav"],
                    "fuel": {
                        "current": ship["fuel"]["current"],
                        "capacity": ship["fuel"]["capacity"],
                        "consumed": {
                            "amount": fuel_cost,
                        "timestamp": departure_time.isoformat().replace('+00:00', 'Z')
                        }
                    },
                    "events": []  # No events in mock
                }
            }

        # Dock - conforms to POST /my/ships/{shipSymbol}/dock response
        elif "/dock" in endpoint:
            # Check for failure flag
            if self.fail_next_dock:
                self.fail_next_dock = False
                return None

            ship_symbol = endpoint.split("/")[3]
            self.ships[ship_symbol]["nav"]["status"] = "DOCKED"
            return {"data": {"nav": self.ships[ship_symbol]["nav"]}}

        # Orbit - conforms to POST /my/ships/{shipSymbol}/orbit response
        elif "/orbit" in endpoint:
            # Check for failure flag
            if self.fail_next_orbit:
                self.fail_next_orbit = False
                return None

            ship_symbol = endpoint.split("/")[3]
            self.ships[ship_symbol]["nav"]["status"] = "IN_ORBIT"
            return {"data": {"nav": self.ships[ship_symbol]["nav"]}}

        # Refuel - conforms to POST /my/ships/{shipSymbol}/refuel response
        elif "/refuel" in endpoint:
            # Check for failure flag
            if self.fail_next_refuel:
                self.fail_next_refuel = False
                return None

            ship_symbol = endpoint.split("/")[3]
            ship = self.ships[ship_symbol]

            units = data.get("units") if data else ship["fuel"]["capacity"] - ship["fuel"]["current"]
            cost = units * 10  # 10 credits per fuel

            # Validate credits
            if self.agent["credits"] < cost:
                return None  # Not enough credits

            ship["fuel"]["current"] = min(ship["fuel"]["current"] + units, ship["fuel"]["capacity"])
            self.agent["credits"] -= cost

            return {
                "data": {
                    "agent": self.agent,
                    "fuel": ship["fuel"],
                    "transaction": {
                        "waypointSymbol": ship["nav"]["waypointSymbol"],
                        "shipSymbol": ship_symbol,
                        "tradeSymbol": "FUEL",
                        "type": "PURCHASE",
                        "units": units,
                        "pricePerUnit": 10,
                        "totalPrice": cost,
                        "timestamp": _utc_now_iso_z()
                    }
                }
            }

        # Buy
        elif "/purchase" in endpoint:
            ship_symbol = endpoint.split("/")[3]
            symbol = data["symbol"]
            units = data["units"]
            ship = self.ships[ship_symbol]

            # Validate cargo capacity
            if ship["cargo"]["units"] + units > ship["cargo"]["capacity"]:
                return None  # Not enough cargo space

            # Validate credits
            cost = units * 50  # 50 credits per unit
            if self.agent["credits"] < cost:
                return None  # Not enough credits

            # Add to cargo
            found = False
            for item in ship["cargo"]["inventory"]:
                if item["symbol"] == symbol:
                    item["units"] += units
                    found = True
                    break

            if not found:
                ship["cargo"]["inventory"].append({"symbol": symbol, "units": units})

            ship["cargo"]["units"] += units
            self.agent["credits"] -= cost

            return {
                "data": {
                    "agent": self.agent,
                    "cargo": ship["cargo"],
                    "transaction": {
                        "waypointSymbol": ship["nav"]["waypointSymbol"],
                        "shipSymbol": ship_symbol,
                        "tradeSymbol": symbol,
                        "type": "PURCHASE",
                        "units": units,
                        "pricePerUnit": 50,
                        "totalPrice": cost,
                        "timestamp": _utc_now_iso_z()
                    }
                }
            }

        # Sell
        elif "/sell" in endpoint:
            # Check for failure flag on first sell
            if self.fail_first_sell and self.sell_call_count == 0:
                self.sell_call_count += 1
                return None

            self.sell_call_count += 1

            ship_symbol = endpoint.split("/")[3]
            symbol = data["symbol"]
            units = data["units"]
            ship = self.ships[ship_symbol]

            # Validate item exists in cargo
            item_found = None
            for item in ship["cargo"]["inventory"]:
                if item["symbol"] == symbol:
                    item_found = item
                    break

            if not item_found:
                return None  # Item not in cargo

            # Validate sufficient units
            if item_found["units"] < units:
                return None  # Not enough units

            # Remove from cargo
            item_found["units"] -= units
            if item_found["units"] <= 0:
                ship["cargo"]["inventory"].remove(item_found)

            ship["cargo"]["units"] -= units

            revenue = units * 70  # 70 credits per unit
            self.agent["credits"] += revenue

            return {
                "data": {
                    "agent": self.agent,
                    "cargo": ship["cargo"],
                    "transaction": {
                        "waypointSymbol": ship["nav"]["waypointSymbol"],
                        "shipSymbol": ship_symbol,
                        "tradeSymbol": symbol,
                        "type": "SELL",
                        "units": units,
                        "pricePerUnit": 70,
                        "totalPrice": revenue,
                        "timestamp": _utc_now_iso_z()
                    }
                }
            }

        # Jettison
        elif "/jettison" in endpoint:
            ship_symbol = endpoint.split("/")[3]
            symbol = data["symbol"]
            units = data["units"]
            ship = self.ships[ship_symbol]

            # Validate item exists in cargo
            item_found = None
            for item in ship["cargo"]["inventory"]:
                if item["symbol"] == symbol:
                    item_found = item
                    break

            if not item_found:
                return None  # Item not in cargo

            # Validate sufficient units
            if item_found["units"] < units:
                return None  # Not enough units

            # Remove from cargo
            item_found["units"] -= units
            if item_found["units"] <= 0:
                ship["cargo"]["inventory"].remove(item_found)

            ship["cargo"]["units"] -= units

            return {
                "data": {
                    "cargo": ship["cargo"]
                }
            }

        # Accept contract
        elif "/accept" in endpoint and "/contracts/" in endpoint:
            contract_id = endpoint.split("/")[3]
            contract = self.contracts.get(contract_id)

            if not contract:
                return None

            contract["accepted"] = True

            # Award acceptance payment
            acceptance_payment = contract["terms"]["payment"]["onAccepted"]
            self.agent["credits"] += acceptance_payment

            return {
                "data": {
                    "contract": contract,
                    "agent": self.agent
                }
            }

        # Deliver to contract
        elif "/deliver" in endpoint and "/contracts/" in endpoint:
            contract_id = endpoint.split("/")[3]
            contract = self.contracts.get(contract_id)

            if not contract:
                return None

            ship_symbol = data["shipSymbol"]
            trade_symbol = data["tradeSymbol"]
            units = data["units"]

            ship = self.ships.get(ship_symbol)
            if not ship:
                return None

            # Verify ship has the cargo
            item_found = None
            for item in ship["cargo"]["inventory"]:
                if item["symbol"] == trade_symbol:
                    item_found = item
                    break

            if not item_found or item_found["units"] < units:
                return None

            # Remove from cargo
            item_found["units"] -= units
            if item_found["units"] <= 0:
                ship["cargo"]["inventory"].remove(item_found)
            ship["cargo"]["units"] -= units

            # Update contract fulfillment
            delivery = contract["terms"]["deliver"][0]
            delivery["unitsFulfilled"] += units

            return {
                "data": {
                    "contract": contract,
                    "cargo": ship["cargo"]
                }
            }

        # Fulfill contract
        elif "/fulfill" in endpoint and "/contracts/" in endpoint:
            contract_id = endpoint.split("/")[3]
            contract = self.contracts.get(contract_id)

            if not contract:
                return None

            # Verify all deliveries fulfilled
            for delivery in contract["terms"]["deliver"]:
                if delivery["unitsFulfilled"] < delivery["unitsRequired"]:
                    return None  # Not fully fulfilled

            contract["fulfilled"] = True

            # Award completion payment
            completion_payment = contract["terms"]["payment"]["onFulfilled"]
            self.agent["credits"] += completion_payment

            return {
                "data": {
                    "contract": contract,
                    "agent": self.agent
                }
            }

        # Extract - conforms to POST /my/ships/{shipSymbol}/extract response
        elif "/extract" in endpoint:
            ship_symbol = endpoint.split("/")[3]
            ship = self.ships[ship_symbol]

            # Check if ship is in orbit
            if ship["nav"]["status"] != "IN_ORBIT":
                return None  # Must be in orbit to extract

            # Check cooldown
            cooldown = ship["cooldown"]
            if cooldown["remainingSeconds"] > 0:
                return None  # Cooldown active

            # Check cargo capacity
            if ship["cargo"]["units"] >= ship["cargo"]["capacity"]:
                return None  # Cargo full

            # Get waypoint to check if extraction is allowed
            current_waypoint = ship["nav"]["waypointSymbol"]
            waypoint = self.waypoints.get(current_waypoint)

            if not waypoint:
                return None  # Invalid waypoint

            # Check if waypoint allows extraction
            traits = [t["symbol"] for t in waypoint["traits"]]
            extractable_traits = ["COMMON_METAL_DEPOSITS", "PRECIOUS_METAL_DEPOSITS",
                                 "RARE_METAL_DEPOSITS", "MINERAL_DEPOSITS", "STRIPPED"]

            if not any(t in traits for t in extractable_traits):
                return None  # Cannot extract at this location

            # Determine extracted resource based on traits
            import random
            if "STRIPPED" in traits:
                symbol = "ICE_WATER"
                units = 1  # Poor yields from stripped asteroids
            elif "COMMON_METAL_DEPOSITS" in traits:
                symbol = "IRON_ORE"
                units = random.randint(2, 7)
            elif "PRECIOUS_METAL_DEPOSITS" in traits:
                symbol = "GOLD_ORE"
                units = random.randint(1, 5)
            elif "RARE_METAL_DEPOSITS" in traits:
                symbol = "PLATINUM_ORE"
                units = random.randint(1, 3)
            else:
                symbol = "SILICON_CRYSTALS"
                units = random.randint(2, 6)

            # Add to cargo
            found = False
            for item in ship["cargo"]["inventory"]:
                if item["symbol"] == symbol:
                    item["units"] += units
                    found = True
                    break

            if not found:
                ship["cargo"]["inventory"].append({"symbol": symbol, "units": units})

            ship["cargo"]["units"] += units

            # Set cooldown (80 seconds standard)
            cooldown_seconds = 80
            expiration = _utc_now() + timedelta(seconds=cooldown_seconds)
            ship["cooldown"] = {
                "shipSymbol": ship_symbol,
                "totalSeconds": cooldown_seconds,
                "remainingSeconds": cooldown_seconds,
                "expiration": expiration.isoformat().replace('+00:00', 'Z')
            }

            return {
                "data": {
                    "cooldown": ship["cooldown"],
                    "extraction": {
                        "shipSymbol": ship_symbol,
                        "yield": {
                            "symbol": symbol,
                            "units": units
                        }
                    },
                    "cargo": ship["cargo"],
                    "events": []
                }
            }

        return None

    def patch(self, endpoint: str, data: Dict = None) -> Optional[Dict]:
        """PATCH request"""
        self._log_call("PATCH", endpoint, data)

        # Check for failure flag
        if self.fail_next_patch:
            self.fail_next_patch = False
            return None

        # Set flight mode
        if "/nav" in endpoint:
            ship_symbol = endpoint.split("/")[3]
            ship = self.ships[ship_symbol]
            ship["nav"]["flightMode"] = data["flightMode"]
            return {"data": ship["nav"]}

        return None

    def _create_default_ship(self, ship_symbol: str) -> Dict:
        """Create default ship data - conforms to Ship schema"""
        return {
            "symbol": ship_symbol,
            "registration": {
                "name": ship_symbol,
                "factionSymbol": "COSMIC",
                "role": "COMMAND"
            },
            "nav": {
                "systemSymbol": "X1-HU87",
                "waypointSymbol": "X1-HU87-A1",
                "route": {
                    "destination": {
                        "symbol": "X1-HU87-A1",
                        "type": "PLANET",
                        "systemSymbol": "X1-HU87",
                        "x": 0,
                        "y": 0
                    },
                    "departure": {
                        "symbol": "X1-HU87-A1",
                        "type": "PLANET",
                        "systemSymbol": "X1-HU87",
                        "x": 0,
                        "y": 0
                    },
                    "departureTime": _utc_now_iso_z(),
                    "arrival": _utc_now_iso_z()
                },
                "status": "IN_ORBIT",
                "flightMode": "CRUISE"
            },
            "crew": {
                "current": 50,
                "required": 50,
                "capacity": 80,
                "rotation": "STRICT",
                "morale": 100,
                "wages": 0
            },
            "frame": {
                "symbol": "FRAME_FRIGATE",
                "name": "Frame Frigate",
                "description": "A medium-sized, multi-purpose spacecraft",
                "condition": 1.0,  # 1.0 = 100% condition
                "moduleSlots": 8,
                "mountingPoints": 5,
                "fuelCapacity": 400,
                "requirements": {
                    "power": 8,
                    "crew": 25,
                    "slots": 8
                }
            },
            "reactor": {
                "symbol": "REACTOR_SOLAR_I",
                "name": "Solar Reactor I",
                "description": "Basic solar power reactor",
                "condition": 1.0,
                "powerOutput": 31,
                "requirements": {
                    "power": 0,
                    "crew": 0,
                    "slots": 1
                }
            },
            "engine": {
                "symbol": "ENGINE_IMPULSE_DRIVE_I",
                "name": "Impulse Drive I",
                "description": "Basic impulse drive",
                "condition": 1.0,
                "speed": 30,
                "requirements": {
                    "power": 8,
                    "crew": 8,
                    "slots": 2
                }
            },
            "modules": [
                {
                    "symbol": "MODULE_CARGO_HOLD_I",
                    "capacity": 40,
                    "range": 0,
                    "name": "Cargo Hold",
                    "description": "A module that increases a ship's cargo capacity.",
                    "requirements": {
                        "power": 1,
                        "crew": 0,
                        "slots": 1
                    }
                }
            ],
            "mounts": [],
            "cargo": {
                "capacity": 40,
                "units": 0,
                "inventory": []
            },
            "fuel": {
                "current": 400,
                "capacity": 400,
                "consumed": {
                    "amount": 0,
                    "timestamp": _utc_now_iso_z()
                }
            },
            "cooldown": {
                "shipSymbol": ship_symbol,
                "totalSeconds": 0,
                "remainingSeconds": 0,
                "expiration": _utc_now_iso_z()
            }
        }

    def reset(self):
        """Reset all mock data"""
        self.ships = {}
        self.waypoints = {}
        self.markets = {}
        self.contracts = {}
        self.call_log = []
        self.agent["credits"] = 100000
        self.fail_endpoint = None
        self.fail_next_get_ship = False
        self.fail_next_dock = False
        self.fail_next_orbit = False
        self.fail_next_refuel = False
        self.fail_next_patch = False
        self.fail_first_sell = False
        self.sell_call_count = 0
