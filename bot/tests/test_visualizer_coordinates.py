#!/usr/bin/env python3
"""
Debug visualizer coordinate handling for X1-VH85 tour.

The cached tour appears to have crossings in the visualizer but geometric
analysis shows 0 crossings. This suggests the visualizer is not using the
correct waypoint coordinates.
"""

import json
import math
from typing import Dict


# Actual database coordinates
WAYPOINTS_DB = {
    "X1-VH85-A1": {"x": 19.0, "y": 15.0},
    "X1-VH85-A2": {"x": 19.0, "y": 15.0},
    "X1-VH85-A3": {"x": 19.0, "y": 15.0},
    "X1-VH85-A4": {"x": 19.0, "y": 15.0},
    "X1-VH85-AC5D": {"x": -14.0, "y": 22.0},
    "X1-VH85-B6": {"x": 149.0, "y": 119.0},
    "X1-VH85-B7": {"x": 337.0, "y": 76.0},
    "X1-VH85-D48": {"x": 2.0, "y": 87.0},
    "X1-VH85-D49": {"x": 2.0, "y": 87.0},
    "X1-VH85-D50": {"x": 2.0, "y": 87.0},
    "X1-VH85-D51": {"x": 2.0, "y": 87.0},
    "X1-VH85-E53": {"x": 34.0, "y": -42.0},
    "X1-VH85-E54": {"x": 34.0, "y": -42.0},
    "X1-VH85-F55": {"x": 24.0, "y": 72.0},
    "X1-VH85-F56": {"x": 24.0, "y": 72.0},
    "X1-VH85-G58": {"x": -50.0, "y": -43.0},
    "X1-VH85-H59": {"x": -36.0, "y": 24.0},
    "X1-VH85-H60": {"x": -36.0, "y": 24.0},
    "X1-VH85-H61": {"x": -36.0, "y": 24.0},
    "X1-VH85-H62": {"x": -36.0, "y": 24.0},
}


CACHED_TOUR_ORDER = [
    "X1-VH85-A1",
    "X1-VH85-AC5D",
    "X1-VH85-H59",
    "X1-VH85-H62",
    "X1-VH85-H61",
    "X1-VH85-H60",
    "X1-VH85-G58",
    "X1-VH85-E53",
    "X1-VH85-E54",
    "X1-VH85-B7",
    "X1-VH85-B6",
    "X1-VH85-F55",
    "X1-VH85-F56",
    "X1-VH85-D51",
    "X1-VH85-D50",
    "X1-VH85-D49",
    "X1-VH85-D48",
    "X1-VH85-A4",
    "X1-VH85-A3",
    "X1-VH85-A2",
    "X1-VH85-A1",
]


def test_unique_coordinates():
    """Check which waypoints share coordinates."""
    coord_groups = {}
    for wp, data in WAYPOINTS_DB.items():
        coord = (data["x"], data["y"])
        if coord not in coord_groups:
            coord_groups[coord] = []
        coord_groups[coord].append(wp)

    print("\n" + "=" * 80)
    print("WAYPOINT COORDINATE GROUPS")
    print("=" * 80)

    for coord, waypoints in sorted(coord_groups.items()):
        if len(waypoints) > 1:
            print(f"\nCoordinate {coord}:")
            for wp in waypoints:
                print(f"  - {wp}")

    unique_coords = len([g for g in coord_groups.values() if len(g) == 1])
    duplicate_coords = len(coord_groups) - unique_coords
    total_waypoints = len(WAYPOINTS_DB)

    print(f"\n{unique_coords} unique coordinate groups")
    print(f"{duplicate_coords} coordinate groups with multiple waypoints")
    print(f"{total_waypoints} total waypoints")


def test_tour_leg_distances():
    """Print distance for each leg of the cached tour."""
    print("\n" + "=" * 80)
    print("CACHED TOUR LEG DISTANCES")
    print("=" * 80)
    print(f"{'From':<15} {'To':<15} {'Distance':>10} {'Same Coords':>12}")
    print("-" * 80)

    total_distance = 0.0
    for i in range(len(CACHED_TOUR_ORDER) - 1):
        from_wp = CACHED_TOUR_ORDER[i]
        to_wp = CACHED_TOUR_ORDER[i + 1]
        from_coord = WAYPOINTS_DB[from_wp]
        to_coord = WAYPOINTS_DB[to_wp]

        dx = to_coord["x"] - from_coord["x"]
        dy = to_coord["y"] - from_coord["y"]
        dist = math.hypot(dx, dy)
        total_distance += dist

        same_coords = "YES" if (from_coord["x"] == to_coord["x"] and from_coord["y"] == to_coord["y"]) else ""

        print(f"{from_wp:<15} {to_wp:<15} {dist:>10.2f} {same_coords:>12}")

    print("-" * 80)
    print(f"{'TOTAL':<31} {total_distance:>10.2f}")
    print("=" * 80)


def test_visualizer_data_format():
    """Show what data format the visualizer should receive."""
    print("\n" + "=" * 80)
    print("EXPECTED VISUALIZER DATA FORMAT")
    print("=" * 80)

    # What the visualizer should receive
    tour_data = {
        "order": CACHED_TOUR_ORDER,
        "coordinates": {
            wp: {"x": data["x"], "y": data["y"]}
            for wp, data in WAYPOINTS_DB.items()
        },
        "total_distance": sum(
            math.hypot(
                WAYPOINTS_DB[CACHED_TOUR_ORDER[i + 1]]["x"] - WAYPOINTS_DB[CACHED_TOUR_ORDER[i]]["x"],
                WAYPOINTS_DB[CACHED_TOUR_ORDER[i + 1]]["y"] - WAYPOINTS_DB[CACHED_TOUR_ORDER[i]]["y"]
            )
            for i in range(len(CACHED_TOUR_ORDER) - 1)
        )
    }

    print(json.dumps(tour_data, indent=2))


if __name__ == "__main__":
    test_unique_coordinates()
    test_tour_leg_distances()
    test_visualizer_data_format()
