from types import SimpleNamespace
from unittest.mock import Mock

import pytest

import spacetraders_bot.operations.mining as mining_module
from spacetraders_bot.operations.mining import (
    MiningContext,
    MiningCycle,
    MiningStats,
)


@pytest.fixture
def mining_context(monkeypatch):
    args = SimpleNamespace(ship="SHIP-1", cycles=3)
    ship = Mock()
    ship.orbit = Mock()
    ship.refuel = Mock()
    ship.get_cargo = Mock(return_value={"units": 0, "capacity": 60})
    navigator = Mock()
    controller = Mock()
    controller.should_cancel.return_value = False
    controller.should_pause.return_value = False
    stats = MiningStats()
    log_error = Mock()

    return MiningContext(
        args=args,
        ship=ship,
        navigator=navigator,
        controller=controller,
        stats=stats,
        log_error=log_error,
    )


def test_mining_cycle_execute_success(monkeypatch, mining_context):
    def fake_navigate(context, destination, cycle, description, resolution):
        return True

    def fake_mine(context):
        context.stats.total_extracted += 20
        return {"units": 20}

    def fake_sell(context, cargo):
        context.stats.total_revenue += 1000
        context.stats.total_sold += cargo.get("units", 0)
        return 1000

    checkpoint_calls = []

    def fake_checkpoint(context, cycle, location):
        checkpoint_calls.append((cycle, location))

    monkeypatch.setattr(mining_module, "_navigate_with_retries", fake_navigate)
    monkeypatch.setattr(mining_module, "_mine_until_cargo_full", fake_mine)
    monkeypatch.setattr(mining_module, "_sell_cargo", fake_sell)
    monkeypatch.setattr(mining_module, "_checkpoint_cycle", fake_checkpoint)

    monkeypatch.setitem(MiningCycle.execute.__globals__, "_navigate_with_retries", fake_navigate)
    monkeypatch.setitem(MiningCycle.execute.__globals__, "_mine_until_cargo_full", fake_mine)
    monkeypatch.setitem(MiningCycle.execute.__globals__, "_sell_cargo", fake_sell)
    monkeypatch.setitem(MiningCycle.execute.__globals__, "_checkpoint_cycle", fake_checkpoint)

    cycle = MiningCycle(
        context=mining_context,
        total_cycles=3,
        asteroid="AST-1",
        market="MKT-1",
    )

    result = cycle.execute(1)

    assert result is True
    assert mining_context.stats.cycles_completed == 1
    assert mining_context.stats.total_revenue == 1000
    assert mining_context.stats.total_sold == 20
    assert checkpoint_calls == [(1, "MKT-1")]
    mining_context.ship.orbit.assert_called_once()
    mining_context.ship.refuel.assert_called_once()


def test_mining_cycle_aborts_on_failed_navigation(monkeypatch, mining_context):
    def fake_navigate(context, destination, cycle, description, resolution):
        return destination != "AST-1"

    def fake_mine(context):
        return {}

    monkeypatch.setattr(mining_module, "_navigate_with_retries", fake_navigate)
    monkeypatch.setattr(mining_module, "_mine_until_cargo_full", fake_mine)
    monkeypatch.setitem(MiningCycle.execute.__globals__, "_navigate_with_retries", fake_navigate)
    monkeypatch.setitem(MiningCycle.execute.__globals__, "_mine_until_cargo_full", fake_mine)

    cycle = MiningCycle(
        context=mining_context,
        total_cycles=3,
        asteroid="AST-1",
        market="MKT-1",
    )

    result = cycle.execute(1)

    assert result is False
    assert mining_context.stats.cycles_completed == 0
    mining_context.ship.orbit.assert_not_called()
    mining_context.ship.refuel.assert_not_called()
