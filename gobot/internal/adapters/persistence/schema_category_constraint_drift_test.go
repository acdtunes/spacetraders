package persistence_test

import (
	"os"
	"regexp"
	"sort"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
)

// This file is the durable schema-drift gate for the category_is_f_type
// CHECK constraint (migration 039), the functional-dependency sibling of the enum
// IN(...) gate in schema_enum_drift_test.go.
//
// THE CONSTRAINT. category is a pure, deterministic relabel of transaction_type
// (ledger.TypeToCategoryMap): category = f(transaction_type). Migration 039 encodes
// that map as a CHECK ( category = CASE transaction_type WHEN '<type>' THEN
// '<category>' ... END ). It is NOT the CHECK(<column> IN (...)) enum shape the enum
// gate discovers, so that gate neither covers nor is broken by it — the two gates are
// deliberately disjoint by DDL shape.
//
// THE DEFECT CLASS IT GUARDS. The SQL CASE duplicates a Go map, and the two can
// drift. The drift is especially treacherous because a Postgres CHECK holds whenever
// its expression is NULL, and a CASE with no matching WHEN returns NULL:
//
//   - Add a 7th (type -> category) to TypeToCategoryMap without extending the CASE:
//     NewTransaction writes the new type, the CASE returns NULL, `category = NULL`
//     is NULL, and the CHECK PASSES. The invariant is SILENTLY unenforced for that
//     type — no error, no signal, exactly the "one-off patch, no standing guard"
//     recurrence the enum gate was built to stop.
//   - Give the CASE a branch that disagrees with the domain: every write of that
//     type is rejected with SQLSTATE 23514 (check_violation) in production.
//
// Both are caught here at unit time by asserting the parsed CASE mapping equals
// ledger.TypeToCategoryMap exactly, in both directions. Pure test-layer: it parses
// the SQL migration and reads the domain map. Needs no database — and could not use
// one, since the SQLite test DB is built by GORM AutoMigrate, which does not create
// this constraint (only the migration does; Postgres enforces it at deploy).

// whenThenRe extracts the `WHEN '<TYPE>' THEN '<CATEGORY>'` pairs from the CASE body.
// Whitespace between tokens is arbitrary, so the migration may align the arms.
var whenThenRe = regexp.MustCompile(`(?i)WHEN\s+'([A-Z0-9_]+)'\s+THEN\s+'([A-Z0-9_]+)'`)

// categoryConstraintDefRe locates the opening paren of the category_is_f_type CHECK
// body. It matches only `category_is_f_type CHECK (` (the ADD line), never the
// `DROP CONSTRAINT IF EXISTS category_is_f_type` line, which has no following `CHECK (`.
var categoryConstraintDefRe = regexp.MustCompile(`(?is)category_is_f_type\s+CHECK\s*\(`)

// TestCategoryConstraintMirrorsDomainMapping is the primary drift gate. It parses the
// effective category_is_f_type CHECK constraint and asserts its CASE mapping equals
// ledger.TypeToCategoryMap exactly. A failure means migration 039's enforced mapping
// and the domain's source-of-truth map have drifted apart — reconcile them in a new
// migration (see the file header for the two failure modes).
func TestCategoryConstraintMirrorsDomainMapping(t *testing.T) {
	constraint := effectiveCategoryConstraintMapping(t)
	domain := domainCategoryMapping()

	// Direction 1 — every domain mapping must be encoded by the constraint.
	for _, txType := range sortedStringKeys(domain) {
		got, ok := constraint[txType]
		if !ok {
			t.Errorf("transaction type %q (domain category %q) has no WHEN branch in the "+
				"category_is_f_type CHECK constraint; a write of that type would be SILENTLY unenforced "+
				"(a Postgres CHECK holds when its expression is NULL, and the CASE returns NULL for an "+
				"unlisted type) — add a migration extending the CASE to mirror ledger.TypeToCategoryMap",
				txType, domain[txType])
			continue
		}
		if got != domain[txType] {
			t.Errorf("category_is_f_type maps %q -> %q but ledger.TypeToCategoryMap maps it -> %q; "+
				"the constraint would reject every %q write with SQLSTATE 23514 — reconcile the migration "+
				"CASE with the domain map", txType, got, domain[txType], txType)
		}
	}

	// Direction 2 — every constraint branch must correspond to a real domain mapping,
	// so a stale or invented branch cannot silently outlive the domain constant.
	for _, txType := range sortedStringKeys(constraint) {
		if _, ok := domain[txType]; !ok {
			t.Errorf("category_is_f_type has a WHEN branch for %q but ledger.TypeToCategoryMap has no such "+
				"transaction type; drop the stale branch (or restore the domain constant) so the enforced "+
				"mapping matches the source of truth", txType)
		}
	}
}

// TestCategoryConstraintDriftGateHasTeeth proves the detector fires on a seeded drift,
// so a green primary test means "no drift" rather than "the check is inert". It
// exercises the same domainTypesMissingFromConstraint helper the primary gate uses for
// its silent-non-enforcement direction, with a domain type deliberately absent from the
// constraint mapping.
func TestCategoryConstraintDriftGateHasTeeth(t *testing.T) {
	constraint := map[string]string{"REFUEL": "FUEL_COSTS"}
	domain := map[string]string{"REFUEL": "FUEL_COSTS", "SELL_CARGO": "TRADING_REVENUE"}

	got := domainTypesMissingFromConstraint(constraint, domain)

	if len(got) != 1 || got[0] != "SELL_CARGO" {
		t.Fatalf("category drift detector failed to catch seeded drift: want [SELL_CARGO], got %v", got)
	}
}

// domainTypesMissingFromConstraint returns the domain transaction types that have no
// branch in the constraint mapping. A non-empty result is the silent-non-enforcement
// direction: those types would slip past the CHECK (NULL CASE result).
func domainTypesMissingFromConstraint(constraint, domain map[string]string) []string {
	var missing []string
	for txType := range domain {
		if _, ok := constraint[txType]; !ok {
			missing = append(missing, txType)
		}
	}
	sort.Strings(missing)
	return missing
}

// domainCategoryMapping renders ledger.TypeToCategoryMap as string->string. This is the
// source-of-truth oracle: NewTransaction derives every persisted category from it, so
// the migration's CASE must mirror it exactly.
func domainCategoryMapping() map[string]string {
	mapping := make(map[string]string, len(ledger.TypeToCategoryMap))
	for txType, category := range ledger.TypeToCategoryMap {
		mapping[string(txType)] = string(category)
	}
	return mapping
}

// effectiveCategoryConstraintMapping parses the WHEN/THEN pairs of the category_is_f_type
// CHECK constraint, resolving to the highest-numbered migration that (re)defines it —
// mirroring the enum gate's "latest definition wins" rule, since a future migration may
// DROP and re-ADD the constraint with a revised CASE.
func effectiveCategoryConstraintMapping(t *testing.T) map[string]string {
	t.Helper()

	bestNum := -1
	var bestBody string
	for _, path := range upMigrationPaths(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		content := stripSQLLineComments(string(raw))
		loc := categoryConstraintDefRe.FindStringIndex(content)
		if loc == nil {
			continue
		}
		openParen := loc[1] - 1 // the '(' consumed by the regex
		body, _, ok := balancedParens(content, openParen)
		if !ok {
			t.Fatalf("%s: unbalanced parentheses after category_is_f_type CHECK", path)
		}
		if num := migrationNumber(t, path); num > bestNum {
			bestNum, bestBody = num, body
		}
	}

	if bestNum == -1 {
		t.Fatal("no migration defines the category_is_f_type CHECK constraint")
	}

	mapping := make(map[string]string)
	for _, m := range whenThenRe.FindAllStringSubmatch(bestBody, -1) {
		mapping[m[1]] = m[2]
	}
	if len(mapping) == 0 {
		t.Fatalf("parsed no WHEN/THEN pairs from the effective category_is_f_type constraint: %q", bestBody)
	}
	return mapping
}

// sortedStringKeys returns the sorted keys of a string map, for deterministic output.
func sortedStringKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
