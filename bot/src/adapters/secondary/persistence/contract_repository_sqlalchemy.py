"""SQLAlchemy-based ContractRepository implementation."""

import json
from typing import Optional, List
from datetime import datetime, timezone
from sqlalchemy import select
from sqlalchemy.engine import Engine
from sqlalchemy.dialects.sqlite import insert as sqlite_insert
from sqlalchemy.dialects.postgresql import insert as pg_insert

from ports.outbound.repositories import IContractRepository
from domain.shared.contract import Contract, ContractTerms, Delivery, Payment
from domain.shared.value_objects import Waypoint
from .models import contracts
from .mappers import _parse_datetime


class ContractRepositorySQLAlchemy(IContractRepository):
    """SQLAlchemy implementation of contract repository"""

    def __init__(self, engine: Engine):
        """
        Initialize contract repository.

        Args:
            engine: SQLAlchemy Engine instance
        """
        self._engine = engine

    def save(self, contract: Contract, player_id: int) -> None:
        """
        Save or update contract.

        Args:
            contract: Contract entity to persist
            player_id: Owning player's ID
        """
        with self._engine.begin() as conn:
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

            # Detect backend
            backend = conn.engine.dialect.name

            # Use dialect-specific UPSERT
            if backend == 'postgresql':
                stmt = pg_insert(contracts).values(
                    contract_id=contract.contract_id,
                    player_id=player_id,
                    faction_symbol=contract.faction_symbol,
                    type=contract.type,
                    accepted=contract.accepted,
                    fulfilled=contract.fulfilled,
                    deadline_to_accept=contract.deadline_to_accept.isoformat(),
                    deadline=contract.terms.deadline.isoformat(),
                    payment_on_accepted=contract.terms.payment.on_accepted,
                    payment_on_fulfilled=contract.terms.payment.on_fulfilled,
                    deliveries_json=deliveries_json,
                    last_updated=datetime.now(timezone.utc).isoformat()
                )
                stmt = stmt.on_conflict_do_update(
                    index_elements=['contract_id', 'player_id'],
                    set_={
                        'faction_symbol': stmt.excluded.faction_symbol,
                        'type': stmt.excluded.type,
                        'accepted': stmt.excluded.accepted,
                        'fulfilled': stmt.excluded.fulfilled,
                        'deadline_to_accept': stmt.excluded.deadline_to_accept,
                        'deadline': stmt.excluded.deadline,
                        'payment_on_accepted': stmt.excluded.payment_on_accepted,
                        'payment_on_fulfilled': stmt.excluded.payment_on_fulfilled,
                        'deliveries_json': stmt.excluded.deliveries_json,
                        'last_updated': stmt.excluded.last_updated
                    }
                )
            else:
                stmt = sqlite_insert(contracts).values(
                    contract_id=contract.contract_id,
                    player_id=player_id,
                    faction_symbol=contract.faction_symbol,
                    type=contract.type,
                    accepted=contract.accepted,
                    fulfilled=contract.fulfilled,
                    deadline_to_accept=contract.deadline_to_accept.isoformat(),
                    deadline=contract.terms.deadline.isoformat(),
                    payment_on_accepted=contract.terms.payment.on_accepted,
                    payment_on_fulfilled=contract.terms.payment.on_fulfilled,
                    deliveries_json=deliveries_json,
                    last_updated=datetime.now(timezone.utc).isoformat()
                )
                stmt = stmt.on_conflict_do_update(
                    index_elements=['contract_id', 'player_id'],
                    set_={
                        'faction_symbol': stmt.excluded.faction_symbol,
                        'type': stmt.excluded.type,
                        'accepted': stmt.excluded.accepted,
                        'fulfilled': stmt.excluded.fulfilled,
                        'deadline_to_accept': stmt.excluded.deadline_to_accept,
                        'deadline': stmt.excluded.deadline,
                        'payment_on_accepted': stmt.excluded.payment_on_accepted,
                        'payment_on_fulfilled': stmt.excluded.payment_on_fulfilled,
                        'deliveries_json': stmt.excluded.deliveries_json,
                        'last_updated': stmt.excluded.last_updated
                    }
                )

            conn.execute(stmt)

    def find_by_id(self, contract_id: str, player_id: int) -> Optional[Contract]:
        """
        Find contract by ID.

        Args:
            contract_id: Unique contract identifier
            player_id: Owning player's ID

        Returns:
            Contract if found, None otherwise
        """
        with self._engine.connect() as conn:
            stmt = (
                select(
                    contracts.c.contract_id,
                    contracts.c.faction_symbol,
                    contracts.c.type,
                    contracts.c.accepted,
                    contracts.c.fulfilled,
                    contracts.c.deadline_to_accept,
                    contracts.c.deadline,
                    contracts.c.payment_on_accepted,
                    contracts.c.payment_on_fulfilled,
                    contracts.c.deliveries_json
                )
                .where(
                    contracts.c.contract_id == contract_id,
                    contracts.c.player_id == player_id
                )
            )

            result = conn.execute(stmt)
            row = result.fetchone()

            if not row:
                return None

            return self._map_to_domain(row)

    def find_all(self, player_id: int) -> List[Contract]:
        """
        Find all contracts for a player.

        Args:
            player_id: Player's ID

        Returns:
            List of contracts (empty if none found)
        """
        with self._engine.connect() as conn:
            stmt = (
                select(
                    contracts.c.contract_id,
                    contracts.c.faction_symbol,
                    contracts.c.type,
                    contracts.c.accepted,
                    contracts.c.fulfilled,
                    contracts.c.deadline_to_accept,
                    contracts.c.deadline,
                    contracts.c.payment_on_accepted,
                    contracts.c.payment_on_fulfilled,
                    contracts.c.deliveries_json
                )
                .where(contracts.c.player_id == player_id)
                .order_by(contracts.c.deadline.desc())
            )

            result = conn.execute(stmt)
            rows = result.fetchall()

            return [self._map_to_domain(row) for row in rows]

    def find_active(self, player_id: int) -> List[Contract]:
        """
        Find active (accepted but not fulfilled) contracts.

        Args:
            player_id: Player's ID

        Returns:
            List of active contracts
        """
        with self._engine.connect() as conn:
            stmt = (
                select(
                    contracts.c.contract_id,
                    contracts.c.faction_symbol,
                    contracts.c.type,
                    contracts.c.accepted,
                    contracts.c.fulfilled,
                    contracts.c.deadline_to_accept,
                    contracts.c.deadline,
                    contracts.c.payment_on_accepted,
                    contracts.c.payment_on_fulfilled,
                    contracts.c.deliveries_json
                )
                .where(
                    contracts.c.player_id == player_id,
                    contracts.c.accepted == True,
                    contracts.c.fulfilled == False
                )
                .order_by(contracts.c.deadline.asc())
            )

            result = conn.execute(stmt)
            rows = result.fetchall()

            return [self._map_to_domain(row) for row in rows]

    def _map_to_domain(self, row) -> Contract:
        """
        Map database row to Contract domain entity.

        Args:
            row: Database row

        Returns:
            Contract domain entity
        """
        # Parse deliveries from JSON
        deliveries_data = json.loads(row.deliveries_json)
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
            on_accepted=row.payment_on_accepted,
            on_fulfilled=row.payment_on_fulfilled
        )

        # Create terms
        terms = ContractTerms(
            deadline=_parse_datetime(row.deadline),
            payment=payment,
            deliveries=deliveries
        )

        # Create contract
        return Contract(
            contract_id=row.contract_id,
            faction_symbol=row.faction_symbol,
            type=row.type,
            terms=terms,
            accepted=bool(row.accepted),
            fulfilled=bool(row.fulfilled),
            deadline_to_accept=_parse_datetime(row.deadline_to_accept)
        )
