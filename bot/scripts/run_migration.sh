#!/bin/bash
# Run migration on all files using mock repositories

echo "Finding files with mock repositories..."
files=$(grep -l "mock_ship_repo\|mock_player_repo" tests/bdd/steps/**/*.py 2>/dev/null)
count=$(echo "$files" | wc -l | tr -d ' ')

echo "Found $count files to migrate"
echo ""

# Counter
modified=0
manual_review=0

# Migrate each file
for file in $files; do
    echo "========================================="
    echo "Migrating: $file"
    echo "========================================="

    output=$(uv run python scripts/migrate_test_fixtures.py --apply "$file" 2>&1)

    echo "$output" | grep -E "WARNING|MANUAL|Applied|No changes"

    if echo "$output" | grep -q "Applied changes"; then
        ((modified++))
    fi

    if echo "$output" | grep -q "MANUAL REVIEW"; then
        ((manual_review++))
    fi

    echo ""
done

echo "========================================="
echo "MIGRATION COMPLETE"
echo "========================================="
echo "Files modified: $modified"
echo "Files needing manual review: $manual_review"
