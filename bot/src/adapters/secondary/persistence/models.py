"""SQLAlchemy table definitions for SpaceTraders bot database.

This module defines all database tables using SQLAlchemy Core (NOT ORM).
Tables are defined as metadata for schema generation and query building.
"""

from sqlalchemy import (
    MetaData,
    Table,
    Column,
    Integer,
    String,
    Text,
    Float,
    Boolean,
    DateTime,
    ForeignKey,
    Index,
    JSON,
    PrimaryKeyConstraint,
)
from datetime import datetime, timezone

# MetaData object for all table definitions
metadata = MetaData()

# Players table
players = Table(
    'players',
    metadata,
    Column('player_id', Integer, primary_key=True, autoincrement=True),
    Column('agent_symbol', String, unique=True, nullable=False),
    Column('token', String, nullable=False),
    Column('created_at', DateTime(timezone=True), nullable=False),
    Column('last_active', DateTime(timezone=True)),
    Column('metadata', JSON),
    Column('credits', Integer, default=0),
)

# Create index on agent_symbol
Index('idx_player_agent', players.c.agent_symbol)

# System graphs table (shared across all players)
system_graphs = Table(
    'system_graphs',
    metadata,
    Column('system_symbol', String, primary_key=True),
    Column('graph_data', Text, nullable=False),
    Column('last_updated', DateTime(timezone=True), nullable=False, default=lambda: datetime.now(timezone.utc)),
)

# Ship assignments table (for container system)
ship_assignments = Table(
    'ship_assignments',
    metadata,
    Column('ship_symbol', String, nullable=False),
    Column('player_id', Integer, ForeignKey('players.player_id', ondelete='CASCADE'), nullable=False),
    Column('container_id', String),
    Column('operation', String),
    Column('status', String, default='idle'),
    Column('assigned_at', DateTime(timezone=True)),
    Column('released_at', DateTime(timezone=True)),
    Column('release_reason', String),
    PrimaryKeyConstraint('ship_symbol', 'player_id'),
)

# Containers table (for daemon system)
containers = Table(
    'containers',
    metadata,
    Column('container_id', String, nullable=False),
    Column('player_id', Integer, ForeignKey('players.player_id', ondelete='CASCADE'), nullable=False),
    Column('container_type', String),
    Column('command_type', String),
    Column('status', String),
    Column('restart_policy', String),
    Column('restart_count', Integer, default=0),
    Column('config', Text),
    Column('started_at', DateTime(timezone=True)),
    Column('stopped_at', DateTime(timezone=True)),
    Column('exit_code', Integer),
    Column('exit_reason', String),
    PrimaryKeyConstraint('container_id', 'player_id'),
)

# Container logs table
container_logs = Table(
    'container_logs',
    metadata,
    Column('log_id', Integer, primary_key=True, autoincrement=True),
    Column('container_id', String, nullable=False),
    Column('player_id', Integer, ForeignKey('players.player_id', ondelete='CASCADE'), nullable=False),
    Column('timestamp', DateTime(timezone=True), nullable=False),
    Column('level', String, nullable=False, default='INFO'),
    Column('message', Text, nullable=False),
)

# Indexes for container_logs
Index('idx_container_logs_container_time', container_logs.c.container_id, container_logs.c.timestamp.desc())
Index('idx_container_logs_timestamp', container_logs.c.timestamp.desc())
Index('idx_container_logs_level', container_logs.c.level, container_logs.c.timestamp.desc())

# Market data table
market_data = Table(
    'market_data',
    metadata,
    Column('waypoint_symbol', String, nullable=False),
    Column('good_symbol', String, nullable=False),
    Column('supply', String),
    Column('activity', String),
    Column('purchase_price', Integer, nullable=False),
    Column('sell_price', Integer, nullable=False),
    Column('trade_volume', Integer, nullable=False),
    Column('last_updated', String, nullable=False),  # ISO timestamp string
    Column('player_id', Integer, ForeignKey('players.player_id', ondelete='CASCADE'), nullable=False),
    PrimaryKeyConstraint('waypoint_symbol', 'good_symbol'),
)

# Indexes for market_data
Index('idx_market_data_waypoint', market_data.c.waypoint_symbol)
Index('idx_market_data_updated', market_data.c.last_updated.desc())
Index('idx_market_data_player', market_data.c.player_id)

# Contracts table
contracts = Table(
    'contracts',
    metadata,
    Column('contract_id', String, nullable=False),
    Column('player_id', Integer, ForeignKey('players.player_id', ondelete='CASCADE'), nullable=False),
    Column('faction_symbol', String, nullable=False),
    Column('type', String, nullable=False),
    Column('accepted', Boolean, nullable=False),
    Column('fulfilled', Boolean, nullable=False),
    Column('deadline_to_accept', DateTime(timezone=True), nullable=False),
    Column('deadline', DateTime(timezone=True), nullable=False),
    Column('payment_on_accepted', Integer, nullable=False),
    Column('payment_on_fulfilled', Integer, nullable=False),
    Column('deliveries_json', Text, nullable=False),
    Column('last_updated', DateTime(timezone=True), nullable=False),
    PrimaryKeyConstraint('contract_id', 'player_id'),
)

# Indexes for contracts
Index('idx_contracts_player', contracts.c.player_id)
Index('idx_contracts_active', contracts.c.player_id, contracts.c.accepted, contracts.c.fulfilled)

# Waypoints table (cached waypoint data for shipyard/market discovery)
waypoints = Table(
    'waypoints',
    metadata,
    Column('waypoint_symbol', String, primary_key=True),
    Column('system_symbol', String, nullable=False),
    Column('type', String, nullable=False),
    Column('x', Float, nullable=False),
    Column('y', Float, nullable=False),
    Column('traits', Text),
    Column('has_fuel', Integer, nullable=False, default=0),
    Column('orbitals', Text),
    Column('synced_at', DateTime(timezone=True)),
)

# Indexes for waypoints
Index('idx_waypoint_system', waypoints.c.system_symbol)
Index('idx_waypoint_fuel', waypoints.c.has_fuel)

# Captain logs table (narrative mission logs from TARS AI captain)
captain_logs = Table(
    'captain_logs',
    metadata,
    Column('log_id', Integer, primary_key=True, autoincrement=True),
    Column('player_id', Integer, ForeignKey('players.player_id', ondelete='CASCADE'), nullable=False),
    Column('timestamp', String, nullable=False),  # ISO timestamp string
    Column('entry_type', String, nullable=False),
    Column('narrative', Text, nullable=False),
    Column('event_data', Text),
    Column('tags', Text),
    Column('fleet_snapshot', Text),
)

# Indexes for captain_logs
Index('idx_captain_logs_player_time', captain_logs.c.player_id, captain_logs.c.timestamp.desc())
Index('idx_captain_logs_entry_type', captain_logs.c.player_id, captain_logs.c.entry_type)
