from pathlib import Path
from types import SimpleNamespace

import json

import pytest

import spacetraders_bot.operations.captain_logging as captain_logging
from spacetraders_bot.operations.captain_logging import CaptainLogWriter, captain_log_operation


class APIStub:
    def __init__(self, agent=None, ships=None, contracts=None):
        self.agent = agent or {'credits': 25000, 'faction': 'COSMIC', 'headquarters': 'X1-TEST-A1'}
        self.ships = ships or [{'nav': {'systemSymbol': 'X1-TEST'}}]
        self.contracts = contracts or [{'accepted': True, 'fulfilled': False}]

    def get_agent(self):
        return self.agent

    def list_ships(self):
        return {'data': self.ships}

    def list_contracts(self):
        return {'data': self.contracts}


@pytest.fixture
def temp_logs_root(tmp_path, monkeypatch):
    root = tmp_path / 'captain'

    def fake_root(agent_symbol):
        agent_dir = root / agent_symbol.lower()
        sessions = agent_dir / 'sessions'
        reports = agent_dir / 'executive_reports'
        sessions.mkdir(parents=True, exist_ok=True)
        reports.mkdir(parents=True, exist_ok=True)
        return agent_dir

    monkeypatch.setattr('spacetraders_bot.operations.captain_logging.captain_logs_root', fake_root)
    return root


def regression_append_to_log_uses_circuit_breaker(tmp_path, monkeypatch):
    writer = CaptainLogWriter('CMD-LOCK', token='TOKEN')
    writer.log_file = tmp_path / 'captain.log'
    writer.log_file.write_text('')

    attempts = {'count': 0}

    def fake_flock(fd, flag):
        if flag == captain_logging.fcntl.LOCK_UN:
            return None
        attempts['count'] += 1
        if attempts['count'] < 3:
            raise IOError(11, 'temporarily unavailable')

    sleep_calls = []

    monkeypatch.setattr('spacetraders_bot.operations.captain_logging.fcntl.flock', fake_flock)
    monkeypatch.setattr('spacetraders_bot.operations.captain_logging.time.sleep', lambda seconds: sleep_calls.append(seconds))

    writer._append_to_log('ENTRY', max_retries=5)

    assert attempts['count'] == 3
    assert sleep_calls == [0.1, 0.2]


def regression_append_to_log_raises_after_circuit_breaker_trip(tmp_path, monkeypatch):
    writer = CaptainLogWriter('CMD-LOCK', token='TOKEN')
    writer.log_file = tmp_path / 'captain.log'
    writer.log_file.write_text('')

    def fake_flock(fd, flag):
        if flag == captain_logging.fcntl.LOCK_UN:
            return None
        raise IOError(11, 'temporarily unavailable')

    monkeypatch.setattr('spacetraders_bot.operations.captain_logging.fcntl.flock', fake_flock)
    monkeypatch.setattr('spacetraders_bot.operations.captain_logging.time.sleep', lambda seconds: None)

    with pytest.raises(IOError):
        writer._append_to_log('ENTRY', max_retries=2)


def regression_initialize_log_creates_file(temp_logs_root, monkeypatch):
    api_stub = APIStub()
    monkeypatch.setattr('spacetraders_bot.operations.captain_logging.APIClient', lambda token: api_stub)
    monkeypatch.setattr(CaptainLogWriter, '_get_timestamp', lambda self: '2025-01-01T00:00:00Z')

    writer = CaptainLogWriter('CMD-TEST', token='TOKEN')
    writer.initialize_log()

    log_path = temp_logs_root / 'cmd-test' / 'captain-log.md'
    assert log_path.exists()
    content = log_path.read_text()
    assert 'CMD-TEST' in content
    assert 'COSMIC' in content


def regression_session_start_records_state(temp_logs_root, monkeypatch):
    api_stub = APIStub(agent={'credits': 12345, 'faction': 'COSMIC', 'headquarters': 'X1-TEST-A1'}, ships=[{'nav': {'systemSymbol': 'X1-ALPHA'}}])
    monkeypatch.setattr('spacetraders_bot.operations.captain_logging.APIClient', lambda token: api_stub)
    monkeypatch.setattr(CaptainLogWriter, '_get_timestamp', lambda self: '2025-01-01T00:00:00Z')

    append_calls = []

    writer = CaptainLogWriter('CMD-SESSION', token='TOKEN')
    writer._append_to_log = lambda content: append_calls.append(content)

    session_id = writer.session_start('Explore asteroid belt', narrative='Setting course for the belt with optimism.')

    state_file = temp_logs_root / 'cmd-session' / 'sessions' / 'current_session.json'
    state = json.loads(state_file.read_text())

    assert session_id == state['session_id']
    assert state['objective'] == 'Explore asteroid belt'
    assert append_calls and 'SESSION_START' in append_calls[0]


def regression_log_entry_updates_session(temp_logs_root, monkeypatch):
    monkeypatch.setattr('spacetraders_bot.operations.captain_logging.APIClient', lambda token: APIStub())
    monkeypatch.setattr(CaptainLogWriter, '_get_timestamp', lambda self: '2025-01-01T00:00:00Z')

    writer = CaptainLogWriter('CMD-LOG', token='TOKEN')
    writer.current_session = {'operations': [], 'errors': []}
    writer._save_session_state = lambda: None

    captured = []
    writer._append_to_log = lambda content: captured.append(content)

    writer.log_entry(
        'OPERATION_STARTED',
        operator='AI',
        ship='SHIP-1',
        op_type='mining',
        daemon_id='daemon-1',
        parameters={'target': 'AST-1'},
        narrative='Deploying miner',
        tags=['mining']
    )

    assert writer.current_session['operations'][0]['daemon_id'] == 'daemon-1'
    assert 'OPERATION_STARTED' in captured[0]

    writer.log_entry(
        'CRITICAL_ERROR',
        ship='SHIP-1',
        error='Failure',
        narrative='Engine failure mid-route required abort.',
    )
    assert writer.current_session['errors'][0]['error'] == 'Failure'


def regression_log_entry_operation_completed_sections(temp_logs_root, monkeypatch):
    monkeypatch.setattr('spacetraders_bot.operations.captain_logging.APIClient', lambda token: APIStub())
    monkeypatch.setattr(CaptainLogWriter, '_get_timestamp', lambda self: '2025-01-01T00:00:00Z')

    writer = CaptainLogWriter('CMD-REPORT', token='TOKEN')
    writer.current_session = None
    writer._save_session_state = lambda: None

    captured = []
    writer._append_to_log = lambda content: captured.append(content)

    writer.log_entry(
        'OPERATION_COMPLETED',
        operator='AI',
        ship='SHIP-1',
        duration='15m',
        results={'Profit': '5,000 cr', 'Fuel Used': '120'},
        narrative='Mission accomplished with minimal resistance.',
        insights='Consider deploying a second hauler.',
        recommendations='Reinvest profits into fuel reserves.',
        notes='All objectives met.',
        tags=['mining', 'success'],
    )

    output = captured[0]
    assert 'MISSION REPORT' in output
    assert 'STRATEGIC INSIGHTS' in output
    assert 'RECOMMENDATIONS' in output

    writer.log_entry(
        'PERFORMANCE_SUMMARY',
        summary_type='Mining',
        financials={'revenue': 5000, 'cumulative': 20000, 'rate': 2500},
        operations={'completed': 2, 'active': 0, 'success_rate': 100},
        fleet={'active': 1, 'total': 3},
        top_performers=[{'ship': 'SHIP-1', 'profit': 5000, 'operation': 'mining'}],
        tags=['performance']
    )

    assert len(captured) == 1


def regression_log_entry_requires_narrative(temp_logs_root, monkeypatch, capsys):
    monkeypatch.setattr('spacetraders_bot.operations.captain_logging.APIClient', lambda token: APIStub())
    writer = CaptainLogWriter('CMD-NARRATIVE', token='TOKEN')
    writer._append_to_log = lambda content: pytest.fail("Entry should be skipped when narrative missing")

    writer.log_entry(
        'OPERATION_COMPLETED',
        operator='AI',
        ship='SHIP-1',
        duration='5m',
        results={'Deliveries': '1'},
        tags=['contract']
    )

    captured = capsys.readouterr()
    assert "skipped" in captured.out.lower()


def regression_log_entry_ignores_scout_operations(temp_logs_root, monkeypatch, capsys):
    monkeypatch.setattr('spacetraders_bot.operations.captain_logging.APIClient', lambda token: APIStub())
    writer = CaptainLogWriter('CMD-SCOUT', token='TOKEN')
    writer._append_to_log = lambda content: pytest.fail("Scout operations must be ignored")

    writer.log_entry(
        'OPERATION_STARTED',
        operator='AI',
        ship='SHIP-2',
        op_type='scout',
        daemon_id='daemon-scout',
        parameters={'route': 'tour'},
        narrative='Scout launching to map markets.',
        tags=['scouting']
    )

    captured = capsys.readouterr()
    assert "skipped" in captured.out.lower()


def regression_session_end_saves_archive(temp_logs_root, monkeypatch):
    real_datetime = __import__('datetime').datetime

    class FakeDateTime:
        @classmethod
        def now(cls, tz=None):
            return real_datetime(2025, 1, 1, 1, 0, 0, tzinfo=__import__('datetime').timezone.utc)

        @classmethod
        def fromisoformat(cls, value):
            return real_datetime.fromisoformat(value)

    monkeypatch.setattr('spacetraders_bot.operations.captain_logging.APIClient', lambda token: APIStub(agent={'credits': 15000}))
    monkeypatch.setattr('spacetraders_bot.operations.captain_logging.datetime', FakeDateTime)
    monkeypatch.setattr(CaptainLogWriter, '_get_timestamp', lambda self: '2025-01-01T01:00:00Z')

    writer = CaptainLogWriter('CMD-END', token='TOKEN')
    writer.current_session = {
        'session_id': '20250101_000000',
        'start_time': '2025-01-01T00:00:00Z',
        'start_credits': 10000,
        'objective': 'Mine resources',
        'operations': [{'daemon_id': 'daemon-1'}],
        'errors': []
    }
    writer._save_session_state = lambda: None

    captured = []
    writer._append_to_log = lambda content: captured.append(content)

    writer.session_end()

    archive = (temp_logs_root / 'cmd-end' / 'sessions' / '20250101_000000.json')
    assert archive.exists()
    data = json.loads(archive.read_text())
    assert data['net_profit'] == 5000
    assert writer.current_session is None
    assert captured and 'SESSION_END' in captured[0]


def regression_search_and_report(temp_logs_root, monkeypatch):
    real_datetime = __import__('datetime').datetime

    class FakeDateTime:
        @classmethod
        def now(cls, tz=None):
            return real_datetime(2025, 1, 2, 0, 0, 0, tzinfo=__import__('datetime').timezone.utc)

        @classmethod
        def fromisoformat(cls, value):
            return real_datetime.fromisoformat(value)

    monkeypatch.setattr('spacetraders_bot.operations.captain_logging.datetime', FakeDateTime)
    monkeypatch.setattr('spacetraders_bot.operations.captain_logging.APIClient', lambda token: APIStub())
    monkeypatch.setattr(CaptainLogWriter, '_get_timestamp', lambda self: '2025-01-02T00:00:00Z')

    writer = CaptainLogWriter('CMD-SEARCH', token='TOKEN')
    writer._append_to_log = lambda content: None

    log_path = writer.log_file
    log_path.write_text(
        """HEADER\n### 📅 STARDATE:2025-01-01T23:00:00Z\n\n#### ✅ OPERATION_COMPLETED\n**Tags:** `#mining`\n---\n"""
    )

    matches = writer.search_logs(tag='mining')
    assert len(matches) == 1

    recent = writer.search_logs(tag='mining', timeframe=2)
    assert isinstance(recent, list)

    session_data = {
        'session_id': '20250101_000000',
        'start_time': '2025-01-01T20:00:00Z',
        'end_time': '2025-01-01T22:00:00Z',
        'net_profit': 4000,
        'roi': 40,
        'objective': 'Scout markets',
        'operations': [{'daemon_id': 'daemon-1'}],
        'errors': []
    }
    session_file = writer.sessions_dir / '20250101_000000.json'
    session_file.write_text(json.dumps(session_data))

    report = writer.generate_executive_report(duration_hours=24)
    assert 'EXECUTIVE REPORT' in report
    assert 'Total Profit' in report


def regression_captain_log_operation_actions(tmp_path, monkeypatch, capsys):
    monkeypatch.setattr('spacetraders_bot.operations.common.setup_logging', lambda *a, **k: tmp_path / 'log.log')

    class WriterStub:
        def __init__(self, agent, token):
            self.agent = agent
            self.token = token
            self.calls = []
            self.reports_dir = tmp_path
            self.sessions_dir = tmp_path

        def initialize_log(self):
            self.calls.append(('init',))

        def session_start(self, objective, operator, narrative=None):
            self.calls.append(('start', objective, operator, narrative))

        def session_end(self):
            self.calls.append(('end',))

        def log_entry(self, entry_type, **kwargs):
            self.calls.append(('entry', entry_type, kwargs))

        def search_logs(self, tag=None, timeframe=None):
            self.calls.append(('search', tag, timeframe))
            return ['ENTRY']

        def generate_executive_report(self, duration_hours=24):
            self.calls.append(('report', duration_hours))
            return 'REPORT'

    created = []

    def writer_factory(agent, token):
        writer = WriterStub(agent, token)
        created.append(writer)
        return writer

    monkeypatch.setattr('spacetraders_bot.operations.captain_logging.CaptainLogWriter', writer_factory)
    monkeypatch.setattr('spacetraders_bot.operations.common.get_database', lambda: SimpleNamespace(connection=lambda: SimpleNamespace(__enter__=lambda self: SimpleNamespace(get_player_by_id=lambda conn, pid: None), __exit__=lambda self, exc_type, exc, tb: False)))

    args_init = SimpleNamespace(agent='CMD', action='init', player_id=None)
    captain_log_operation(args_init)
    assert created[-1].calls == [('init',)]

    args_start = SimpleNamespace(agent='CMD', action='session-start', objective='Explore', operator='AI', narrative='Heading out with a clear plan.', player_id=None)
    captain_log_operation(args_start)
    assert created[-1].calls[-1][0] == 'start'

    args_end = SimpleNamespace(agent='CMD', action='session-end', player_id=None)
    captain_log_operation(args_end)
    assert created[-1].calls[-1][0] == 'end'

    args_entry = SimpleNamespace(agent='CMD', action='entry', entry_type='OPERATION_STARTED', operator='AI', ship='SHIP-1', player_id=None)
    captain_log_operation(args_entry)
    assert created[-1].calls[-1][0] == 'entry'

    args_search = SimpleNamespace(agent='CMD', action='search', tag='mining', timeframe=4, player_id=None)
    captain_log_operation(args_search)
    assert created[-1].calls[-1][0] == 'search'
    assert 'ENTRY' in capsys.readouterr().out

    args_report = SimpleNamespace(agent='CMD', action='report', duration=12, player_id=None)
    captain_log_operation(args_report)
    report_writer = created[-1]
    assert report_writer.calls[-1] == ('report', 12)
