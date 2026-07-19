package persistence_test

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"gorm.io/gorm/schema"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

// This file is the durable COLUMN-level schema-drift gate, the 42703
// (undefined_column) sibling of the 23514 enum gate in schema_enum_drift_test.go.
//
// The defect class: a GORM model grows a persisted column, but no hand-written
// migration ever creates it. In production the daemon runs AutoMigrate at boot
// ("models are the source of truth, AutoMigrate is additive" — see
// cmd/spacetraders-daemon/main.go), and AutoMigrate failure is NON-FATAL: it
// "logs loudly and continues on the existing schema". So a model column with no
// migration reaches prod backed only by that best-effort boot reconcile — the
// exact gap the 2026-07-03 reserved-column P0 fell through. This gate makes the
// migration the proof, so a checkable table's columns never depend on AutoMigrate
// having succeeded.
//
// WHAT IS CHECKABLE, AND WHY IT IS NARROWER THAN "every table"
// -----------------------------------------------------------
// The migrations are NOT a complete schema — they are a patch set for what
// AutoMigrate cannot do (renames, drops, CHECK/FK constraints, type changes).
// Many base tables (players, waypoints, containers, ships, market_data, ...) were
// born from AutoMigrate and have NO CREATE TABLE anywhere in the migration
// history; migrations only ever ALTER them. For such a table the migrations
// define no baseline column set, so "every model column must appear in
// migrations" is not a well-posed question — AutoMigrate owns the baseline.
//
// Therefore a model's table is CHECKABLE iff a migration CREATE TABLEs it (the
// 12 tables that carry a migration-owned baseline: transactions, goods_factories,
// market_price_history, the manufacturing_* family, gas_operations,
// storage_operations, captain_events, eras). For those, every model column must
// be backed by that CREATE TABLE plus the ALTER ... ADD COLUMN history. Tables
// with no CREATE in the migrations are AutoMigrate-managed and are exempted
// AUTOMATICALLY (the `created` set drives it — nothing is hand-listed), so a new
// migration-created table becomes checkable the moment its CREATE TABLE lands.
//
// Pure test-layer: parses the SQL migrations and reflects the GORM models via
// gorm.io/gorm/schema. Needs no database. The SQLite test DB is built by
// AutoMigrate, which would happily create any model column and so can never
// surface this drift — only parsing the hand-written migrations can.

// modelColumnSet reflects one registered GORM model into its table name and the
// set of database column names it persists, via the same schema parser GORM's
// AutoMigrate uses. Association struct fields (e.g. Player *PlayerModel) carry no
// column of their own and are skipped; their foreign-key columns are separate,
// explicitly-tagged fields and are included.
func modelColumnSet(t *testing.T, cache *sync.Map, model any) (string, map[string]struct{}) {
	t.Helper()
	s, err := schema.Parse(model, cache, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("gorm schema.Parse(%T): %v", model, err)
	}
	cols := make(map[string]struct{})
	for _, f := range s.Fields {
		// DBName == "" is an association / non-column field; IgnoreMigration
		// fields are not persisted by AutoMigrate either, so a migration is not
		// expected to define them.
		if f.DBName == "" || f.IgnoreMigration {
			continue
		}
		cols[f.DBName] = struct{}{}
	}
	return s.Table, cols
}

// TestModelColumnsBackedByMigrations is the primary column-drift gate. For every
// registered model whose table is CREATE'd by a migration, it asserts each
// persisted column exists in the effective migration column set (CREATE TABLE +
// ALTER ADD COLUMN, minus DROP COLUMN, following renames). A failure means a
// write touching that column could hit SQLSTATE 42703 (undefined_column) on a
// production database whose boot AutoMigrate did not (or could not) add it — add
// an ALTER TABLE ... ADD COLUMN migration.
func TestModelColumnsBackedByMigrations(t *testing.T) {
	migCols, created := effectiveMigrationColumns(t)
	cache := &sync.Map{}

	for _, model := range persistence.AllModels() {
		table, cols := modelColumnSet(t, cache, model)
		if _, ok := created[table]; !ok {
			// AutoMigrate-managed table (no migration CREATE baseline) — exempt.
			continue
		}
		defined := migCols[table]
		for _, col := range sortedKeys(cols) {
			if _, ok := defined[col]; !ok {
				t.Errorf("model column %s.%s is persisted by GORM but no migration defines it "+
					"(CREATE TABLE + ALTER ADD COLUMN history); a write touching it risks SQLSTATE 42703 "+
					"undefined_column if boot AutoMigrate did not add it — add an ALTER TABLE %s ADD COLUMN %s migration",
					table, col, table, col)
			}
		}
	}
}

// TestColumnDriftGateHasTeeth proves the detection logic fires on a seeded drift,
// so a green primary test means "no drift" rather than "the check is inert". It
// exercises the same set-difference the primary gate uses, with a column
// deliberately absent from the migration-defined set.
func TestColumnDriftGateHasTeeth(t *testing.T) {
	defined := map[string]struct{}{"id": {}, "player_id": {}}
	model := map[string]struct{}{"id": {}, "player_id": {}, "orphan_column": {}}

	var missing []string
	for _, col := range sortedKeys(model) {
		if _, ok := defined[col]; !ok {
			missing = append(missing, col)
		}
	}

	if len(missing) != 1 || missing[0] != "orphan_column" {
		t.Fatalf("column drift detector failed to catch seeded drift: want [orphan_column], got %v", missing)
	}
}

// TestColumnDriftParserFindsCreatedTables pins the migration parser: it must
// discover the migration-created tables and their landmark columns. This is the
// analog of the enum gate's highest-migration pin — if a parser regression stops
// finding CREATE TABLEs or ALTER ADD COLUMNs, the primary gate would pass
// vacuously (nothing checkable), so this test fails loudly instead.
func TestColumnDriftParserFindsCreatedTables(t *testing.T) {
	migCols, created := effectiveMigrationColumns(t)

	// A representative, stable subset of the migration-created tables. Not
	// exhaustive on purpose: new CREATE TABLE migrations should not have to touch
	// this test, but these anchors must never silently disappear.
	wantCreated := []string{
		"transactions", "goods_factories", "market_price_history",
		"manufacturing_pipelines", "manufacturing_tasks",
		"gas_operations", "storage_operations", "captain_events", "eras",
	}
	for _, table := range wantCreated {
		if _, ok := created[table]; !ok {
			t.Errorf("parser did not detect CREATE TABLE for %q; the column-drift gate would skip it as exempt", table)
		}
	}

	// Landmark columns proving both CREATE-body parsing (net_profit from 018's
	// manufacturing_pipelines body) and ALTER ADD COLUMN parsing (operation_type
	// added to transactions by 012, pipeline_type added to pipelines by 020).
	assertHasColumn(t, migCols, "manufacturing_pipelines", "net_profit")
	assertHasColumn(t, migCols, "manufacturing_pipelines", "pipeline_type")
	assertHasColumn(t, migCols, "transactions", "operation_type")
	assertHasColumn(t, migCols, "goods_factories", "estimated_speedup") // 010 multi-ADD
}

func assertHasColumn(t *testing.T, migCols map[string]map[string]struct{}, table, col string) {
	t.Helper()
	if _, ok := migCols[table][col]; !ok {
		t.Errorf("parser missing column %s.%s; expected it from the migration history (parser regression)", table, col)
	}
}

// --- migration DDL parser -------------------------------------------------
//
// Builds the effective column set per table by replaying the up-migrations in
// numeric order and applying CREATE TABLE / ALTER ADD COLUMN / DROP COLUMN /
// RENAME COLUMN / RENAME TO. Only the shapes this migration corpus actually uses
// are handled; the parser is deliberately corpus-specific, not a general SQL
// parser. A table is reported in `created` iff a CREATE TABLE established it.

var (
	createTableRe  = regexp.MustCompile(`(?is)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?"?(\w+)"?\s*\(`)
	alterTableRe   = regexp.MustCompile(`(?is)ALTER\s+TABLE\s+(?:ONLY\s+)?"?(\w+)"?(.*?);`)
	addColumnRe    = regexp.MustCompile(`(?is)ADD\s+COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?"?(\w+)"?`)
	dropColumnRe   = regexp.MustCompile(`(?is)DROP\s+COLUMN\s+(?:IF\s+EXISTS\s+)?"?(\w+)"?`)
	renameColumnRe = regexp.MustCompile(`(?is)RENAME\s+COLUMN\s+"?(\w+)"?\s+TO\s+"?(\w+)"?`)
	renameTableRe  = regexp.MustCompile(`(?is)RENAME\s+TO\s+"?(\w+)"?`)
	firstIdentRe   = regexp.MustCompile(`^\s*"?([A-Za-z_][A-Za-z0-9_]*)"?`)
)

// tableLevelKeywords are the first tokens of a CREATE TABLE body item that denote
// a table-level constraint rather than a column definition.
var tableLevelKeywords = map[string]struct{}{
	"CONSTRAINT": {}, "PRIMARY": {}, "FOREIGN": {}, "UNIQUE": {},
	"CHECK": {}, "EXCLUDE": {}, "LIKE": {}, "INDEX": {},
}

// effectiveMigrationColumns returns, per table, the set of columns the migrations
// leave defined, plus the set of tables that a CREATE TABLE established (the
// checkable tables). Migrations are replayed in numeric-then-filename order.
func effectiveMigrationColumns(t *testing.T) (cols map[string]map[string]struct{}, created map[string]struct{}) {
	t.Helper()

	cols = make(map[string]map[string]struct{})
	created = make(map[string]struct{})

	ensure := func(table string) map[string]struct{} {
		set, ok := cols[table]
		if !ok {
			set = make(map[string]struct{})
			cols[table] = set
		}
		return set
	}

	for _, path := range sortedMigrationPaths(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		content := stripSQLLineComments(string(raw))

		// 1. CREATE TABLE — establishes a table and its baseline columns.
		for _, loc := range createTableRe.FindAllStringSubmatchIndex(content, -1) {
			table := content[loc[2]:loc[3]]
			openParen := loc[1] - 1 // the '(' consumed by the regex
			body, _, ok := balancedParens(content, openParen)
			if !ok {
				t.Fatalf("%s: unbalanced parentheses after CREATE TABLE %s", path, table)
			}
			created[table] = struct{}{}
			set := ensure(table)
			for _, col := range createTableColumns(body) {
				set[col] = struct{}{}
			}
		}

		// 2. ALTER TABLE — ADD/DROP/RENAME COLUMN and RENAME TO.
		for _, m := range alterTableRe.FindAllStringSubmatch(content, -1) {
			table, clause := m[1], m[2]

			if rc := renameColumnRe.FindStringSubmatch(clause); rc != nil {
				set := ensure(table)
				delete(set, rc[1])
				set[rc[2]] = struct{}{}
				continue
			}
			if strings.Contains(strings.ToUpper(clause), "RENAME") {
				if rt := renameTableRe.FindStringSubmatch(clause); rt != nil {
					newName := rt[1]
					if old, ok := cols[table]; ok {
						cols[newName] = old
						delete(cols, table)
					} else {
						ensure(newName)
					}
					if _, ok := created[table]; ok {
						created[newName] = struct{}{}
						delete(created, table)
					}
					continue
				}
			}

			set := ensure(table)
			for _, a := range addColumnRe.FindAllStringSubmatch(clause, -1) {
				set[a[1]] = struct{}{}
			}
			for _, d := range dropColumnRe.FindAllStringSubmatch(clause, -1) {
				delete(set, d[1])
			}
		}
	}

	return cols, created
}

// createTableColumns extracts column names from a CREATE TABLE body, splitting on
// top-level commas (so type args, REFERENCES lists, and CHECK(... IN (...)) lists
// stay intact) and skipping items whose first token is a table-level constraint
// keyword.
func createTableColumns(body string) []string {
	var out []string
	for _, item := range splitTopLevelCommas(body) {
		m := firstIdentRe.FindStringSubmatch(item)
		if m == nil {
			continue
		}
		if _, isConstraint := tableLevelKeywords[strings.ToUpper(m[1])]; isConstraint {
			continue
		}
		out = append(out, m[1])
	}
	return out
}

// balancedParens returns the substring between the '(' at index open and its
// matching ')', respecting single-quoted string literals so quoted parens/commas
// do not disturb nesting.
func balancedParens(s string, open int) (body string, end int, ok bool) {
	depth := 0
	inStr := false
	for i := open; i < len(s); i++ {
		c := s[i]
		if inStr {
			if c == '\'' {
				inStr = false
			}
			continue
		}
		switch c {
		case '\'':
			inStr = true
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[open+1 : i], i, true
			}
		}
	}
	return "", 0, false
}

// splitTopLevelCommas splits a CREATE TABLE body on commas that sit at paren
// depth 0 and outside single-quoted string literals.
func splitTopLevelCommas(body string) []string {
	var items []string
	depth := 0
	inStr := false
	start := 0
	for i := 0; i < len(body); i++ {
		c := body[i]
		if inStr {
			if c == '\'' {
				inStr = false
			}
			continue
		}
		switch c {
		case '\'':
			inStr = true
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				items = append(items, body[start:i])
				start = i + 1
			}
		}
	}
	return append(items, body[start:])
}

// stripSQLLineComments removes "-- ..." to end of line. The migration corpus
// contains no "--" inside string literals, so this line-oriented strip is safe.
func stripSQLLineComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// sortedMigrationPaths returns the up-migration paths in numeric-then-filename
// order, so replayed ALTERs (renames, drops) apply after the CREATE they follow.
func sortedMigrationPaths(t *testing.T) []string {
	t.Helper()
	paths := upMigrationPaths(t) // reused from schema_enum_drift_test.go
	sort.Slice(paths, func(i, j int) bool {
		ni, nj := migrationNumber(t, paths[i]), migrationNumber(t, paths[j])
		if ni != nj {
			return ni < nj
		}
		return paths[i] < paths[j]
	})
	return paths
}

// sortedKeys returns the sorted keys of a set, for deterministic failure output.
func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
