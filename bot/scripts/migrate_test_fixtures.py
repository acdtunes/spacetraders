#!/usr/bin/env python3
"""
CAREFUL test fixture migration script.

Migrates test files from mock repositories to real repositories.
Run in DRY-RUN mode first to review changes!

Usage:
    # Dry run (shows changes, doesn't modify files)
    python scripts/migrate_test_fixtures.py --dry-run tests/bdd/steps/application/shipyard/test_purchase_ship_steps.py

    # Apply changes to single file
    python scripts/migrate_test_fixtures.py tests/bdd/steps/application/shipyard/test_purchase_ship_steps.py

    # Apply to all test files
    python scripts/migrate_test_fixtures.py --all
"""
import re
import sys
import argparse
from pathlib import Path
from typing import List, Tuple


class TestFixtureMigrator:
    """Carefully migrates test files from mocks to real repositories"""

    def __init__(self, dry_run: bool = True):
        self.dry_run = dry_run
        self.changes = []

    def migrate_file(self, file_path: Path) -> bool:
        """
        Migrate a single test file.

        Returns:
            True if changes were made, False otherwise
        """
        if not file_path.exists():
            print(f"❌ File not found: {file_path}")
            return False

        content = file_path.read_text()
        original_content = content

        # Apply migrations in order (careful sequencing!)
        content = self._migrate_fixture_parameters(content)
        content = self._migrate_mock_player_repo_usage(content)
        content = self._migrate_mock_ship_repo_usage(content)

        if content == original_content:
            print(f"✅ No changes needed: {file_path}")
            return False

        # Show diff
        self._show_diff(file_path, original_content, content)

        # Apply changes if not dry-run
        if not self.dry_run:
            file_path.write_text(content)
            print(f"✅ Applied changes: {file_path}")
        else:
            print(f"⚠️  DRY RUN: Would modify {file_path}")

        return True

    def _migrate_fixture_parameters(self, content: str) -> str:
        """
        Replace mock fixture parameters with real ones.

        Before: def handler(mock_ship_repo, mock_player_repo):
        After:  def handler(ship_repo, player_repo):
        """
        # Replace function parameters
        content = re.sub(
            r'\bmock_ship_repo\b',
            'ship_repo',
            content
        )
        content = re.sub(
            r'\bmock_player_repo\b',
            'player_repo',
            content
        )

        return content

    def _migrate_mock_player_repo_usage(self, content: str) -> str:
        """
        Migrate MockPlayerRepository usage to real PlayerRepository.

        This is straightforward because PlayerRepository interface matches!
        The mock had the SAME methods as the real repo.
        """
        # No additional changes needed - parameter rename handles this
        # Real PlayerRepository has create(), find_by_id(), etc.
        return content

    def _migrate_mock_ship_repo_usage(self, content: str) -> str:
        """
        Migrate MockShipRepository usage to context['ships_data'] pattern.

        CAREFUL: ShipRepository is API-only, has NO create() method!

        Before:
            ship = create_ship(...)
            mock_ship_repo.create(ship)

        After:
            ship = create_ship(...)
            # Store ship for API mock
            if 'ships_data' not in context:
                context['ships_data'] = {}
            context['ships_data'][ship.ship_symbol] = {
                'symbol': ship.ship_symbol,
                'nav': {...},
                'fuel': {...},
                ...
            }

        BUT this is too complex for regex! We need manual review.
        """
        # Check if file has mock_ship_repo.create() calls
        if re.search(r'ship_repo\.create\(', content):
            print("⚠️  WARNING: File contains ship_repo.create() calls!")
            print("   These need MANUAL migration to context['ships_data']")
            print("   ShipRepository has NO create() method in production!")
            self.changes.append(("MANUAL_REVIEW", "ship_repo.create() usage"))

        # Check for ship_repo.update() calls
        if re.search(r'ship_repo\.update\(', content):
            print("⚠️  WARNING: File contains ship_repo.update() calls!")
            print("   These need MANUAL migration - ShipRepository is API-only!")
            self.changes.append(("MANUAL_REVIEW", "ship_repo.update() usage"))

        # Check for ship_repo.delete() calls
        if re.search(r'ship_repo\.delete\(', content):
            print("⚠️  WARNING: File contains ship_repo.delete() calls!")
            print("   These need MANUAL migration - ShipRepository is API-only!")
            self.changes.append(("MANUAL_REVIEW", "ship_repo.delete() usage"))

        return content

    def _show_diff(self, file_path: Path, original: str, modified: str):
        """Show unified diff of changes"""
        print(f"\n{'='*80}")
        print(f"CHANGES FOR: {file_path}")
        print('='*80)

        # Simple line-by-line diff
        original_lines = original.split('\n')
        modified_lines = modified.split('\n')

        for i, (orig, mod) in enumerate(zip(original_lines, modified_lines), 1):
            if orig != mod:
                print(f"Line {i}:")
                print(f"  - {orig}")
                print(f"  + {mod}")

        print('='*80)


def find_test_files(base_path: Path = Path("tests/bdd/steps")) -> List[Path]:
    """Find all test step files"""
    return list(base_path.rglob("test_*.py"))


def main():
    parser = argparse.ArgumentParser(description="Migrate test fixtures from mocks to real repositories")
    parser.add_argument("files", nargs="*", help="Test files to migrate")
    parser.add_argument("--all", action="store_true", help="Migrate all test files")
    parser.add_argument("--dry-run", action="store_true", default=True, help="Show changes without applying (default)")
    parser.add_argument("--apply", action="store_true", help="Apply changes (disables dry-run)")

    args = parser.parse_args()

    # Determine dry-run mode
    dry_run = not args.apply

    if dry_run:
        print("🔍 DRY RUN MODE - No files will be modified")
        print("   Use --apply to actually make changes")
        print()

    # Determine files to migrate
    if args.all:
        files = find_test_files()
        print(f"Found {len(files)} test files to migrate")
    elif args.files:
        files = [Path(f) for f in args.files]
    else:
        parser.print_help()
        return 1

    # Migrate files
    migrator = TestFixtureMigrator(dry_run=dry_run)
    modified_count = 0
    manual_review_needed = []

    for file_path in files:
        print(f"\n{'='*80}")
        print(f"Processing: {file_path}")
        print('='*80)

        if migrator.migrate_file(file_path):
            modified_count += 1

            # Check if manual review needed
            if any(change[0] == "MANUAL_REVIEW" for change in migrator.changes):
                manual_review_needed.append(file_path)

            migrator.changes = []  # Reset for next file

    # Summary
    print(f"\n{'='*80}")
    print("MIGRATION SUMMARY")
    print('='*80)
    print(f"Total files processed: {len(files)}")
    print(f"Files modified: {modified_count}")
    print(f"Files needing manual review: {len(manual_review_needed)}")

    if manual_review_needed:
        print("\n⚠️  FILES REQUIRING MANUAL REVIEW:")
        for file_path in manual_review_needed:
            print(f"   - {file_path}")
        print("\n   These files use ship_repo.create/update/delete which don't exist!")
        print("   Must manually migrate to context['ships_data'] pattern.")

    if dry_run:
        print("\n🔍 This was a DRY RUN - no files were modified")
        print("   Use --apply to actually make changes")

    return 0


if __name__ == "__main__":
    sys.exit(main())
