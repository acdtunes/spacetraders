"""
Asteroid Finder - Locate alternative asteroids for resource extraction

Handles:
- Resource-to-trait mapping
- System waypoint scanning
- Filtering by asteroid traits
"""

from typing import List

from spacetraders_bot.core.api_client import APIClient


def find_alternative_asteroids(
    api: APIClient,
    system: str,
    current_asteroid: str,
    target_resource: str
) -> List[str]:
    """
    Find alternative asteroids in the system that may contain the target resource

    Args:
        api: API client instance
        system: System symbol (e.g., "X1-HU87")
        current_asteroid: Current asteroid that's not working
        target_resource: Resource we're looking for (e.g., "ALUMINUM_ORE")

    Returns:
        List of alternative asteroid waypoint symbols
    """
    print(f"\n🔍 Searching for alternative asteroids with {target_resource}...")

    # Map resources to asteroid traits
    resource_to_traits = {
        "ALUMINUM_ORE": ["COMMON_METAL_DEPOSITS", "MINERAL_DEPOSITS"],
        "IRON_ORE": ["COMMON_METAL_DEPOSITS"],
        "COPPER_ORE": ["COMMON_METAL_DEPOSITS"],
        "QUARTZ_SAND": ["MINERAL_DEPOSITS", "COMMON_METAL_DEPOSITS"],
        "SILICON_CRYSTALS": ["MINERAL_DEPOSITS", "CRYSTALLINE_STRUCTURES"],
        "PRECIOUS_METAL_DEPOSITS": ["PRECIOUS_METAL_DEPOSITS"],
        "GOLD_ORE": ["PRECIOUS_METAL_DEPOSITS"],
        "SILVER_ORE": ["PRECIOUS_METAL_DEPOSITS"],
        "PLATINUM_ORE": ["PRECIOUS_METAL_DEPOSITS"]
    }

    target_traits = resource_to_traits.get(target_resource, ["COMMON_METAL_DEPOSITS"])
    alternatives = []

    # Get all waypoints in system (paginated)
    page = 1
    while True:
        result = api.list_waypoints(system, limit=20, page=page)
        if not result or 'data' not in result:
            break

        waypoints = result['data']
        if not waypoints:
            break

        for wp in waypoints:
            # Skip current asteroid
            if wp['symbol'] == current_asteroid:
                continue

            # Check if it's an asteroid
            if wp['type'] != 'ASTEROID':
                continue

            # Check traits
            wp_traits = [trait['symbol'] for trait in wp.get('traits', [])]

            # Skip stripped asteroids
            if 'STRIPPED' in wp_traits:
                continue

            # Check if has target traits
            has_target_trait = any(trait in wp_traits for trait in target_traits)
            if has_target_trait:
                alternatives.append(wp['symbol'])
                print(f"   Found: {wp['symbol']} with traits {wp_traits}")

        # Check if there are more pages
        meta = result.get('meta', {})
        if page >= meta.get('total', 1):
            break

        page += 1

    print(f"\n📍 Found {len(alternatives)} alternative asteroids")
    return alternatives
