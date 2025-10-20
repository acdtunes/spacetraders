#!/usr/bin/env python3
"""Analyze BDD scenarios for overlaps and duplicates."""

import os
import re
from collections import defaultdict
from pathlib import Path

def extract_scenarios(feature_file):
    """Extract scenario names and their context from a feature file."""
    scenarios = []
    with open(feature_file, 'r') as f:
        content = f.read()

    # Extract feature name
    feature_match = re.search(r'^Feature:\s*(.+)$', content, re.MULTILINE)
    feature_name = feature_match.group(1).strip() if feature_match else "Unknown"

    # Extract scenarios
    scenario_pattern = r'^\s*Scenario:\s*(.+)$'
    for match in re.finditer(scenario_pattern, content, re.MULTILINE):
        scenario_name = match.group(1).strip()
        scenarios.append({
            'file': feature_file,
            'feature': feature_name,
            'scenario': scenario_name,
            'normalized': normalize_scenario_name(scenario_name)
        })

    return scenarios

def normalize_scenario_name(name):
    """Normalize scenario name for comparison."""
    # Remove test IDs, numbers, specific values
    normalized = name.lower()
    # Remove common prefixes
    normalized = re.sub(r'^(test_|bug_|fix_|regression_)', '', normalized)
    # Remove version numbers, IDs
    normalized = re.sub(r'[_\-]\d+', '', normalized)
    # Remove parenthetical notes
    normalized = re.sub(r'\([^)]+\)', '', normalized)
    # Normalize whitespace
    normalized = ' '.join(normalized.split())
    return normalized

def find_similar_scenarios(scenarios, similarity_threshold=0.8):
    """Find scenarios with similar names."""
    from difflib import SequenceMatcher

    similar_groups = []
    checked = set()

    for i, s1 in enumerate(scenarios):
        if i in checked:
            continue

        group = [s1]
        for j, s2 in enumerate(scenarios[i+1:], start=i+1):
            if j in checked:
                continue

            # Calculate similarity
            ratio = SequenceMatcher(None, s1['normalized'], s2['normalized']).ratio()

            if ratio >= similarity_threshold:
                group.append(s2)
                checked.add(j)

        if len(group) > 1:
            similar_groups.append(group)
            checked.add(i)

    return similar_groups

def find_duplicate_scenarios(scenarios):
    """Find exact duplicate scenario names."""
    scenario_map = defaultdict(list)

    for s in scenarios:
        key = s['normalized']
        scenario_map[key].append(s)

    duplicates = {k: v for k, v in scenario_map.items() if len(v) > 1}
    return duplicates

def analyze_domain_overlaps(scenarios):
    """Analyze overlaps between different domains."""
    domain_scenarios = defaultdict(list)

    for s in scenarios:
        # Extract domain from file path
        path_parts = Path(s['file']).parts
        if 'features' in path_parts:
            idx = path_parts.index('features')
            if idx + 1 < len(path_parts):
                domain = path_parts[idx + 1]
                domain_scenarios[domain].append(s)

    return domain_scenarios

def main():
    """Main analysis."""
    feature_dir = Path('tests/bdd/features')

    # Collect all scenarios
    all_scenarios = []
    for feature_file in feature_dir.rglob('*.feature'):
        all_scenarios.extend(extract_scenarios(feature_file))

    print(f"📊 Total Scenarios: {len(all_scenarios)}")
    print(f"📁 Total Feature Files: {len(list(feature_dir.rglob('*.feature')))}")
    print()

    # Find exact duplicates
    duplicates = find_duplicate_scenarios(all_scenarios)

    if duplicates:
        print("🔴 EXACT DUPLICATES (normalized names):")
        print("=" * 80)
        for norm_name, scenarios in sorted(duplicates.items()):
            print(f"\n'{scenarios[0]['scenario']}'")
            print(f"  Normalized: {norm_name}")
            for s in scenarios:
                domain = Path(s['file']).parts[Path(s['file']).parts.index('features') + 1]
                print(f"  - {domain}/{Path(s['file']).name}")
        print()
    else:
        print("✅ No exact duplicate scenarios found")
        print()

    # Find similar scenarios (potential overlaps)
    similar_groups = find_similar_scenarios(all_scenarios, similarity_threshold=0.75)

    if similar_groups:
        print("🟡 SIMILAR SCENARIOS (75%+ match):")
        print("=" * 80)
        for group in similar_groups:
            print(f"\nGroup ({len(group)} scenarios):")
            for s in group:
                domain = Path(s['file']).parts[Path(s['file']).parts.index('features') + 1]
                print(f"  - [{domain}] {s['scenario']}")
                print(f"    File: {Path(s['file']).name}")
        print()
    else:
        print("✅ No highly similar scenarios found")
        print()

    # Analyze by domain
    domain_scenarios = analyze_domain_overlaps(all_scenarios)

    print("📁 SCENARIOS BY DOMAIN:")
    print("=" * 80)
    for domain, scenarios in sorted(domain_scenarios.items()):
        print(f"{domain:20} {len(scenarios):3} scenarios")
    print()

    # Look for cross-domain concerns
    print("🔍 POTENTIAL CROSS-DOMAIN OVERLAPS:")
    print("=" * 80)

    # Common patterns to check
    patterns = {
        'navigation': ['navigate', 'route', 'fuel', 'refuel', 'drift', 'cruise'],
        'trading': ['buy', 'sell', 'market', 'price', 'profit', 'trade'],
        'mining': ['mine', 'extract', 'asteroid', 'ore', 'yield'],
        'circuit_breaker': ['circuit', 'breaker', 'spike', 'profitability'],
        'checkpoint': ['checkpoint', 'resume', 'recovery'],
    }

    cross_domain_issues = []

    for pattern_name, keywords in patterns.items():
        domains_with_pattern = defaultdict(list)

        for domain, scenarios in domain_scenarios.items():
            for s in scenarios:
                if any(kw in s['normalized'] for kw in keywords):
                    domains_with_pattern[domain].append(s['scenario'])

        if len(domains_with_pattern) > 1:
            print(f"\n'{pattern_name}' appears in multiple domains:")
            for domain, scenario_list in sorted(domains_with_pattern.items()):
                print(f"  {domain}: {len(scenario_list)} scenarios")

if __name__ == '__main__':
    main()
