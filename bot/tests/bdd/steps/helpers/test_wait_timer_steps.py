from importlib import import_module

from pytest_bdd import scenarios, given, when, then

wait_timer_module = import_module('spacetraders_bot.helpers.wait_timer')

scenarios('../../features/helpers/wait_timer.feature')


@given('the wait timer is patched to capture output', target_fixture='wait_timer_calls')
def given_patched_wait_timer(monkeypatch):
    calls = {'prints': [], 'sleep': []}
    monkeypatch.setattr(wait_timer_module.time, 'sleep', lambda seconds: calls['sleep'].append(seconds))
    monkeypatch.setattr('builtins.print', lambda message: calls['prints'].append(message))
    return calls


@when('the wait timer runs for 2 seconds', target_fixture='wait_timer_state')
def when_wait_timer_runs(wait_timer_calls):
    wait_timer_module.wait_timer(2)
    return wait_timer_calls


@then('the wait timer reports the wait duration')
def then_reports_duration(wait_timer_state):
    prints = wait_timer_state['prints']
    assert prints[0].startswith('⏳ Waiting for 2 seconds')
    assert prints[-1].startswith('✅ Wait complete!')


@then('the wait timer calls sleep with 2 seconds')
def then_sleep_called(wait_timer_state):
    assert wait_timer_state['sleep'] == [2]
