"""Contract repository implementation"""
import json
from typing import Optional, List
from datetime import datetime, timezone

from ports.outbound.repositories import IContractRepository
from domain.shared.contract import Contract, ContractTerms, Delivery, Payment
from domain.shared.value_objects import Waypoint
from .database import Database


class ContractRepository(IContractRepository):
    """SQLite implementation of contract repository"""

    def __init__(self, database: Database):
        self._db = database

    def save(self, contract: Contract, player_id: int) -> None:
        """Save or update contract"""
        with self._db.transaction() as conn:
            # Serialize deliveries to JSON
            deliveries_json = json.dumps([
                {
                    'trade_symbol': d.trade_symbol,
                    'destination': {
                        'symbol': d.destination.symbol,
                        'x': d.destination.x,
                        'y': d.destination.y
                    },
                    'units_required': d.units_required,
                    'units_fulfilled': d.units_fulfilled
                }
                for d in contract.terms.deliveries
            ])

            conn.execute("""
                INSERT INTO contracts (
                    contract_id, player_id, faction_symbol, type,
                    accepted, fulfilled, deadline_to_accept, deadline,
                    payment_on_accepted, payment_on_fulfilled,
                    deliveries_json, last_updated
                )
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(contract_id, player_id) DO UPDATE SET
                    faction_symbol = excluded.faction_symbol,
                    type = excluded.type,
                    accepted = excluded.accepted,
                    fulfilled = excluded.fulfilled,
                    deadline_to_accept = excluded.deadline_to_accept,
                    deadline = excluded.deadline,
                    payment_on_accepted = excluded.payment_on_accepted,
                    payment_on_fulfilled = excluded.payment_on_fulfilled,
                    deliveries_json = excluded.deliveries_json,
                    last_updated = excluded.last_updated
            """, (
                contract.contract_id,
                player_id,
                contract.faction_symbol,
                contract.type,
                contract.accepted,
                contract.fulfilled,
                contract.deadline_to_accept.isoformat(),
                contract.terms.deadline.isoformat(),
                contract.terms.payment.on_accepted,
                contract.terms.payment.on_fulfilled,
                deliveries_json,
                datetime.now(timezone.utc).isoformat()
            ))

    def find_by_id(self, contract_id: str, player_id: int) -> Optional[Contract]:
        """Find contract by ID"""
        with self._db.connection() as conn:
            cursor = conn.execute("""
                SELECT contract_id, faction_symbol, type,
                       accepted, fulfilled, deadline_to_accept, deadline,
                       payment_on_accepted, payment_on_fulfilled,
                       deliveries_json
                FROM contracts
                WHERE contract_id = ? AND player_id = ?
            """, (contract_id, player_id))

            row = cursor.fetchone()
            if not row:
                return None

            return self._map_to_domain(row)

    def find_all(self, player_id: int) -> List[Contract]:
        """Find all contracts for a player"""
        with self._db.connection() as conn:
            cursor = conn.execute("""
                SELECT contract_id, faction_symbol, type,
                       accepted, fulfilled, deadline_to_accept, deadline,
                       payment_on_accepted, payment_on_fulfilled,
                       deliveries_json
                FROM contracts
                WHERE player_id = ?
                ORDER BY deadline DESC
            """, (player_id,))

            rows = cursor.fetchall()
            return [self._map_to_domain(row) for row in rows]

    def find_active(self, player_id: int) -> List[Contract]:
        """Find active (accepted but not fulfilled) contracts"""
        with self._db.connection() as conn:
            cursor = conn.execute("""
                SELECT contract_id, faction_symbol, type,
                       accepted, fulfilled, deadline_to_accept, deadline,
                       payment_on_accepted, payment_on_fulfilled,
                       deliveries_json
                FROM contracts
                WHERE player_id = ?
                  AND accepted = 1
                  AND fulfilled = 0
                ORDER BY deadline ASC
            """, (player_id,))

            rows = cursor.fetchall()
            return [self._map_to_domain(row) for row in rows]

    def _map_to_domain(self, row) -> Contract:
        """Map database row to Contract domain entity"""
        # Parse deliveries from JSON
        deliveries_data = json.loads(row['deliveries_json'])
        deliveries = [
            Delivery(
                trade_symbol=d['trade_symbol'],
                destination=Waypoint(
                    symbol=d['destination']['symbol'],
                    x=d['destination']['x'],
                    y=d['destination']['y']
                ),
                units_required=d['units_required'],
                units_fulfilled=d['units_fulfilled']
            )
            for d in deliveries_data
        ]

        # Create payment
        payment = Payment(
            on_accepted=row['payment_on_accepted'],
            on_fulfilled=row['payment_on_fulfilled']
        )

        # Create terms
        terms = ContractTerms(
            deadline=datetime.fromisoformat(row['deadline']),
            payment=payment,
            deliveries=deliveries
        )

        # Create contract
        return Contract(
            contract_id=row['contract_id'],
            faction_symbol=row['faction_symbol'],
            type=row['type'],
            terms=terms,
            accepted=bool(row['accepted']),
            fulfilled=bool(row['fulfilled']),
            deadline_to_accept=datetime.fromisoformat(row['deadline_to_accept'])
        )
