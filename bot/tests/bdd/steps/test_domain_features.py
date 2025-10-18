import subprocess
import sys
from pathlib import Path

import pytest
from pytest_bdd import scenarios, when, then, parsers


FEATURES_DIR = Path(__file__).resolve().parents[2] / "features"
REPO_ROOT = FEATURES_DIR.parent
DOMAIN_ROOT = REPO_ROOT / "domain"
UNIT_ROOT = REPO_ROOT / "unit"

# Auto-discover all feature files so pytest-bdd registers them.
for feature_path in FEATURES_DIR.glob("*.feature"):
    scenarios(str(feature_path))


def _run_pytest_on_module(domain: str, module: str):
    """Execute a pytest run for the given domain test module in a subprocess."""
    if domain == "unit":
        module_path = UNIT_ROOT / Path(module)
    else:
        module_path = DOMAIN_ROOT / domain / Path(module)

    if not module_path.exists():
        raise FileNotFoundError(f"Domain module not found: {module_path}")

    process = subprocess.run(
        [sys.executable, "-m", "pytest", str(module_path), "-q"],
        capture_output=True,
        text=True,
        cwd=REPO_ROOT.parent,
    )
    return process


@pytest.fixture
def domain_execution():
    """Mutable container for sharing subprocess results between steps."""
    return {}


@when(parsers.parse('I execute the "{domain}" domain module "{module}"'))
def execute_domain_module(domain_execution, domain: str, module: str):
    """Run the requested domain test module via pytest."""
    result = _run_pytest_on_module(domain, module)
    domain_execution["result"] = result


@then("the module should pass")
def assert_module_success(domain_execution):
    """Validate the subprocess exit code and bubble up stdout/stderr on failure."""
    result = domain_execution.get("result")
    assert result is not None, "No domain execution result recorded"

    if result.returncode != 0:
        debug_output = (
            f"Pytest run failed with code {result.returncode}\n"
            f"--- stdout ---\n{result.stdout}\n"
            f"--- stderr ---\n{result.stderr}"
        )
        pytest.fail(debug_output)
