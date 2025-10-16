import importlib
import sys

import pytest

cli_main = importlib.import_module('spacetraders_bot.cli.main')


def _run_cli(monkeypatch, argv, **handlers):
    for name, handler in handlers.items():
        monkeypatch.setattr(cli_main, name, handler)

    monkeypatch.setattr(sys, 'argv', ['spacetraders-bot', *argv])
    return cli_main.main()


def regression_cli_dispatches_graph_build(monkeypatch):
    called = {}

    def fake_graph(args):
        called['args'] = args
        return 0

    result = _run_cli(
        monkeypatch,
        ['graph-build', '--player-id', '7', '--system', 'X1-TEST'],
        graph_build_operation=fake_graph,
    )

    assert result == 0
    assert called['args'].system == 'X1-TEST'


def regression_cli_route_plan(monkeypatch):
    triggered = {}

    def fake_route(args):
        triggered['args'] = args
        return 5

    result = _run_cli(
        monkeypatch,
        [
            'route-plan',
            '--player-id', '9',
            '--ship', 'SHIP-1',
            '--system', 'X1-OPS',
            '--start', 'A',
            '--goal', 'B',
        ],
        route_plan_operation=fake_route,
    )

    assert result == 5
    assert triggered['args'].goal == 'B'


def regression_cli_assignments_list(monkeypatch):
    captured = {}

    def fake_list(args):
        captured['called'] = True
        return 0

    result = _run_cli(
        monkeypatch,
        ['assignments', 'list', '--player-id', '3'],
        assignment_list_operation=fake_list,
    )

    assert result == 0
    assert captured.get('called') is True


def regression_cli_assignments_missing_action(monkeypatch, capsys):
    result = _run_cli(monkeypatch, ['assignments'])
    assert result == 1
    out = capsys.readouterr().out
    assert 'usage' in out.lower()


def regression_cli_scout_coordinator_status(monkeypatch):
    hit = {}

    def fake_status(args):
        hit['action'] = args.coordinator_action
        return 0

    result = _run_cli(
        monkeypatch,
        [
            'scout-coordinator',
            'status',
            '--player-id', '5',
            '--system', 'X1-TEST',
        ],
        coordinator_status_operation=fake_status,
    )

    assert result == 0
    assert hit['action'] == 'status'


def regression_cli_daemon_start(monkeypatch):
    called = {}

    def fake_daemon_start(args):
        called['action'] = args.daemon_action
        return 3

    result = _run_cli(
        monkeypatch,
        [
            'daemon',
            'start',
            '--player-id', '11',
            '--ship', 'SHIP-9',
            '--operation', 'mining',
        ],
        daemon_start_operation=fake_daemon_start,
    )

    assert result == 3
    assert called['action'] == 'start'
