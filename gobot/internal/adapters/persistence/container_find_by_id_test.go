package persistence_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// prodScaleRows is the size the containers table actually reached in production: 34,279 FAILED
// rows from the sp-20eyn crash loop alone, on top of 30,016 STOPPED and 18,876 COMPLETED. The
// bead is explicit that a fix invisible at 100 rows is not verified for 34,000, so the table is
// seeded to that order of magnitude rather than to a token handful.
const prodScaleRows = 34_000

// capturedQuery is one SELECT the repository issued: its SQL and how many rows it pulled back.
type capturedQuery struct {
	sql  string
	rows int64
}

// sqlCapture records every query GORM executes, so a test can assert the SHAPE of the read and
// not merely its result. Result-only assertions cannot tell an indexed lookup from a full scan
// that happens to find the right row — which is exactly the defect here.
type sqlCapture struct {
	mu      sync.Mutex
	queries []capturedQuery
}

func (c *sqlCapture) record(sql string, rows int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queries = append(c.queries, capturedQuery{sql: sql, rows: rows})
}

func (c *sqlCapture) last() capturedQuery {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.queries) == 0 {
		return capturedQuery{}
	}
	return c.queries[len(c.queries)-1]
}

func (c *sqlCapture) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queries = nil
}

func attachSQLCapture(t *testing.T, db *gorm.DB) *sqlCapture {
	t.Helper()
	capture := &sqlCapture{}
	err := db.Callback().Query().After("gorm:query").Register("sp72gmi:capture", func(tx *gorm.DB) {
		capture.record(tx.Statement.SQL.String(), tx.Statement.RowsAffected)
	})
	require.NoError(t, err, "register query capture")
	return capture
}

// seedContainersAtProdScale builds a containers table of realistic size and returns the id of one
// row buried in the middle of it — the middle rather than the end so a scan cannot accidentally
// look fast by finding it immediately.
func seedContainersAtProdScale(t *testing.T, db *gorm.DB, n int) (targetID string, playerID int) {
	t.Helper()

	player := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)

	started := time.Now().Add(-24 * time.Hour)
	rows := make([]persistence.ContainerModel, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, persistence.ContainerModel{
			ID:            containerIDAt(i),
			PlayerID:      player.ID,
			ContainerType: "WORKER",
			CommandType:   "contract_work",
			Status:        "FAILED",
			RestartPolicy: "ON_FAILURE",
			StartedAt:     &started,
		})
	}
	require.NoError(t, db.CreateInBatches(rows, 1000).Error)

	return containerIDAt(n / 2), player.ID
}

func containerIDAt(i int) string {
	return "contract-work-TORWIND-5-" + strings.Repeat("0", 6) + itoa6(i)
}

func itoa6(i int) string {
	digits := "0123456789"
	out := []byte("000000")
	for pos := 5; pos >= 0 && i > 0; pos-- {
		out[pos] = digits[i%10]
		i /= 10
	}
	return string(out)
}

// THE FIX, asserted as a query shape (sp-72gmi acceptance 2 and 4).
//
// findContainerModelByID used to call ListAll(ctx, nil) and scan the result in Go, so every worker
// start pulled the ENTIRE containers table into memory to find one row. The rows-read assertion is
// the one that matters and it is size-independent in meaning: one row versus all of them. A
// timing assertion would be flaky; a result-only assertion would pass just as happily against the
// full scan, since the scan does find the right container.
func TestFindByIDAcrossPlayers_ReadsOneRowNotTheWholeTable(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	targetID, _ := seedContainersAtProdScale(t, db, prodScaleRows)
	capture := attachSQLCapture(t, db)
	repo := persistence.NewContainerRepository(db)

	// BEFORE: the old shape, run against the same table so the comparison is like-for-like.
	beforeStart := time.Now()
	all, err := repo.ListAll(context.Background(), nil)
	require.NoError(t, err)
	beforeElapsed := time.Since(beforeStart)
	scanned := capture.last()
	require.Equal(t, int64(prodScaleRows), scanned.rows,
		"premise broken — the old ListAll shape must read every row, or this test is not measuring what it claims")
	require.Len(t, all, prodScaleRows)

	// AFTER: one indexed read.
	capture.reset()
	afterStart := time.Now()
	model, err := repo.FindByIDAcrossPlayers(context.Background(), targetID)
	require.NoError(t, err)
	afterElapsed := time.Since(afterStart)

	require.NotNil(t, model, "the container must still be found")
	require.Equal(t, targetID, model.ID)

	indexed := capture.last()
	require.Equal(t, int64(1), indexed.rows,
		"the lookup read %d rows out of a %d-row table; it must read exactly one. Reading them all is "+
			"the defect: the sp-20eyn crash loop inflated this table and every worker start then paid for it",
		indexed.rows, prodScaleRows)

	// THE SHAPE. A full scan's SQL carries no WHERE and no LIMIT; a regression to ListAll-then-scan
	// would satisfy every result assertion above and fail here.
	require.Contains(t, indexed.sql, "WHERE",
		"the lookup must filter in SQL, not in Go: %s", indexed.sql)
	require.Contains(t, indexed.sql, "id",
		"the filter must be on id: %s", indexed.sql)
	require.Contains(t, strings.ToUpper(indexed.sql), "LIMIT",
		"a single-row lookup must bound itself: %s", indexed.sql)

	// Not an assertion — evidence for the retention half of sp-72gmi. Once this lookup is indexed,
	// the only reads left that grow with the table are the two boot-time recovery scans
	// (daemon_server_recovery.go: ListByStatus(INTERRUPTED|RUNNING, nil)). status carries no index,
	// so they are sequential scans, but they run twice per BOOT rather than per worker start. That
	// is what decides whether table size is still a latency problem or merely disk.
	capture.reset()
	recoveryStart := time.Now()
	running, err := repo.ListByStatus(context.Background(), container.ContainerStatusRunning, nil)
	require.NoError(t, err)
	recoveryElapsed := time.Since(recoveryStart)

	t.Logf("containers=%d  before(ListAll+Go scan)=%s rows=%d   after(indexed)=%s rows=%d   boot-recovery(ListByStatus RUNNING)=%s matched=%d",
		prodScaleRows, beforeElapsed, scanned.rows, afterElapsed, indexed.rows, recoveryElapsed, len(running))
}

// Acceptance 3, regression: the lookup still answers for a container owned by a DIFFERENT player.
// That is the whole reason this read exists — the worker start and recovery paths hold a container
// id and no player id, so a player-scoped Get cannot serve them.
func TestFindByIDAcrossPlayers_FindsAContainerOwnedByAnotherPlayer(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	owner := persistence.PlayerModel{AgentSymbol: "OWNER", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&owner).Error)
	other := persistence.PlayerModel{AgentSymbol: "OTHER", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&other).Error)

	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "worker-owned-by-owner", PlayerID: owner.ID,
		ContainerType: "WORKER", CommandType: "contract_work", Status: "RUNNING",
	}).Error)

	repo := persistence.NewContainerRepository(db)
	model, err := repo.FindByIDAcrossPlayers(context.Background(), "worker-owned-by-owner")
	require.NoError(t, err)
	require.NotNil(t, model, "a container must be findable without knowing which player owns it")
	require.Equal(t, owner.ID, model.PlayerID)
}

// Acceptance 3, regression: absent is (nil, nil), not an error — the same convention Get follows,
// and what the caller turns into its "container %s not found" message.
func TestFindByIDAcrossPlayers_AbsentIsNilNotAnError(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	repo := persistence.NewContainerRepository(db)
	model, err := repo.FindByIDAcrossPlayers(context.Background(), "no-such-container")
	require.NoError(t, err, "an absent container is not an error, matching Get")
	require.Nil(t, model)
}
