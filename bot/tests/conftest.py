#!/usr/bin/env python3
"""
Pytest configuration for BDD tests
"""

import sys
from pathlib import Path

PROJECT_ROOT = Path(__file__).parent.parent
SRC_DIR = PROJECT_ROOT / "src"
sys.path.insert(0, str(SRC_DIR))
sys.path.insert(0, str(Path(__file__).parent))

# Import package to register compatibility shims for legacy module names
import spacetraders_bot  # noqa: F401  # pylint: disable=unused-import

# pytest-bdd configuration
def pytest_bdd_step_error(request, feature, scenario, step, step_func, step_func_args, exception):
    """Custom error handling for BDD steps"""
    print(f"\n❌ Step failed: {step.keyword} {step.name}")
    print(f"   Feature: {feature.name}")
    print(f"   Scenario: {scenario.name}")
    print(f"   Error: {exception}")
