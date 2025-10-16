from __future__ import annotations

import inspect
from importlib import import_module
from typing import Any, Callable, Dict

import pytest
from pytest_bdd import parsers, then, when


@pytest.fixture
def regression_context() -> Dict[str, Any]:
    """Shared context for regression execution."""
    return {}


@pytest.fixture
def call_with_fixtures(request):
    """Invoke a callable with pytest-managed fixtures based on its signature."""

    def _call(func: Callable[..., Any]):
        signature = inspect.signature(func)
        kwargs = {
            name: request.getfixturevalue(name) for name in signature.parameters
        }
        return func(**kwargs)

    return _call


@when(parsers.parse('I execute regression "{module_path}" "{callable_name}"'))
def when_execute_regression(
    module_path: str,
    callable_name: str,
    regression_context: Dict[str, Any],
    call_with_fixtures,
) -> None:
    """Import the regression callable and execute it with fixtures."""
    module = import_module(module_path)

    target: Any = module
    for part in callable_name.split("."):
        attr = getattr(target, part)
        if inspect.isclass(attr):
            # Instantiate the class so we can call bound instance methods.
            target = attr()
        else:
            target = attr

    func = target
    regression_context["callable"] = f"{module_path}.{callable_name}"

    try:
        regression_context["result"] = call_with_fixtures(func)
        regression_context["exception"] = None
    except Exception as exc:  # noqa: BLE001 - stored for re-raising in Then step
        regression_context["result"] = None
        regression_context["exception"] = exc


@then("the regression completes successfully")
def then_regression_completes(regression_context: Dict[str, Any]) -> None:
    """Ensure the regression callable finished without raising."""
    exc = regression_context.get("exception")
    if exc is not None:
        callable_id = regression_context.get("callable", "<unknown>")
        raise AssertionError(f"Regression {callable_id} failed") from exc
