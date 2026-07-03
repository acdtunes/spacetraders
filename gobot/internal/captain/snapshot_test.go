package captainsup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

func TestComposeSnapshotContainsAllSections(t *testing.T) {
	db, playerID, _ := setupDB(t)
	now := time.Now()

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "SHIP-1", PlayerID: playerID, NavStatus: "DOCKED",
		LocationSymbol: "X1-A1", FuelCurrent: 300, FuelCapacity: 400,
		CargoUnits: 10, CargoCapacity: 40,
	}).Error)
	started := now.Add(-30 * time.Minute)
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: "c-1", PlayerID: playerID, Status: "RUNNING", CommandType: "arbitrage",
		StartedAt: &started, HeartbeatAt: &now,
	}).Error)
	require.NoError(t, db.Create(&persistence.TransactionModel{
		ID: "t-1", PlayerID: playerID, Timestamp: now.Add(-2 * time.Hour), TransactionType: "SELL",
		Category: "TRADING_REVENUE", Amount: 4000, BalanceBefore: 96000, BalanceAfter: 100000,
	}).Error)

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "state"), 0o755))
	ws := NewWorkspace(dir)
	require.NoError(t, os.WriteFile(ws.StatePath("strategy.md"), []byte("PRIORITIZE MANUFACTURING"), 0o644))
	require.NoError(t, os.WriteFile(ws.StatePath("lessons.md"), []byte("LESSON: never overfuel"), 0o644))
	require.NoError(t, os.WriteFile(ws.StatePath("decisions.jsonl"),
		[]byte(`{"id":"d-1","action":"test arb","expectation":"+40k in 3h","review_after":"2020-01-01T00:00:00Z"}`+"\n"), 0o644))

	events := []*captain.Event{{ID: 5, Type: captain.EventWorkflowFailed, Ship: "SHIP-1",
		PlayerID: playerID, Payload: `{"error":"no fuel"}`, CreatedAt: now}}

	prompt, err := ComposeSnapshot(context.Background(), db, ws, playerID, events, now)
	require.NoError(t, err)

	for _, want := range []string{
		"## Pending events", "workflow.failed", "no fuel",
		"## Fleet", "SHIP-1", "DOCKED", "X1-A1",
		"## Active containers", "c-1", "arbitrage",
		"## Treasury", "100000",
		"## Decisions due for review", "d-1", "+40k in 3h",
		"## Standing strategy", "PRIORITIZE MANUFACTURING",
		"## Lessons", "never overfuel",
		"## Your obligations this session",
	} {
		require.Contains(t, prompt, want, "missing section content: %s", want)
	}
}

func TestComposeSnapshotIncludesAdmiralInbox(t *testing.T) {
	db, playerID, _ := setupDB(t)
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "state"), 0o755))
	ws := NewWorkspace(dir)
	require.NoError(t, os.WriteFile(ws.InboxPath(), []byte("I disagree about haulers."), 0o644))

	prompt, err := ComposeSnapshot(context.Background(), db, ws, playerID, nil, time.Now())
	require.NoError(t, err)
	require.Contains(t, prompt, "## Message from the Admiral")
	require.Contains(t, prompt, "I disagree about haulers.")
	require.Contains(t, prompt, "address it with evidence")

	require.NoError(t, os.Remove(ws.InboxPath()))
	prompt, err = ComposeSnapshot(context.Background(), db, ws, playerID, nil, time.Now())
	require.NoError(t, err)
	require.NotContains(t, prompt, "Message from the Admiral")
}
