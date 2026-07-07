package persistence_test

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// This file is the durable schema-drift gate for the sp-8ind defect class,
// generalized from the original valid_task_type-only guard (sp-tvcm).
//
// The defect class: a domain enum grows a value the code persists to a column,
// but the column's CHECK(... IN (...)) constraint is never migrated to accept it,
// so the write fails against Postgres with SQLSTATE 23514 (check_violation). It
// recurred repeatedly (task_type: migrations 024 then 033) because each fix was a
// one-off additive patch with no standing guard. A 23514 is invisible to a
// "launches clean" smoke test — nothing writes the offending value at startup —
// so a unit-level gate that parses the migrations is the only durable protection.
//
// The gate is pure test-layer: it parses the SQL migrations and compares against
// the domain's typed constants. It needs no database. The SQLite-backed test DB
// is built by GORM AutoMigrate, which does NOT create these CHECK constraints —
// only the migrations encode them, which is exactly why the drift can reach
// production untested. Postgres enforces the constraints at deploy; this gate is
// the compile-and-unit-time proof that migrations and domain stay in sync.
//
// Two directions are guarded:
//   - TestDomainEnumsCoveredByMigrationCheckConstraints: every persisted domain
//     value is accepted by its effective CHECK constraint (the 23514 direction).
//   - TestEveryEnumCheckConstraintHasDomainMapping: every enum CHECK constraint
//     the migrations declare is registered here, so a newly-added constraint
//     cannot silently escape the gate.

// enumConstraint pairs a migration-defined CHECK(<column> IN (...)) enum
// constraint with the set of values the code can persist to that column.
type enumConstraint struct {
	// constraint is the SQL constraint name, e.g. "valid_task_status".
	constraint string
	// table and column identify the guarded column (used in failure messages).
	table  string
	column string
	// domain lists every value the code can persist to column. Each entry
	// references a domain typed constant via string(...), so renaming or removing
	// the constant breaks compilation here and forces this table to be revisited.
	domain []string
}

// registeredEnumConstraints is the authoritative mapping of enum CHECK
// constraints to the domain values guarded against drift. Adding a new enum
// CHECK constraint to a migration, or persisting a new domain constant to an
// already-constrained column, requires a matching entry here — otherwise one of
// the two tests below fails.
func registeredEnumConstraints() []enumConstraint {
	return []enumConstraint{
		{
			constraint: "valid_task_type",
			table:      "manufacturing_tasks",
			column:     "task_type",
			domain: []string{
				string(manufacturing.TaskTypeAcquireDeliver),
				string(manufacturing.TaskTypeCollectSell),
				string(manufacturing.TaskTypeLiquidate),
				string(manufacturing.TaskTypeStorageAcquireDeliver),
				string(manufacturing.TaskTypeDeliverToConstruction),
			},
		},
		{
			constraint: "valid_task_status",
			table:      "manufacturing_tasks",
			column:     "status",
			// manufacturing.TaskStatusCancelled ("CANCELLED") is intentionally
			// omitted: it is defined on the type but never persisted to
			// manufacturing_tasks.status. Cancellation happens at the pipeline level
			// (tasks are recycled, not marked CANCELLED); the constant's only
			// repository use is a WHERE ... NOT IN read filter, never a write. The
			// 018 valid_task_status constraint has no CANCELLED, so listing it here
			// would (correctly) turn the gate red. If a code path ever persists
			// CANCELLED to this column, add it here AND extend the constraint.
			domain: []string{
				string(manufacturing.TaskStatusPending),
				string(manufacturing.TaskStatusReady),
				string(manufacturing.TaskStatusAssigned),
				string(manufacturing.TaskStatusExecuting),
				string(manufacturing.TaskStatusCompleted),
				string(manufacturing.TaskStatusFailed),
			},
		},
		{
			constraint: "valid_pipeline_status",
			table:      "manufacturing_pipelines",
			column:     "status",
			domain: []string{
				string(manufacturing.PipelineStatusPlanning),
				string(manufacturing.PipelineStatusExecuting),
				string(manufacturing.PipelineStatusCompleted),
				string(manufacturing.PipelineStatusFailed),
				string(manufacturing.PipelineStatusCancelled),
			},
		},
		{
			constraint: "valid_operation_type",
			table:      "storage_operations",
			column:     "operation_type",
			domain: []string{
				string(storage.OperationTypeGasSiphon),
				string(storage.OperationTypeMining),
				string(storage.OperationTypeCustom),
			},
		},
		{
			constraint: "valid_storage_status",
			table:      "storage_operations",
			column:     "status",
			domain: []string{
				string(storage.OperationStatusPending),
				string(storage.OperationStatusRunning),
				string(storage.OperationStatusCompleted),
				string(storage.OperationStatusStopped),
				string(storage.OperationStatusFailed),
			},
		},
	}
}

// TestDomainEnumsCoveredByMigrationCheckConstraints is the primary drift gate.
// For every registered enum constraint it parses the effective (highest-numbered
// migration) CHECK constraint and asserts that every domain value the code
// persists is accepted. A failure means a write of that value would be rejected
// by Postgres with SQLSTATE 23514 — add a migration extending the constraint.
func TestDomainEnumsCoveredByMigrationCheckConstraints(t *testing.T) {
	for _, ec := range registeredEnumConstraints() {
		ec := ec
		t.Run(ec.constraint, func(t *testing.T) {
			allowed := effectiveConstraintValues(t, ec.constraint)
			for _, missing := range missingDomainValues(allowed, ec.domain) {
				t.Errorf("domain value %q is not accepted by the %s CHECK constraint on %s.%s; "+
					"add a migration extending %s to include it (otherwise the write fails with SQLSTATE 23514)",
					missing, ec.constraint, ec.table, ec.column, ec.constraint)
			}
		})
	}
}

// TestEveryEnumCheckConstraintHasDomainMapping guards the reverse direction: it
// discovers every CHECK(<column> IN (...)) constraint the migrations declare and
// asserts each one is registered in registeredEnumConstraints. This is what makes
// the gate durable — a future migration that adds a new enum constraint without a
// domain mapping fails here, forcing the drift guard to be wired up rather than
// silently omitted (the root cause of the recurring defect class).
func TestEveryEnumCheckConstraintHasDomainMapping(t *testing.T) {
	registered := make(map[string]struct{})
	for _, ec := range registeredEnumConstraints() {
		registered[ec.constraint] = struct{}{}
	}

	discovered := discoverEnumConstraintNames(t)
	if len(discovered) == 0 {
		t.Fatal("discovered no enum CHECK constraints in migrations; parser or migrations changed shape")
	}

	for name := range discovered {
		if _, ok := registered[name]; !ok {
			t.Errorf("migration declares enum CHECK constraint %q with no entry in registeredEnumConstraints; "+
				"register it (constraint name, table, column, domain values) so its domain enum is guarded against drift", name)
		}
	}
}

// TestSchemaEnumDriftGateHasTeeth proves the detection logic actually fires on a
// seeded drift, so a green primary test means "no drift" rather than "the check
// is inert". It exercises the same missingDomainValues function the primary gate
// uses, with a domain value deliberately absent from the allowed set.
func TestSchemaEnumDriftGateHasTeeth(t *testing.T) {
	allowed := map[string]struct{}{"ACCEPTED": {}, "ALSO_OK": {}}

	got := missingDomainValues(allowed, []string{"ACCEPTED", "DRIFTED", "ALSO_OK"})

	if len(got) != 1 || got[0] != "DRIFTED" {
		t.Fatalf("drift detector failed to catch seeded drift: want [DRIFTED], got %v", got)
	}
}

// TestEffectiveConstraintValuesPicksHighestMigration pins the parser: valid_task_type
// is (re)defined by three migrations (018 with the obsolete single-verb tokens,
// then 024, then 033). The gate must resolve to 033's set exactly — proving both
// that later DROP/ADD redefinitions win and that token extraction is exact rather
// than accidentally matching everything or nothing.
func TestEffectiveConstraintValuesPicksHighestMigration(t *testing.T) {
	got := effectiveConstraintValues(t, "valid_task_type")

	want := map[string]struct{}{
		"ACQUIRE_DELIVER":         {},
		"COLLECT_SELL":            {},
		"LIQUIDATE":               {},
		"STORAGE_ACQUIRE_DELIVER": {},
		"DELIVER_TO_CONSTRUCTION": {},
	}

	if len(got) != len(want) {
		t.Fatalf("effective valid_task_type set = %v, want %v", keys(got), keys(want))
	}
	for v := range want {
		if _, ok := got[v]; !ok {
			t.Errorf("effective valid_task_type set is missing %q (got %v); "+
				"the parser must resolve to the highest-numbered migration (033), not 018", v, keys(got))
		}
	}
	// The obsolete 018 tokens must NOT survive — proves later definitions replace,
	// not union with, earlier ones.
	for _, obsolete := range []string{"ACQUIRE", "DELIVER", "COLLECT", "SELL"} {
		if _, ok := got[obsolete]; ok {
			t.Errorf("effective valid_task_type set contains obsolete 018 token %q; "+
				"parser is unioning migrations instead of taking the highest-numbered definition", obsolete)
		}
	}
}

// missingDomainValues returns the domain values not present in allowed. An empty
// result means every domain value is accepted by the constraint.
func missingDomainValues(allowed map[string]struct{}, domain []string) []string {
	var missing []string
	for _, v := range domain {
		if _, ok := allowed[v]; !ok {
			missing = append(missing, v)
		}
	}
	return missing
}

var (
	migrationNumRe = regexp.MustCompile(`^(\d+)_`)
	// enumTokenRe extracts the 'UPPER_SNAKE' string literals inside an IN (...) list.
	enumTokenRe = regexp.MustCompile(`'([A-Z0-9_]+)'`)
	// anyEnumConstraintRe discovers every CONSTRAINT <name> CHECK (<column> IN (...))
	// declaration. It matches both the inline CREATE TABLE form and the
	// ALTER TABLE ... ADD CONSTRAINT form (both share the "<name> CHECK (<col> IN ("
	// shape) and captures the constraint name. DOTALL so the IN list may span lines.
	anyEnumConstraintRe = regexp.MustCompile(`(?s)CONSTRAINT\s+(\w+)\s+CHECK\s*\(\s*\w+\s+IN\s*\(`)
)

// effectiveConstraintValues parses the set of allowed string literals for the
// named enum CHECK constraint, resolving to the highest-numbered migration that
// (re)defines it. Constraints are DROPped and re-ADDed across migrations, so the
// latest definition is the one Postgres ends up enforcing.
func effectiveConstraintValues(t *testing.T, constraint string) map[string]struct{} {
	t.Helper()

	// Per-constraint definition matcher: "<name> CHECK ( <col> IN ( <body> ) )".
	// Non-greedy body stops at the first ')', which closes the IN list (the enum
	// lists here contain no nested parentheses). Anchoring on the exact quoted
	// name prevents cross-matching between similarly named constraints
	// (e.g. valid_task_status vs valid_storage_status).
	defRe := regexp.MustCompile(`(?s)` + regexp.QuoteMeta(constraint) + `\s+CHECK\s*\(\s*\w+\s+IN\s*\((.*?)\)`)

	type def struct {
		num  int
		body string
	}
	var defs []def
	for _, path := range upMigrationPaths(t) {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		m := defRe.FindSubmatch(content)
		if m == nil {
			continue
		}
		defs = append(defs, def{num: migrationNumber(t, path), body: string(m[1])})
	}

	if len(defs) == 0 {
		t.Fatalf("no migration defines the %s CHECK constraint", constraint)
	}

	sort.Slice(defs, func(i, j int) bool { return defs[i].num > defs[j].num })
	winner := defs[0].body

	allowed := make(map[string]struct{})
	for _, m := range enumTokenRe.FindAllStringSubmatch(winner, -1) {
		allowed[m[1]] = struct{}{}
	}
	if len(allowed) == 0 {
		t.Fatalf("parsed no values from the effective %s constraint: %q", constraint, winner)
	}
	return allowed
}

// discoverEnumConstraintNames returns the set of every enum CHECK constraint name
// declared across the up migrations.
func discoverEnumConstraintNames(t *testing.T) map[string]struct{} {
	t.Helper()

	names := make(map[string]struct{})
	for _, path := range upMigrationPaths(t) {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, m := range anyEnumConstraintRe.FindAllSubmatch(content, -1) {
			names[string(m[1])] = struct{}{}
		}
	}
	return names
}

// upMigrationPaths returns the *.up.sql migration files. Every enum CHECK
// constraint in this schema is declared in an up migration; down migrations and
// bare *.sql maintenance scripts are deliberately excluded.
func upMigrationPaths(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(migrationsDir(t), "*.up.sql"))
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no *.up.sql migrations found")
	}
	return paths
}

// migrationNumber parses the leading numeric prefix of a migration filename.
func migrationNumber(t *testing.T, path string) int {
	t.Helper()
	base := filepath.Base(path)
	m := migrationNumRe.FindStringSubmatch(base)
	if m == nil {
		t.Fatalf("migration %s has no numeric prefix", base)
	}
	num, _ := strconv.Atoi(m[1])
	return num
}

// migrationsDir resolves <gobot>/migrations relative to this test file so the
// gate is independent of the working directory go test runs in.
func migrationsDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// thisFile = <gobot>/internal/adapters/persistence/schema_enum_drift_test.go
	dir := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "migrations")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("migrations dir %s not found: %v", dir, err)
	}
	return dir
}

// keys returns the keys of a set, for readable failure messages.
func keys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
