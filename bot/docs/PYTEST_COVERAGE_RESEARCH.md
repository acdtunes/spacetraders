# Pytest, pytest-bdd, and Coverage.py - Comprehensive Documentation Research

**Research Date:** 2025-10-19
**Purpose:** Support test coverage improvement initiative for SpaceTraders Bot
**Current Stack:** pytest 7.0+, pytest-bdd 6.0+, pytest-cov 4.0+

---

## Table of Contents

1. [pytest-bdd Official Documentation](#1-pytest-bdd-official-documentation)
2. [pytest Fixtures Best Practices](#2-pytest-fixtures-best-practices)
3. [coverage.py Configuration & Advanced Usage](#3-coveragepy-configuration--advanced-usage)
4. [pytest-cov Plugin Features](#4-pytest-cov-plugin-features)
5. [pytest Markers for Test Organization](#5-pytest-markers-for-test-organization)
6. [BDD Scenario Outline & Parametrization](#6-bdd-scenario-outline--parametrization)
7. [Testing Async Code with pytest-asyncio](#7-testing-async-code-with-pytest-asyncio)
8. [pytest Plugins for Enhanced Testing](#8-pytest-plugins-for-enhanced-testing)
9. [Coverage Exclusion Patterns & Pragmas](#9-coverage-exclusion-patterns--pragmas)
10. [CI/CD Integration for Coverage Enforcement](#10-cicd-integration-for-coverage-enforcement)

---

## 1. pytest-bdd Official Documentation

**Official URL:** https://pytest-bdd.readthedocs.io/en/latest/
**Latest Version:** 8.1.0 (February 2025)
**PyPI:** https://pypi.org/project/pytest-bdd/

### Key Features

- **Gherkin Parser:** Uses official gherkin-official parser (v8+) for better compatibility
- **No Separate Runner:** Integrates directly with pytest (no behave/cucumber runner needed)
- **Dependency Injection:** Uses pytest fixtures instead of global context objects
- **Supported Python:** 3.9, 3.10, 3.11, 3.12, 3.13 (dropped 3.8 in v8+)

### Breaking Changes in v8.x

1. **Multiline steps** must use triple-quotes for additional lines
2. **Feature keyword required:** All feature files must start with `Feature:`
3. **Tags cannot have spaces:** `@my_tag` works, `@my tag` fails

### Basic Usage

**Feature File (`publish_article.feature`):**
```gherkin
Feature: Publishing articles
  As an author
  I want to publish my articles
  So that readers can view them

Scenario: Publishing a draft article
  Given I'm an author user
  And I have a draft article
  When I publish the article
  Then the article should be publicly visible
```

**Step Definitions (`test_publish_steps.py`):**
```python
from pytest_bdd import scenario, given, when, then, parsers

@scenario('publish_article.feature', 'Publishing a draft article')
def test_publish():
    """Pytest test function binding to scenario"""
    pass

@given("I'm an author user")
def author_user(auth, author):
    """Setup: create author user"""
    auth['user'] = author.user
    return auth

@given("I have a draft article")
def draft_article(author):
    """Setup: create draft article"""
    return author.create_article(status='draft')

@when("I publish the article")
def publish_article(draft_article):
    """Action: publish the article"""
    draft_article.publish()

@then("the article should be publicly visible")
def verify_published(draft_article):
    """Assertion: verify publication"""
    assert draft_article.status == 'published'
    assert draft_article.is_public is True
```

### Step Argument Parsers

Four parser types for extracting parameters:

1. **string** (default) - Exact matching
2. **parse** - Named fields: `{param:Type}`
3. **cfparse** - Extended with cardinality: `{values:Type+}`
4. **re** - Full regex with named groups

**Example:**
```python
from pytest_bdd import given, parsers

@given(parsers.parse('there are {count:d} cucumbers'))
def cucumbers(count):
    return {'count': count}

@given(parsers.cfparse('I have {items:Words+} in my basket'))
def basket_items(items):
    return items  # items is a list

@given(parsers.re(r'I wait (?P<duration>\d+) seconds'))
def wait_duration(duration):
    time.sleep(int(duration))
```

### Hooks & Advanced Features

**Before/After Scenario Hooks:**
```python
from pytest_bdd import given, when, then
import pytest

@pytest.fixture
def context():
    """Shared context for scenario"""
    return {}

@given("setup step", target_fixture="setup_result")
def setup_step():
    """Fixture-returning step"""
    return {"initialized": True}
```

**Data Tables & Docstrings:**
```python
@given(parsers.parse('I have the following users:\n{data}'))
def users_table(data):
    """Access table data from step"""
    # data contains the table as string
    # Parse it manually or use helper library
    return parse_table(data)
```

### Best Practices

1. **Use fixtures for state management** - Avoid global variables
2. **Prefer parse over regex** - More readable, easier to maintain
3. **Keep steps atomic** - One logical action per step
4. **Use scenario outlines** - DRY principle for parametrized tests
5. **Tag scenarios** - Enable selective test execution (`@smoke`, `@regression`)

---

## 2. pytest Fixtures Best Practices

**Official URL:** https://docs.pytest.org/en/stable/how-to/fixtures.html

### Fixture Scopes

Five scope levels controlling fixture lifetime:

| Scope | Lifetime | Use Case |
|-------|----------|----------|
| `function` | Per test (default) | Test-specific setup/teardown |
| `class` | Per test class | Shared state within class |
| `module` | Per module | Expensive setup (DB connection) |
| `package` | Per package | Package-wide resources |
| `session` | Entire test session | One-time global setup |

**Example:**
```python
import pytest

@pytest.fixture(scope="session")
def database_connection():
    """Created once for entire test session"""
    conn = Database.connect()
    yield conn
    conn.close()

@pytest.fixture(scope="module")
def api_client():
    """Created once per test module"""
    client = APIClient()
    yield client
    client.cleanup()

@pytest.fixture  # scope="function" is default
def temporary_data():
    """Created for each test"""
    data = create_test_data()
    yield data
    cleanup_test_data(data)
```

### Fixture Composition

**Fixtures can request other fixtures:**
```python
@pytest.fixture
def smtp_connection():
    return smtplib.SMTP("smtp.gmail.com", 587)

@pytest.fixture
def authenticated_smtp(smtp_connection, credentials):
    smtp_connection.login(credentials.user, credentials.password)
    return smtp_connection

def test_send_email(authenticated_smtp):
    """Test receives fully configured SMTP client"""
    authenticated_smtp.sendmail("from@test.com", "to@test.com", "Test")
```

### Cleanup Strategies

**Yield Fixtures (Recommended):**
```python
@pytest.fixture
def resource():
    # Setup
    r = allocate_resource()

    # Provide to test
    yield r

    # Teardown (always runs even if test fails)
    r.cleanup()
```

**Finalizers (Alternative):**
```python
@pytest.fixture
def resource(request):
    r = allocate_resource()

    def cleanup():
        r.cleanup()

    request.addfinalizer(cleanup)
    return r
```

### Fixture Parametrization

**Run tests with multiple fixture values:**
```python
@pytest.fixture(params=["sqlite", "postgres", "mysql"])
def database(request):
    """Test runs 3 times, once per database"""
    db_type = request.param
    db = Database.connect(db_type)
    yield db
    db.disconnect()

def test_query(database):
    """Runs 3 times with different database backends"""
    result = database.query("SELECT 1")
    assert result is not None
```

### Factory Pattern

**Generate multiple instances in a single test:**
```python
@pytest.fixture
def make_user():
    """Factory fixture returns a callable"""
    users = []

    def _make_user(username, role="user"):
        user = User(username=username, role=role)
        users.append(user)
        return user

    yield _make_user

    # Cleanup all created users
    for user in users:
        user.delete()

def test_permissions(make_user):
    """Create multiple users in one test"""
    admin = make_user("admin", role="admin")
    regular = make_user("user1")
    guest = make_user("guest", role="guest")

    assert admin.can_delete()
    assert not regular.can_delete()
    assert not guest.can_delete()
```

### Autouse Fixtures

**Automatically apply to all tests:**
```python
@pytest.fixture(autouse=True)
def reset_global_state():
    """Runs before every test automatically"""
    GlobalCache.clear()
    yield
    GlobalCache.clear()

@pytest.fixture(autouse=True, scope="session")
def configure_logging():
    """Configure logging once for all tests"""
    logging.basicConfig(level=logging.DEBUG)
```

### Dynamic Scopes

**Runtime scope determination:**
```python
def determine_scope(fixture_name, config):
    """Choose scope based on config"""
    if config.getoption("--expensive-setup"):
        return "session"
    return "function"

@pytest.fixture(scope=determine_scope)
def database(request):
    """Scope chosen at runtime"""
    return Database.connect()
```

### Best Practices

1. **Atomic fixtures** - Each fixture does ONE thing with its cleanup
2. **Minimize scope creep** - Use narrowest scope that works
3. **Explicit dependencies** - Declare fixture dependencies clearly
4. **Avoid side effects** - Keep fixtures independent when possible
5. **Use autouse sparingly** - Explicit is better than implicit
6. **Name fixtures descriptively** - `authenticated_api_client` > `client`

---

## 3. coverage.py Configuration & Advanced Usage

**Official URL:** https://coverage.readthedocs.io/en/latest/config.html
**Latest Version:** 7.10.7
**Exclusion Patterns:** https://coverage.readthedocs.io/en/latest/excluding.html

### Configuration Files

**Supported formats:**
- `.coveragerc` (INI format, default)
- `setup.cfg` (section: `[coverage:run]`)
- `tox.ini` (section: `[coverage:run]`)
- `pyproject.toml` (requires `pip install coverage[toml]`)

**Example `.coveragerc`:**
```ini
[run]
branch = True
source = src/
omit =
    */tests/*
    */migrations/*
    */__pycache__/*
    */site-packages/*
parallel = False
concurrency = multiprocessing
data_file = .coverage

[report]
exclude_lines =
    pragma: no cover
    def __repr__
    raise NotImplementedError
    if TYPE_CHECKING:
    if __name__ == .__main__.:
    @abstractmethod
    @abc.abstractmethod
fail_under = 80
precision = 2
show_missing = True
skip_covered = False
skip_empty = True

[html]
directory = htmlcov
title = SpaceTraders Bot Coverage Report

[xml]
output = coverage.xml
```

**Example `pyproject.toml`:**
```toml
[tool.coverage.run]
branch = true
source = ["src/"]
omit = [
    "*/tests/*",
    "*/migrations/*",
    "*/__pycache__/*",
]
concurrency = ["multiprocessing", "thread"]

[tool.coverage.report]
exclude_lines = [
    "pragma: no cover",
    "def __repr__",
    "raise NotImplementedError",
    "if TYPE_CHECKING:",
    "if __name__ == .__main__.:",
    "@abstractmethod",
]
fail_under = 80
precision = 2
show_missing = true
skip_empty = true

[tool.coverage.html]
directory = "htmlcov"
```

### [run] Section - Key Options

| Option | Purpose | Example |
|--------|---------|---------|
| `branch` | Enable branch coverage | `branch = True` |
| `source` | Packages/dirs to measure | `source = src/,lib/` |
| `omit` | File patterns to exclude | `omit = */tests/*` |
| `include` | File patterns to include | `include = src/*.py` |
| `parallel` | Multi-process coverage | `parallel = True` |
| `concurrency` | Async library support | `concurrency = gevent,eventlet` |
| `data_file` | Coverage data location | `data_file = .coverage` |
| `plugins` | Coverage plugins | `plugins = django_coverage_plugin` |

**Concurrency Options:**
- `thread` - Multi-threaded code
- `multiprocessing` - Multiprocessing module
- `gevent` - Gevent async
- `eventlet` - Eventlet async
- `greenlet` - Greenlet coroutines

### [report] Section - Key Options

| Option | Purpose | Example |
|--------|---------|---------|
| `fail_under` | Minimum coverage % | `fail_under = 80` |
| `precision` | Decimal places | `precision = 2` |
| `show_missing` | Show uncovered lines | `show_missing = True` |
| `skip_covered` | Hide 100% files | `skip_covered = True` |
| `skip_empty` | Hide empty files | `skip_empty = True` |
| `sort` | Sort order | `sort = Cover` |
| `exclude_lines` | Exclusion patterns | See below |

### Advanced Features

**Path Mapping (for CI/CD):**
```ini
[paths]
source =
    src/
    /home/runner/work/project/src/
    C:\Users\dev\project\src\
```

**Combining Coverage Data:**
```bash
# Collect coverage from multiple test runs
coverage run -p tests/test_unit.py
coverage run -p tests/test_integration.py

# Combine all .coverage.* files
coverage combine

# Generate report
coverage report
coverage html
```

**Branch Coverage:**
```python
def function(x):
    if x > 0:  # Two branches: True and False
        return "positive"
    else:
        return "non-positive"

# Branch coverage tracks BOTH paths
# Line coverage only tracks if line was executed
```

---

## 4. pytest-cov Plugin Features

**Official URL:** https://pytest-cov.readthedocs.io/en/latest/
**Latest Version:** 7.0.0 (September 2025)
**PyPI:** https://pypi.org/project/pytest-cov/

### Key Features Over `coverage run`

1. **Automatic data management** - Erases and combines `.coverage` files
2. **Default reporting** - Shows coverage after tests complete
3. **Coverage contexts** - Track which tests cover which lines
4. **Xdist support** - Works with pytest-xdist for parallel testing
5. **Subprocess coverage** - Tracks coverage in spawned processes

### Command-Line Options

**Basic Usage:**
```bash
# Measure coverage for specific path
pytest --cov=src tests/

# Multiple paths
pytest --cov=src --cov=lib tests/

# Branch coverage
pytest --cov=src --cov-branch tests/

# Fail if coverage below threshold
pytest --cov=src --cov-fail-under=80 tests/

# Append to existing coverage (don't erase)
pytest --cov=src --cov-append tests/

# Disable coverage report
pytest --cov=src --no-cov-on-fail tests/
```

**Report Formats:**
```bash
# Terminal report
pytest --cov=src --cov-report=term tests/

# Terminal with missing lines
pytest --cov=src --cov-report=term-missing tests/

# HTML report
pytest --cov=src --cov-report=html tests/

# XML report (for CI/CD)
pytest --cov=src --cov-report=xml tests/

# JSON report
pytest --cov=src --cov-report=json tests/

# Multiple reports
pytest --cov=src --cov-report=term --cov-report=html --cov-report=xml tests/

# No report (just collect data)
pytest --cov=src --cov-report= tests/
```

### Coverage Contexts

**Track which tests cover which code:**
```bash
# Enable context tracking
pytest --cov=src --cov-context=test tests/

# View in HTML report
pytest --cov=src --cov-report=html --cov-context=test tests/
open htmlcov/index.html  # Click lines to see which tests cover them
```

### Configuration in pytest.ini

**Add pytest-cov options to pytest config:**
```ini
[pytest]
addopts =
    --cov=src
    --cov-report=term-missing
    --cov-report=html
    --cov-branch
    --cov-fail-under=80
    -v
```

### Integration with coverage.py

**pytest-cov respects `.coveragerc` settings:**
```bash
# Uses exclusion patterns from .coveragerc
pytest --cov=src tests/

# Override coverage config file
pytest --cov=src --cov-config=custom_coverage.ini tests/
```

### Xdist Support

**Parallel test execution with coverage:**
```bash
# Run tests in parallel across 4 CPUs
pytest --cov=src -n 4 tests/

# Auto-detect CPU count
pytest --cov=src -n auto tests/
```

### Best Practices

1. **Use `--cov-report=term-missing`** - See which lines need tests
2. **Enable branch coverage** - Catch untested conditionals
3. **Set `fail_under` threshold** - Prevent coverage regression
4. **Use contexts for debugging** - Identify redundant tests
5. **Configure in pytest.ini** - Consistent coverage across team

---

## 5. pytest Markers for Test Organization

**Official URL:** https://docs.pytest.org/en/stable/how-to/mark.html
**Custom Markers:** https://docs.pytest.org/en/stable/example/markers.html

### Built-in Markers

| Marker | Purpose | Example |
|--------|---------|---------|
| `skip` | Always skip test | `@pytest.mark.skip(reason="Not implemented")` |
| `skipif` | Conditional skip | `@pytest.mark.skipif(sys.version_info < (3,10), reason="Requires 3.10+")` |
| `xfail` | Expected failure | `@pytest.mark.xfail(raises=ValueError)` |
| `parametrize` | Parametrize test | `@pytest.mark.parametrize("x", [1, 2, 3])` |
| `usefixtures` | Apply fixtures | `@pytest.mark.usefixtures("cleanup")` |
| `filterwarnings` | Manage warnings | `@pytest.mark.filterwarnings("ignore::DeprecationWarning")` |

### Custom Marker Registration

**In `pytest.ini`:**
```ini
[pytest]
markers =
    slow: marks tests as slow (deselect with '-m "not slow"')
    integration: integration tests requiring external services
    unit: unit tests with no external dependencies
    smoke: critical smoke tests
    regression: regression tests for bug fixes
    bdd: BDD tests using pytest-bdd
    domain: domain-level tests
    api: tests requiring API access
    database: tests requiring database
```

**In `pyproject.toml`:**
```toml
[tool.pytest.ini_options]
markers = [
    "slow: marks tests as slow",
    "integration: integration tests",
    "unit: unit tests",
    "smoke: smoke tests",
    "regression: regression tests",
]
```

**Programmatic Registration:**
```python
# conftest.py
def pytest_configure(config):
    config.addinivalue_line("markers", "slow: marks tests as slow")
    config.addinivalue_line("markers", "integration: integration tests")
```

### Using Markers

**Single marker:**
```python
import pytest

@pytest.mark.slow
def test_large_computation():
    """Marked as slow test"""
    result = expensive_operation()
    assert result == expected
```

**Multiple markers:**
```python
@pytest.mark.slow
@pytest.mark.integration
@pytest.mark.database
def test_database_backup():
    """Marked with multiple tags"""
    backup = create_full_backup()
    assert backup.is_valid()
```

**Class-level markers:**
```python
@pytest.mark.integration
class TestAPIEndpoints:
    """All tests in class are marked 'integration'"""

    def test_get_user(self):
        assert api.get("/users/1").status == 200

    def test_create_user(self):
        assert api.post("/users", data={}).status == 201
```

**Module-level markers:**
```python
# test_integration.py
import pytest

pytestmark = pytest.mark.integration  # Apply to all tests in module

def test_feature_one():
    pass

def test_feature_two():
    pass
```

### Test Selection with Markers

**Run specific markers:**
```bash
# Run only 'smoke' tests
pytest -m smoke

# Run only 'unit' tests
pytest -m unit

# Exclude 'slow' tests
pytest -m "not slow"

# Run 'integration' but not 'slow'
pytest -m "integration and not slow"

# Run 'smoke' OR 'regression'
pytest -m "smoke or regression"

# Complex expression
pytest -m "(integration or unit) and not slow"
```

### Strict Marker Checking

**Prevent typos by requiring marker registration:**

```ini
[pytest]
markers =
    slow: slow tests
    integration: integration tests

# Enable strict mode
addopts = --strict-markers
```

```bash
# With --strict-markers, this fails:
@pytest.mark.itegration  # Typo! Not registered
def test_feature():
    pass

# Error: 'itegration' not a registered marker
```

### Marker Limitations

**Important:** "Marks can only be applied to tests, having no effect on fixtures."

```python
# This does NOT work
@pytest.mark.slow  # Ignored on fixtures!
@pytest.fixture
def expensive_setup():
    return setup_database()
```

### Best Practices

1. **Register all markers** - Use `--strict-markers` to catch typos
2. **Document marker purpose** - Add descriptions in pytest.ini
3. **Use hierarchical markers** - `@slow` + `@integration` vs `@slow_integration`
4. **Consistent naming** - Choose convention (snake_case vs lowercase)
5. **CI/CD integration** - Different pipelines for different marker groups

---

## 6. BDD Scenario Outline & Parametrization

**Documentation:** https://pytest-bdd.readthedocs.io/en/stable/
**Tutorial:** https://testautomationu.applitools.com/behavior-driven-python-with-pytest-bdd/chapter5.html

### Scenario Outlines with Examples Tables

**Gherkin Feature File:**
```gherkin
Feature: Calculator operations

Scenario Outline: Adding numbers
  Given I have a calculator
  When I add <first> and <second>
  Then the result should be <expected>

Examples:
  | first | second | expected |
  | 2     | 3      | 5        |
  | 10    | 15     | 25       |
  | -5    | 5      | 0        |
  | 100   | -50    | 50       |
```

**Step Definitions:**
```python
from pytest_bdd import scenario, given, when, then, parsers

@scenario('calculator.feature', 'Adding numbers')
def test_addition():
    """Test runs 4 times with different parameter sets"""
    pass

@given("I have a calculator", target_fixture="calculator")
def calculator():
    return Calculator()

@when(parsers.parse("I add {first:d} and {second:d}"))
def add_numbers(calculator, first, second):
    calculator.add(first, second)

@then(parsers.parse("the result should be {expected:d}"))
def verify_result(calculator, expected):
    assert calculator.result == expected
```

### Multiple Example Tables

**Use tags to differentiate example sets:**
```gherkin
Scenario Outline: Temperature conversion
  Given the temperature is <celsius> degrees Celsius
  When I convert to Fahrenheit
  Then the result should be <fahrenheit> degrees

@positive
Examples: Positive temperatures
  | celsius | fahrenheit |
  | 0       | 32         |
  | 100     | 212        |

@negative
Examples: Negative temperatures
  | celsius | fahrenheit |
  | -40     | -40        |
  | -273    | -459.4     |
```

### Combining with pytest.mark.parametrize

**Alternative approach (less BDD-pure):**
```python
import pytest
from pytest_bdd import scenario, given, when, then

@scenario('calculator.feature', 'Basic addition')
@pytest.mark.parametrize("a,b,expected", [
    (2, 3, 5),
    (10, 15, 25),
    (-5, 5, 0),
])
def test_addition(a, b, expected):
    """Parametrize via pytest decorator"""
    pass
```

**Note:** This breaks specification-by-example principle. Prefer Examples tables in Gherkin.

### Vertical vs Horizontal Tables

**Only horizontal tables are supported (official Gherkin):**

```gherkin
# ✅ CORRECT: Horizontal table
Examples:
  | first | second | expected |
  | 2     | 3      | 5        |
  | 10    | 15     | 25       |

# ❌ WRONG: Vertical table (not supported in pytest-bdd 8+)
Examples:
  | first    | 2  | 10 |
  | second   | 3  | 15 |
  | expected | 5  | 25 |
```

### Accessing Example Parameters

**Parameters automatically passed to steps:**
```gherkin
Scenario Outline: User login
  Given a user with username "<username>" and role "<role>"
  When the user logs in
  Then the user should have <permissions> permissions

Examples:
  | username | role  | permissions |
  | admin    | admin | 100         |
  | user1    | user  | 10          |
  | guest    | guest | 1           |
```

```python
@given(parsers.parse('a user with username "{username}" and role "{role}"'))
def create_user(username, role):
    return User(username=username, role=role)

@then(parsers.parse('the user should have {permissions:d} permissions'))
def verify_permissions(create_user, permissions):
    assert create_user.permission_level == permissions
```

### Best Practices

1. **Use Examples for data variations** - Keep scenario logic DRY
2. **Keep examples readable** - Align table columns for clarity
3. **Tag example sets** - Enable selective execution
4. **Limit examples** - Too many rows = harder to debug
5. **Use meaningful names** - `<email>` better than `<val1>`
6. **Avoid pytest parametrize** - Keep examples in Gherkin for BDD purity

---

## 7. Testing Async Code with pytest-asyncio

**Official URL:** https://pytest-asyncio.readthedocs.io/en/latest/
**Latest Version:** 1.2.0+
**GitHub:** https://github.com/pytest-dev/pytest-asyncio

### Installation

```bash
pip install pytest-asyncio
```

### Basic Usage

**Mark async tests with `@pytest.mark.asyncio`:**
```python
import pytest
import asyncio

@pytest.mark.asyncio
async def test_async_function():
    """Test an async function"""
    result = await async_operation()
    assert result == "expected"

@pytest.mark.asyncio
async def test_with_delay():
    """Test with asyncio.sleep"""
    await asyncio.sleep(0.1)
    assert True
```

### Testing Modes

**Strict Mode (default):**
- Requires `@pytest.mark.asyncio` on every async test
- Explicit is better than implicit
- Recommended for mixed sync/async codebases

**Auto Mode:**
- Automatically marks all async tests
- No decorator needed
- Recommended for async-only projects

**Configure in pytest.ini:**
```ini
[pytest]
asyncio_mode = auto  # or "strict"
```

**Or via command line:**
```bash
pytest --asyncio-mode=auto tests/
```

### Event Loop Scopes

**Control event loop lifetime:**
```python
import pytest

@pytest.mark.asyncio(scope="function")  # Default: new loop per test
async def test_function_scope():
    """New event loop for this test"""
    await async_operation()

@pytest.mark.asyncio(scope="module")  # Shared loop per module
async def test_module_scope():
    """Shares event loop with other module-scoped tests"""
    await async_operation()

@pytest.mark.asyncio(scope="session")  # One loop for all tests
async def test_session_scope():
    """Shares event loop across entire test session"""
    await async_operation()
```

### Async Fixtures

**Create async fixtures for setup/teardown:**
```python
import pytest

@pytest.fixture
async def async_client():
    """Async fixture with setup and teardown"""
    client = await AsyncClient.connect()
    yield client
    await client.disconnect()

@pytest.mark.asyncio
async def test_with_async_fixture(async_client):
    """Use async fixture in test"""
    result = await async_client.query("SELECT 1")
    assert result is not None
```

**Fixture scopes work too:**
```python
@pytest.fixture(scope="module")
async def shared_async_resource():
    """Async fixture shared across module"""
    resource = await create_expensive_resource()
    yield resource
    await resource.cleanup()
```

### Testing Async Generators

**Async generators and context managers:**
```python
import pytest
from contextlib import asynccontextmanager

@asynccontextmanager
async def async_resource():
    """Async context manager"""
    resource = await allocate()
    try:
        yield resource
    finally:
        await resource.cleanup()

@pytest.mark.asyncio
async def test_async_context_manager():
    """Test async context manager"""
    async with async_resource() as r:
        result = await r.process()
        assert result is not None
```

### Testing Concurrent Operations

**Test multiple concurrent tasks:**
```python
@pytest.mark.asyncio
async def test_concurrent_requests():
    """Test multiple concurrent async operations"""
    tasks = [
        async_api_call(1),
        async_api_call(2),
        async_api_call(3),
    ]
    results = await asyncio.gather(*tasks)
    assert len(results) == 3
    assert all(r.status == 200 for r in results)
```

### Exception Handling

**Test async exceptions:**
```python
@pytest.mark.asyncio
async def test_async_exception():
    """Test that async function raises expected exception"""
    with pytest.raises(ValueError):
        await async_function_that_raises()
```

### Timeouts

**Add timeouts to async tests:**
```python
@pytest.mark.asyncio
@pytest.mark.timeout(5)  # Requires pytest-timeout
async def test_with_timeout():
    """Test must complete within 5 seconds"""
    await potentially_slow_operation()
```

### Best Practices

1. **Use auto mode for async-only projects** - Reduces boilerplate
2. **Match fixture scope to event loop scope** - Avoid scope conflicts
3. **Test concurrent operations** - Catch race conditions
4. **Use async context managers** - Proper resource cleanup
5. **Add timeouts** - Prevent hanging tests
6. **Avoid blocking calls** - No `time.sleep()` in async code

### Limitations

**unittest classes not supported:**
```python
# ❌ This does NOT work
class TestAsync(unittest.TestCase):
    @pytest.mark.asyncio
    async def test_async(self):
        pass

# ✅ Use this instead
class TestAsync:
    @pytest.mark.asyncio
    async def test_async(self):
        pass
```

---

## 8. pytest Plugins for Enhanced Testing

### pytest-mock

**Official URL:** https://pytest-mock.readthedocs.io/en/latest/
**PyPI:** https://pypi.org/project/pytest-mock/

**Installation:**
```bash
pip install pytest-mock
```

**Key Features:**
- Thin wrapper around `unittest.mock`
- Automatic cleanup after tests
- `mocker` fixture for all mocking operations
- Spy and stub utilities

**Basic Usage:**
```python
def test_os_remove(mocker):
    """Mock os.remove to test file deletion"""
    mocker.patch('os.remove')

    UnixFS.rm('file.txt')

    os.remove.assert_called_once_with('file.txt')
```

**Common Methods:**
```python
# Patch object
mocker.patch.object(SomeClass, 'method', return_value=42)

# Patch multiple
mocker.patch.multiple('module', attr1=value1, attr2=value2)

# Patch dict
mocker.patch.dict('os.environ', {'API_KEY': 'test_key'})

# Spy (call real method + track calls)
spy = mocker.spy(SomeClass, 'method')

# Stub (accepts any arguments)
stub = mocker.stub(name='feature_stub')

# Stop all patches
mocker.stopall()

# Reset all mocks
mocker.resetall()
```

**Advanced Example:**
```python
def test_api_client(mocker):
    """Mock HTTP requests"""
    mock_response = mocker.Mock()
    mock_response.status_code = 200
    mock_response.json.return_value = {'data': 'test'}

    mocker.patch('requests.get', return_value=mock_response)

    client = APIClient()
    result = client.fetch_data('https://api.example.com/data')

    assert result['data'] == 'test'
    requests.get.assert_called_once_with('https://api.example.com/data')
```

### pytest-timeout

**PyPI:** https://pypi.org/project/pytest-timeout/

**Installation:**
```bash
pip install pytest-timeout
```

**Usage:**
```python
@pytest.mark.timeout(5)  # Seconds
def test_slow_operation():
    """Fail if takes longer than 5 seconds"""
    result = slow_computation()
    assert result is not None

@pytest.mark.timeout(0)  # Disable timeout for this test
def test_no_timeout():
    """No timeout on this test"""
    very_slow_operation()
```

**Global timeout in pytest.ini:**
```ini
[pytest]
timeout = 300  # 5 minute default timeout
timeout_method = thread  # or "signal"
```

### pytest-xdist

**GitHub:** https://github.com/pytest-dev/pytest-xdist

**Installation:**
```bash
pip install pytest-xdist
```

**Parallel test execution:**
```bash
# Run on 4 CPUs
pytest -n 4

# Auto-detect CPU count
pytest -n auto

# With coverage (pytest-cov integration)
pytest --cov=src -n auto tests/
```

### pytest-benchmark

**PyPI:** https://pypi.org/project/pytest-benchmark/

**Installation:**
```bash
pip install pytest-benchmark
```

**Usage:**
```python
def test_performance(benchmark):
    """Benchmark function performance"""
    result = benchmark(expensive_function, arg1, arg2)
    assert result is not None
```

### pytest-sugar

**PyPI:** https://pypi.org/project/pytest-sugar/

**Installation:**
```bash
pip install pytest-sugar
```

**Features:**
- Progress bar for test execution
- Instant failure display
- Colored output
- No configuration needed - just install and run

---

## 9. Coverage Exclusion Patterns & Pragmas

**Official URL:** https://coverage.readthedocs.io/en/latest/excluding.html

### Pragma Comments

**Line Exclusion:**
```python
def debug_only():
    print("Debug info")  # pragma: no cover

if DEBUG:  # pragma: no cover
    # Entire block excluded
    enable_debugging()
    setup_debug_logging()
```

**Branch Exclusion:**
```python
def process(value):
    if value is None:  # pragma: no branch
        # Branch never taken in tests, but needed for safety
        raise ValueError("Value cannot be None")
    return process_value(value)
```

**File Exclusion:**
```python
# At top of file
# pragma: exclude file

# Entire file excluded from coverage
```

### Default Exclusion Patterns

Coverage.py automatically excludes:

1. `# pragma: no cover` (and variations)
2. `# pragma: no branch`
3. `if TYPE_CHECKING:`
4. Lines with only `...`
5. `if False:` and `if True:` (compile-time constants)

### Custom Exclusion Patterns

**Add custom regex patterns in `.coveragerc`:**
```ini
[report]
exclude_lines =
    # Default pragmas
    pragma: no cover
    pragma: no branch

    # Don't complain about missing debug code
    def __repr__
    def __str__

    # Don't complain if tests don't hit defensive assertion code
    raise AssertionError
    raise NotImplementedError

    # Don't complain about abstract methods
    @abstractmethod
    @abc.abstractmethod

    # Don't complain about type checking code
    if TYPE_CHECKING:

    # Don't complain about script entry points
    if __name__ == .__main__.:

    # Don't complain about protocol/interface methods
    \.\.\.

    # Don't complain about safety checks
    assert False
    raise NotImplementedError
```

### Multi-line Regex Exclusions

**Since coverage.py 7.6.0:**
```ini
[report]
exclude_also =
    # Exclude entire try/except blocks with specific pattern
    try:(?s:.)except ImportError:(?s:.)+?^(?=[^ ])

    # Exclude comment-delimited regions
    # no cover: start(?s:.)# no cover: stop

    # Exclude specific exception patterns
    except SpecificError:(?s:.)+?(?=except|finally|^[^ ])
```

### Partial Branch Exclusion

**Exclude specific branches while keeping line coverage:**
```python
def safe_divide(a, b):
    if b == 0:  # pragma: no branch
        # We don't test divide-by-zero, but keep the guard
        return 0
    return a / b
```

### Best Practices

1. **Use regex patterns for common exclusions** - Avoid pragma clutter
2. **Document why code is excluded** - Future maintainability
3. **Be specific with patterns** - Avoid accidentally excluding real code
4. **Minimize exclusions** - Every exclusion is untested code
5. **Review exclusions regularly** - Remove when code becomes testable

### Common Exclusion Scenarios

**Debug/Development Code:**
```python
if DEBUG:  # pragma: no cover
    print(f"Debug: {variable}")
```

**Abstract Base Classes:**
```python
class BaseHandler(ABC):
    @abstractmethod  # pragma: no cover
    def handle(self):
        """Subclasses must implement"""
        pass
```

**Type Checking Imports:**
```python
from typing import TYPE_CHECKING

if TYPE_CHECKING:  # Automatically excluded
    from expensive_module import HeavyType
```

**Script Entry Points:**
```python
if __name__ == "__main__":  # pragma: no cover
    main()
```

**Platform-Specific Code:**
```python
if sys.platform == 'win32':  # pragma: no cover
    # Windows-specific code
    setup_windows()
```

---

## 10. CI/CD Integration for Coverage Enforcement

### GitHub Actions Workflow

**Example `.github/workflows/test.yml`:**
```yaml
name: Tests and Coverage

on:
  push:
    branches: [main, develop]
  pull_request:
    branches: [main]

jobs:
  test:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        python-version: ['3.10', '3.11', '3.12']

    steps:
    - uses: actions/checkout@v4

    - name: Set up Python ${{ matrix.python-version }}
      uses: actions/setup-python@v5
      with:
        python-version: ${{ matrix.python-version }}

    - name: Install dependencies
      run: |
        python -m pip install --upgrade pip
        pip install -r tests/requirements.txt
        pip install -e .

    - name: Run tests with coverage
      run: |
        pytest --cov=src \
               --cov-report=term-missing \
               --cov-report=xml \
               --cov-report=html \
               --cov-branch \
               --cov-fail-under=80 \
               -v tests/

    - name: Upload coverage to Codecov
      uses: codecov/codecov-action@v4
      with:
        file: ./coverage.xml
        flags: unittests
        name: coverage-${{ matrix.python-version }}
        fail_ci_if_error: true

    - name: Upload coverage HTML report
      uses: actions/upload-artifact@v4
      with:
        name: coverage-report-${{ matrix.python-version }}
        path: htmlcov/

    - name: Comment coverage on PR
      if: github.event_name == 'pull_request'
      uses: py-cov-action/python-coverage-comment-action@v3
      with:
        GITHUB_TOKEN: ${{ github.token }}
        MINIMUM_GREEN: 80
        MINIMUM_ORANGE: 70
```

### Quality Gates

**Per-file coverage enforcement:**
```yaml
    - name: Check per-file coverage
      run: |
        coverage report --fail-under=80 --skip-empty

        # Ensure no file drops below threshold
        coverage json
        python scripts/check_per_file_coverage.py coverage.json 80
```

**`scripts/check_per_file_coverage.py`:**
```python
#!/usr/bin/env python3
import json
import sys

def check_coverage(coverage_file, threshold):
    with open(coverage_file) as f:
        data = json.load(f)

    failed_files = []
    for filename, stats in data['files'].items():
        coverage = stats['summary']['percent_covered']
        if coverage < threshold:
            failed_files.append((filename, coverage))

    if failed_files:
        print(f"❌ {len(failed_files)} files below {threshold}% coverage:")
        for filename, coverage in failed_files:
            print(f"  {filename}: {coverage:.1f}%")
        sys.exit(1)
    else:
        print(f"✅ All files meet {threshold}% coverage threshold")

if __name__ == "__main__":
    check_coverage(sys.argv[1], float(sys.argv[2]))
```

### Coverage Badges

**Using Codecov:**
```markdown
[![codecov](https://codecov.io/gh/username/repo/branch/main/graph/badge.svg)](https://codecov.io/gh/username/repo)
```

**Using shields.io:**
```markdown
![Coverage](https://img.shields.io/endpoint?url=https://gist.githubusercontent.com/username/gist-id/raw/coverage-badge.json)
```

### Matrix Testing

**Test across multiple configurations:**
```yaml
    strategy:
      matrix:
        os: [ubuntu-latest, windows-latest, macos-latest]
        python-version: ['3.10', '3.11', '3.12']
        exclude:
          # Skip some combinations if needed
          - os: macos-latest
            python-version: '3.10'
```

### Coverage Reports as PR Comments

**Using pytest-coverage-comment:**
```yaml
    - name: Coverage comment
      if: github.event_name == 'pull_request'
      uses: py-cov-action/python-coverage-comment-action@v3
      with:
        GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        MINIMUM_GREEN: 80
        MINIMUM_ORANGE: 70
        ANNOTATE_MISSING_LINES: true
        ANNOTATION_TYPE: warning
```

### Local Coverage Makefile

**`Makefile` for local development:**
```makefile
.PHONY: test coverage coverage-html coverage-report

test:
	pytest -v tests/

coverage:
	pytest --cov=src \
	       --cov-report=term-missing \
	       --cov-branch \
	       --cov-fail-under=80 \
	       tests/

coverage-html:
	pytest --cov=src \
	       --cov-report=html \
	       --cov-branch \
	       tests/
	@echo "Opening coverage report..."
	@open htmlcov/index.html || xdg-open htmlcov/index.html

coverage-report:
	pytest --cov=src \
	       --cov-report=term-missing \
	       --cov-report=html \
	       --cov-report=xml \
	       --cov-branch \
	       tests/
	@echo "Coverage reports generated:"
	@echo "  - Terminal: stdout"
	@echo "  - HTML: htmlcov/index.html"
	@echo "  - XML: coverage.xml"
```

### Pre-commit Hooks

**`.pre-commit-config.yaml`:**
```yaml
repos:
  - repo: local
    hooks:
      - id: pytest-coverage
        name: pytest coverage check
        entry: pytest --cov=src --cov-fail-under=80 tests/
        language: system
        pass_filenames: false
        always_run: true
```

### Best Practices

1. **Set realistic thresholds** - Start at current level, increase gradually
2. **Fail CI on coverage drop** - Prevent regression
3. **Test on multiple platforms** - Catch platform-specific issues
4. **Generate multiple reports** - Terminal + HTML + XML for different uses
5. **Comment on PRs** - Visibility for code reviewers
6. **Track trends over time** - Use Codecov/Coveralls dashboards
7. **Per-file enforcement** - Catch localized coverage drops
8. **Local coverage checks** - Fast feedback before CI

---

## Current Project Configuration

**Based on analysis of `/Users/andres.camacho/Development/Personal/spacetradersV2/bot`:**

### Existing Setup

**`pytest.ini`:**
```ini
[pytest]
testpaths = tests
python_files = test_*.py
python_classes = Test*
python_functions = test_* regression_*
norecursedirs = htmlcov data graphs legacy features .git __pycache__ .pytest_cache
addopts = -q

markers =
    bdd: BDD tests using pytest-bdd with Gherkin scenarios
    unit: Unit-level BDD tests (migrated from tests/unit/)
    domain: Domain-level BDD tests (migrated from tests/domain/)
    regression: Regression tests for bug fixes
```

**`tests/requirements.txt`:**
```
pytest>=7.0.0
pytest-bdd>=6.0.0
pytest-cov>=4.0.0
psutil>=5.9.0
```

**`pyproject.toml`:**
```toml
[project.optional-dependencies]
dev = [
    "pytest>=7.0.0",
    "pytest-bdd>=6.0.0",
    "pytest-cov>=4.0.0",
]
```

### Recommendations for Improvement

1. **Add coverage configuration** - Create `.coveragerc` or add to `pyproject.toml`
2. **Enable branch coverage** - Track conditional logic coverage
3. **Set coverage threshold** - Start with current baseline, increase gradually
4. **Add exclusion patterns** - Abstract methods, debug code, type checking blocks
5. **Configure pytest-cov in pytest.ini** - Consistent coverage reporting
6. **Add GitHub Actions workflow** - Automated coverage enforcement
7. **Generate multiple report formats** - Terminal, HTML, XML for CI
8. **Add coverage badge** - Visual indicator in README
9. **Per-file coverage checks** - Ensure comprehensive coverage across codebase
10. **Local Makefile targets** - Easy coverage checks during development

---

## Quick Reference

### Essential Commands

```bash
# Run tests with coverage
pytest --cov=src tests/

# Generate HTML report
pytest --cov=src --cov-report=html tests/

# Fail if below threshold
pytest --cov=src --cov-fail-under=80 tests/

# Show missing lines
pytest --cov=src --cov-report=term-missing tests/

# Branch coverage
pytest --cov=src --cov-branch tests/

# Select tests by marker
pytest -m unit tests/

# Exclude slow tests
pytest -m "not slow" tests/

# Run in parallel
pytest -n auto tests/

# With timeout
pytest --timeout=300 tests/

# Verbose output
pytest -v tests/
```

### Essential URLs

| Resource | URL |
|----------|-----|
| pytest-bdd docs | https://pytest-bdd.readthedocs.io/en/latest/ |
| pytest fixtures | https://docs.pytest.org/en/stable/how-to/fixtures.html |
| pytest parametrize | https://docs.pytest.org/en/stable/how-to/parametrize.html |
| pytest markers | https://docs.pytest.org/en/stable/how-to/mark.html |
| coverage.py config | https://coverage.readthedocs.io/en/latest/config.html |
| coverage.py excluding | https://coverage.readthedocs.io/en/latest/excluding.html |
| pytest-cov | https://pytest-cov.readthedocs.io/en/latest/ |
| pytest-asyncio | https://pytest-asyncio.readthedocs.io/en/latest/ |
| pytest-mock | https://pytest-mock.readthedocs.io/en/latest/ |

---

## Next Steps for Coverage Improvement

1. **Establish baseline** - Run coverage analysis on current codebase
2. **Create coverage config** - Add `.coveragerc` or `[tool.coverage]` to `pyproject.toml`
3. **Set initial threshold** - Use current coverage % as baseline
4. **Add exclusion patterns** - Exclude unrealistic coverage targets
5. **Configure pytest-cov** - Add to `pytest.ini` for consistent reporting
6. **Implement CI checks** - GitHub Actions workflow with coverage gates
7. **Add coverage badge** - Visual indicator in README
8. **Incremental improvement** - Raise threshold 5% at a time
9. **Per-file enforcement** - Catch coverage gaps in new code
10. **Team education** - Share best practices and coverage strategies

---

**End of Documentation Research Report**
