#!/usr/bin/env python3
"""
Pytest configuration for BDD tests
"""

import sys
from pathlib import Path

# Add lib directory to path
sys.path.insert(0, str(Path(__file__).parent.parent / "lib"))
sys.path.insert(0, str(Path(__file__).parent))

# pytest-bdd configuration
def pytest_bdd_step_error(request, feature, scenario, step, step_func, step_func_args, exception):
    """Custom error handling for BDD steps"""
    print(f"\n❌ Step failed: {step.keyword} {step.name}")
    print(f"   Feature: {feature.name}")
    print(f"   Scenario: {scenario.name}")
    print(f"   Error: {exception}")
