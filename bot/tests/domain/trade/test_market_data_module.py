#!/usr/bin/env python3
"""Unit tests for market_data helper functions."""

from datetime import datetime, timedelta, timezone

import pytest

from spacetraders_bot.core.database import Database, get_database  # type: ignore
from spacetraders_bot.core.market_data import (
    find_markets_buying,
    find_markets_selling,
    get_recent_updates,
    get_stale_markets,
    get_waypoint_good,
    get_waypoint_goods,
    summarize_good,
)


@pytest.fixture()
def temp_database(tmp_path):
    db_path = tmp_path / "market_test.db"
    database = Database(db_path)
    yield database
    # Remove cached instance
    cache = getattr(get_database, "_db_instances", {})
    cache.pop(str(db_path.resolve()), None)


def _seed_market_entry(
    db: Database,
    waypoint: str,
    good: str,
    *,
    supply: str,
    activity: str,
    purchase_price: int,
    sell_price: int,
    trade_volume: int,
    last_updated: datetime,
) -> None:
    with db.transaction() as conn:
        db.update_market_data(
            conn,
            waypoint,
            good,
            supply,
            activity,
            purchase_price,
            sell_price,
            trade_volume,
            last_updated.isoformat(),
            player_id=None,
        )


def regression_get_waypoint_goods_returns_all_goods(temp_database):
    now = datetime.now(timezone.utc)
    _seed_market_entry(
        temp_database,
        "X1-TEST-A1",
        "IRON_ORE",
        supply="HIGH",
        activity="STRONG",
        purchase_price=45,
        sell_price=70,
        trade_volume=120,
        last_updated=now,
    )
    _seed_market_entry(
        temp_database,
        "X1-TEST-A1",
        "COPPER",
        supply="MODERATE",
        activity="FAIR",
        purchase_price=110,
        sell_price=155,
        trade_volume=80,
        last_updated=now,
    )

    goods = get_waypoint_goods("X1-TEST-A1", db=temp_database)
    assert {g["good_symbol"] for g in goods} == {"IRON_ORE", "COPPER"}


def regression_find_markets_selling_filters_by_system_and_supply(temp_database):
    now = datetime.now(timezone.utc)
    _seed_market_entry(
        temp_database,
        "X1-TEST-A1",
        "IRON_ORE",
        supply="LIMITED",
        activity="FAIR",
        purchase_price=60,
        sell_price=90,
        trade_volume=50,
        last_updated=now,
    )
    _seed_market_entry(
        temp_database,
        "X1-TEST-A2",
        "IRON_ORE",
        supply="ABUNDANT",
        activity="STRONG",
        purchase_price=40,
        sell_price=72,
        trade_volume=75,
        last_updated=now,
    )
    _seed_market_entry(
        temp_database,
        "X2-OTHER-B1",
        "IRON_ORE",
        supply="ABUNDANT",
        activity="STRONG",
        purchase_price=35,
        sell_price=65,
        trade_volume=70,
        last_updated=now,
    )

    markets = find_markets_selling(
        "IRON_ORE",
        system="X1-TEST",
        min_supply="MODERATE",
        db=temp_database,
    )
    assert len(markets) == 1
    assert markets[0]["waypoint_symbol"] == "X1-TEST-A2"


def regression_find_markets_buying_orders_by_sell_price(temp_database):
    now = datetime.now(timezone.utc)
    _seed_market_entry(
        temp_database,
        "X1-TEST-A1",
        "COPPER",
        supply="HIGH",
        activity="EXCESSIVE",
        purchase_price=100,
        sell_price=175,
        trade_volume=60,
        last_updated=now,
    )
    _seed_market_entry(
        temp_database,
        "X1-TEST-A2",
        "COPPER",
        supply="MODERATE",
        activity="STRONG",
        purchase_price=115,
        sell_price=150,
        trade_volume=55,
        last_updated=now,
    )

    markets = find_markets_buying("COPPER", system="X1-TEST", db=temp_database)
    assert [m["waypoint_symbol"] for m in markets] == ["X1-TEST-A1", "X1-TEST-A2"]


def regression_recent_updates_and_stale_queries(temp_database):
    now = datetime.now(timezone.utc)
    stale_time = now - timedelta(hours=3)
    _seed_market_entry(
        temp_database,
        "X1-TEST-A1",
        "IRON_ORE",
        supply="HIGH",
        activity="STRONG",
        purchase_price=40,
        sell_price=70,
        trade_volume=90,
        last_updated=now,
    )
    _seed_market_entry(
        temp_database,
        "X1-TEST-A2",
        "IRON_ORE",
        supply="HIGH",
        activity="STRONG",
        purchase_price=41,
        sell_price=71,
        trade_volume=90,
        last_updated=stale_time,
    )

    recent = get_recent_updates(system="X1-TEST", limit=1, db=temp_database)
    assert recent[0]["waypoint_symbol"] == "X1-TEST-A1"

    stale = get_stale_markets(2, system="X1-TEST", db=temp_database)
    assert len(stale) == 1
    assert stale[0]["waypoint_symbol"] == "X1-TEST-A2"


def regression_get_waypoint_good_and_summary(temp_database):
    now = datetime.now(timezone.utc)
    _seed_market_entry(
        temp_database,
        "X1-TEST-A1",
        "COPPER",
        supply="HIGH",
        activity="STRONG",
        purchase_price=100,
        sell_price=150,
        trade_volume=40,
        last_updated=now,
    )
    _seed_market_entry(
        temp_database,
        "X1-TEST-A2",
        "COPPER",
        supply="LIMITED",
        activity="FAIR",
        purchase_price=120,
        sell_price=160,
        trade_volume=30,
        last_updated=now,
    )

    entry = get_waypoint_good("X1-TEST-A1", "COPPER", db=temp_database)
    assert entry is not None
    assert entry["sell_price"] == 150

    summary = summarize_good("COPPER", system="X1-TEST", db=temp_database)
    assert summary is not None
    assert summary["market_count"] == 2
    assert summary["min_purchase_price"] == 100
    assert summary["max_sell_price"] == 160
