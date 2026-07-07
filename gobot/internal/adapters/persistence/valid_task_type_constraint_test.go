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
)

// domainTaskTypes is every manufacturing.TaskType the code can persist to
// manufacturing_tasks.task_type. Whenever a new TaskType constant is added in
// internal/domain/manufacturing/task.go, it MUST be added here AND to the
// valid_task_type CHECK constraint via a new migration — otherwise saving a task
// of that type fails against Postgres with SQLSTATE 23514 (the sp-8ind defect).
var domainTaskTypes = []manufacturing.TaskType{
	manufacturing.TaskTypeAcquireDeliver,
	manufacturing.TaskTypeCollectSell,
	manufacturing.TaskTypeLiquidate,
	manufacturing.TaskTypeStorageAcquireDeliver,
	manufacturing.TaskTypeDeliverToConstruction,
}

// TestValidTaskTypeConstraintCoversAllDomainTaskTypes is a drift guard for the
// sp-8ind defect class. It parses the effective valid_task_type CHECK constraint
// from the SQL migrations (the highest-numbered *.up.sql that (re)defines it) and
// asserts every domain TaskType is accepted.
//
// The SQLite-backed test DB is built by GORM AutoMigrate, which does NOT create
// this CHECK constraint — only the migrations encode it. That gap is exactly why
// the original defect reached production untested. This test is the unit-level
// proof that the migration-defined constraint stays in sync with the domain; the
// constraint itself is enforced by Postgres and exercised at deploy.
func TestValidTaskTypeConstraintCoversAllDomainTaskTypes(t *testing.T) {
	allowed := parseEffectiveValidTaskTypes(t)

	for _, tt := range domainTaskTypes {
		if _, ok := allowed[string(tt)]; !ok {
			t.Errorf("domain TaskType %q is not accepted by the valid_task_type CHECK constraint; "+
				"add a migration extending manufacturing_tasks valid_task_type to include it", tt)
		}
	}
}

var (
	migrationNumRe = regexp.MustCompile(`^(\d+)_`)
	// inClauseRe captures the task_type IN (...) list that follows the first
	// mention of valid_task_type in a migration (the DROP/ADD pair share the
	// name; the ADD's IN(...) is the next one reached).
	inClauseRe = regexp.MustCompile(`(?s)valid_task_type.*?task_type\s+IN\s*\((.*?)\)`)
	tokenRe    = regexp.MustCompile(`'([A-Z_]+)'`)
)

// parseEffectiveValidTaskTypes returns the set of task_type string literals from
// the most recent migration that (re)defines the valid_task_type constraint.
// Later migrations DROP + re-ADD the constraint, so the highest-numbered
// definition is the one Postgres ends up enforcing.
func parseEffectiveValidTaskTypes(t *testing.T) map[string]struct{} {
	t.Helper()

	dir := migrationsDir(t)
	paths, err := filepath.Glob(filepath.Join(dir, "*.up.sql"))
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}

	type def struct {
		num  int
		body string
	}
	var defs []def
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		m := inClauseRe.FindSubmatch(content)
		if m == nil {
			continue
		}
		base := filepath.Base(path)
		nm := migrationNumRe.FindStringSubmatch(base)
		if nm == nil {
			t.Fatalf("migration %s has no numeric prefix", base)
		}
		num, _ := strconv.Atoi(nm[1])
		defs = append(defs, def{num: num, body: string(m[1])})
	}

	if len(defs) == 0 {
		t.Fatal("no migration defines the valid_task_type CHECK constraint")
	}

	sort.Slice(defs, func(i, j int) bool { return defs[i].num > defs[j].num })
	winner := defs[0].body

	allowed := make(map[string]struct{})
	for _, m := range tokenRe.FindAllStringSubmatch(winner, -1) {
		allowed[m[1]] = struct{}{}
	}
	if len(allowed) == 0 {
		t.Fatalf("parsed no task types from effective valid_task_type constraint: %q", winner)
	}
	return allowed
}

// migrationsDir resolves <gobot>/migrations relative to this test file so the
// test is independent of the working directory go test runs in.
func migrationsDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// thisFile = <gobot>/internal/adapters/persistence/valid_task_type_constraint_test.go
	dir := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "migrations")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("migrations dir %s not found: %v", dir, err)
	}
	return dir
}
