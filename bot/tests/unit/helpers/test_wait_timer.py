from importlib import import_module

wait_timer_module = import_module('spacetraders_bot.helpers.wait_timer')


def test_wait_timer(monkeypatch):
    prints = []
    monkeypatch.setattr(wait_timer_module.time, 'sleep', lambda seconds: prints.append(f'sleep {seconds}'))
    monkeypatch.setattr('builtins.print', lambda message: prints.append(message))

    wait_timer_module.wait_timer(2)

    assert prints[0].startswith('⏳ Waiting for 2 seconds')
    assert 'sleep 2' in prints
    assert prints[-1].startswith('✅ Wait complete!')
