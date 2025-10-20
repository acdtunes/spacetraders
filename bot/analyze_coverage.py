#!/usr/bin/env python3
"""Analyze code coverage and generate comprehensive report."""

import json
from pathlib import Path
from collections import defaultdict

def load_coverage_data():
    """Load coverage.json file."""
    with open('coverage.json', 'r') as f:
        return json.load(f)

def categorize_modules(files):
    """Categorize modules by package."""
    categories = defaultdict(list)

    for filepath, data in files.items():
        if not filepath.startswith('src/spacetraders_bot'):
            continue

        parts = Path(filepath).parts
        if len(parts) < 3:
            continue

        # Extract category (core, operations, helpers, etc.)
        category = parts[2]

        summary = data.get('summary', {})
        categories[category].append({
            'file': Path(filepath).name,
            'path': filepath,
            'stmts': summary.get('num_statements', 0),
            'miss': summary.get('missing_lines', 0),
            'cover': summary.get('percent_covered', 0)
        })

    return categories

def calculate_category_stats(modules):
    """Calculate aggregate stats for a category."""
    total_stmts = sum(m['stmts'] for m in modules)
    total_miss = sum(m['miss'] for m in modules)

    if total_stmts == 0:
        return 0, 0, 0

    total_covered = total_stmts - total_miss
    percent = (total_covered / total_stmts) * 100

    return total_stmts, total_miss, percent

def identify_critical_gaps(categories):
    """Identify critical files with low coverage."""
    critical = []

    for category, modules in categories.items():
        for module in modules:
            # Critical if >100 statements and <30% coverage
            if module['stmts'] > 100 and module['cover'] < 30:
                critical.append({
                    **module,
                    'category': category
                })

    return sorted(critical, key=lambda x: x['stmts'], reverse=True)

def identify_well_tested(categories):
    """Identify well-tested modules."""
    well_tested = []

    for category, modules in categories.items():
        for module in modules:
            # Well-tested if >50 statements and >80% coverage
            if module['stmts'] > 50 and module['cover'] >= 80:
                well_tested.append({
                    **module,
                    'category': category
                })

    return sorted(well_tested, key=lambda x: x['cover'], reverse=True)

def main():
    """Generate coverage report."""
    data = load_coverage_data()
    files = data.get('files', {})
    totals = data.get('totals', {})

    print("=" * 80)
    print("CODE COVERAGE REPORT")
    print("=" * 80)
    print()

    # Overall summary
    print("📊 OVERALL COVERAGE")
    print("-" * 80)
    print(f"Total Statements:  {totals.get('num_statements', 0):,}")
    print(f"Missing Lines:     {totals.get('missing_lines', 0):,}")
    print(f"Covered Lines:     {totals.get('covered_lines', 0):,}")
    print(f"Coverage:          {totals.get('percent_covered', 0):.1f}%")
    print()

    # Category breakdown
    categories = categorize_modules(files)

    print("📦 COVERAGE BY PACKAGE")
    print("-" * 80)
    print(f"{'Package':<25} {'Statements':>12} {'Missing':>12} {'Coverage':>12}")
    print("-" * 80)

    category_stats = {}
    for category in sorted(categories.keys()):
        modules = categories[category]
        stmts, miss, percent = calculate_category_stats(modules)
        category_stats[category] = (stmts, miss, percent)
        print(f"{category:<25} {stmts:>12,} {miss:>12,} {percent:>11.1f}%")

    print()

    # Critical gaps
    critical = identify_critical_gaps(categories)

    print("🔴 CRITICAL COVERAGE GAPS (>100 statements, <30% coverage)")
    print("-" * 80)
    if critical:
        print(f"{'File':<40} {'Category':<15} {'Stmts':>8} {'Cover':>8}")
        print("-" * 80)
        for module in critical[:15]:  # Top 15
            print(f"{module['file']:<40} {module['category']:<15} {module['stmts']:>8} {module['cover']:>7.1f}%")
    else:
        print("✅ No critical coverage gaps!")
    print()

    # Well-tested modules
    well_tested = identify_well_tested(categories)

    print("✅ WELL-TESTED MODULES (>50 statements, >80% coverage)")
    print("-" * 80)
    if well_tested:
        print(f"{'File':<40} {'Category':<15} {'Stmts':>8} {'Cover':>8}")
        print("-" * 80)
        for module in well_tested[:10]:  # Top 10
            print(f"{module['file']:<40} {module['category']:<15} {module['stmts']:>8} {module['cover']:>7.1f}%")
    else:
        print("No well-tested modules found.")
    print()

    # Recommendations
    print("💡 RECOMMENDATIONS")
    print("-" * 80)

    if category_stats:
        # Find lowest coverage category
        lowest_cat = min(category_stats.items(), key=lambda x: x[1][2])
        print(f"1. Priority: Improve '{lowest_cat[0]}' coverage ({lowest_cat[1][2]:.1f}%)")

        # Count critical gaps by category
        gap_counts = defaultdict(int)
        for module in critical:
            gap_counts[module['category']] += 1

        if gap_counts:
            top_gap_cat = max(gap_counts.items(), key=lambda x: x[1])
            print(f"2. Focus: '{top_gap_cat[0]}' has {top_gap_cat[1]} critical gaps")

        # Overall target
        overall_coverage = totals.get('percent_covered', 0)
        if overall_coverage < 50:
            print(f"3. Target: Increase overall coverage from {overall_coverage:.1f}% to 50%")
        elif overall_coverage < 80:
            print(f"3. Target: Increase overall coverage from {overall_coverage:.1f}% to 80%")
        else:
            print(f"3. Maintain: Keep coverage above 80% (currently {overall_coverage:.1f}%)")

    print()
    print("=" * 80)
    print("Report generated successfully!")
    print("HTML report: htmlcov/index.html")
    print("=" * 80)

if __name__ == '__main__':
    main()
