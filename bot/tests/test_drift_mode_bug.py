"""
Simple unit test to demonstrate the DRIFT mode bug.

This test directly verifies that the opportunistic refuel logic has the `not is_last_segment` condition
which prevents refueling before the final segment - a critical bug.
"""
import pytest


def test_opportunistic_refuel_excludes_last_segment():
    """
    FAILING TEST: Demonstrates Bug #1 - opportunistic refuel excludes final segment.

    The navigate_ship.py code at line 548-549 has:
        if (current_waypoint.has_fuel and fuel_percentage < 0.9 and
            not segment.requires_refuel and not is_last_segment):

    The `not is_last_segment` condition prevents refueling when ship arrives at a
    fuel station that happens to be the second-to-last waypoint, even with low fuel.

    This test will PASS once we remove `not is_last_segment` from line 549.
    """
    from pathlib import Path

    # Read the navigate_ship.py file
    nav_file = Path(__file__).parent.parent / "src" / "application" / "navigation" / "commands" / "navigate_ship.py"
    content = nav_file.read_text()

    # Find the opportunistic refuel logic (around line 548-549)
    # The buggy line contains: "not segment.requires_refuel and not is_last_segment"
    buggy_pattern = "not segment.requires_refuel and not is_last_segment"
    fixed_pattern_1 = "not segment.requires_refuel):"  # Fixed version (no is_last_segment check)
    fixed_pattern_2 = "not segment.requires_refuel and"  # Partial match if on separate lines

    # This test FAILS if the buggy pattern exists
    assert buggy_pattern not in content, (
        f"Bug #1 detected: Opportunistic refuel excludes last segment!\n"
        f"File: {nav_file}\n"
        f"Line 548-549 contains: '{buggy_pattern}'\n"
        f"This prevents ships from refueling at fuel stations before the final segment.\n"
        f"\n"
        f"Expected: Ships should refuel whenever fuel < 90% at a fuel station\n"
        f"Actual: Ships skip refueling if it's the segment before the destination\n"
        f"\n"
        f"Fix: Remove 'and not is_last_segment' from line 549"
    )


def test_no_pre_departure_refuel_check():
    """
    FAILING TEST: Demonstrates Bug #2 - no pre-departure fuel check.

    The navigate_ship.py code sets flight mode (line 491) without checking if the ship
    should refuel first when at a fuel station with low fuel.

    This test checks if there's a pre-departure refuel check before line 491.
    It will PASS once we add the pre-departure check.
    """
    from pathlib import Path

    # Read the navigate_ship.py file
    nav_file = Path(__file__).parent.parent / "src" / "application" / "navigation" / "commands" / "navigate_ship.py"
    content = nav_file.read_text()

    lines = content.split('\n')

    # Find the line with set_flight_mode
    set_flight_mode_line = None
    for i, line in enumerate(lines):
        if 'api_client.set_flight_mode' in line and 'segment.flight_mode' in line:
            set_flight_mode_line = i
            break

    assert set_flight_mode_line is not None, "Could not find set_flight_mode line"

    # Check if there's a pre-departure refuel check within 40 lines before set_flight_mode
    # Look for "Pre-departure refuel" or DRIFT mode check
    pre_departure_check_exists = False
    for i in range(max(0, set_flight_mode_line - 40), set_flight_mode_line):
        line = lines[i]
        if 'Pre-departure refuel' in line or ('DRIFT' in line and 'fuel_percentage < 0.9' in line):
            pre_departure_check_exists = True
            break

    assert pre_departure_check_exists, (
        f"Bug #2 detected: No pre-departure refuel check!\n"
        f"File: {nav_file}\n"
        f"Line {set_flight_mode_line + 1}: api_client.set_flight_mode() is called\n"
        f"But no pre-departure refuel check exists before this line.\n"
        f"\n"
        f"Expected: Check if ship is at fuel station with <90% fuel before departure\n"
        f"Actual: Ship blindly follows pre-planned flight mode without checking current fuel\n"
        f"\n"
        f"Fix: Add pre-departure refuel check before setting flight mode"
    )


if __name__ == '__main__':
    pytest.main([__file__, '-v'])
