import importlib

wait_timer_module = importlib.import_module('spacetraders_bot.helpers.wait_timer')


def test_wait_timer(monkeypatch, capsys):
    monkeypatch.setattr(wait_timer_module.time, 'sleep', lambda *_: None)
    wait_timer_module.wait_timer(3)

    out = capsys.readouterr().out
    assert 'Waiting for 3 seconds' in out
    assert 'Wait complete' in out
