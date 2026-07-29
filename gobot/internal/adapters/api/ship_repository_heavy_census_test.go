package api

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// captureLogger records every line so the census's loud warning can be asserted on.
type captureLogger struct {
	mu    sync.Mutex
	lines []capturedLine
}

type capturedLine struct {
	level    string
	message  string
	metadata map[string]interface{}
}

func (c *captureLogger) Log(level, message string, metadata map[string]interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, capturedLine{level: level, message: message, metadata: metadata})
}

func (c *captureLogger) warnings() []capturedLine {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []capturedLine
	for _, l := range c.lines {
		if strings.HasPrefix(strings.ToUpper(l.level), "WARN") {
			out = append(out, l)
		}
	}
	return out
}

// censusRepo builds a DB-only ShipRepository with a capturing logger on the context.
func censusRepo(t *testing.T) (*ShipRepository, *persistence.PlayerModel, *captureLogger, context.Context) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	p := &persistence.PlayerModel{AgentSymbol: "ORION", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(p).Error)
	log := &captureLogger{}
	return NewShipRepository(nil, nil, nil, nil, db, nil), p, log, logging.WithLogger(context.Background(), log)
}

func addHull(t *testing.T, r *ShipRepository, playerID int, symbol, frame string, cargo int, fleet string) {
	t.Helper()
	require.NoError(t, r.db.Create(&persistence.ShipModel{
		ShipSymbol:     symbol,
		PlayerID:       playerID,
		FrameSymbol:    frame,
		CargoCapacity:  cargo,
		DedicatedFleet: fleet,
	}).Error)
}

// THE MONEY-GUARD PIN. The census must be independent of dedicated_fleet.
//
// The heavy class's DEMAND signal uses countShips(..., DedicatedFleet() == "trade") — a TRADE-POOL
// count. Reusing that predicate here would make a heavy tagged anything else invisible, leaving the
// reservation open and authorising a re-buy of a hull we already own (spec C2). This census asks a
// different question: how much capital is tied up in large hulls, whatever they are doing — and
// since sp-r7eiu removed class_ceiling it is the ONLY count that can refuse a heavy purchase.
func TestCountHeavyHulls_IsIndependentOfFleetTag(t *testing.T) {
	repo, p, _, ctx := censusRepo(t)

	addHull(t, repo, p.ID, "ORION-1", "FRAME_HEAVY_FREIGHTER", 225, "trade")
	addHull(t, repo, p.ID, "ORION-2", "FRAME_HEAVY_FREIGHTER", 225, "contract")
	addHull(t, repo, p.ID, "ORION-3", "FRAME_HEAVY_FREIGHTER", 225, "sensing_parked")
	addHull(t, repo, p.ID, "ORION-4", "FRAME_HEAVY_FREIGHTER", 225, "explorer")
	addHull(t, repo, p.ID, "ORION-5", "FRAME_HEAVY_FREIGHTER", 225, "") // untagged

	got, err := repo.CountHeavyHulls(ctx, shared.MustNewPlayerID(p.ID))
	require.NoError(t, err)
	require.Equal(t, 5, got, "every owned heavy counts regardless of fleet tag — a tag-scoped census would authorise a re-buy")
}

// Both known-heavy frames count, and ordinary hulls do not.
func TestCountHeavyHulls_CountsKnownHeavyFrames(t *testing.T) {
	repo, p, log, ctx := censusRepo(t)

	addHull(t, repo, p.ID, "ORION-1", "FRAME_HEAVY_FREIGHTER", 225, "trade")
	addHull(t, repo, p.ID, "ORION-2", "FRAME_BULK_FREIGHTER", 500, "trade")
	addHull(t, repo, p.ID, "ORION-3", "FRAME_LIGHT_FREIGHTER", 80, "trade")
	addHull(t, repo, p.ID, "ORION-4", "FRAME_FRIGATE", 40, "")
	addHull(t, repo, p.ID, "ORION-5", "FRAME_PROBE", 0, "sensing_parked")

	got, err := repo.CountHeavyHulls(ctx, shared.MustNewPlayerID(p.ID))
	require.NoError(t, err)
	require.Equal(t, 2, got)
	require.Empty(t, log.warnings(), "a frame-matched heavy infers nothing, so it must not warn")
}

// THE SAFETY NET + ITS LOUD LOG. A hull big enough to be a heavy but carrying a frame we do not
// recognise counts (over-counting is the safe direction) AND warns — that warning is the only
// signal we can get that the inferred frame list is incomplete, since the fleet owns no heavy to
// check it against.
func TestCountHeavyHulls_CapacityNetCountsAndWarnsOnUnknownFrame(t *testing.T) {
	repo, p, log, ctx := censusRepo(t)

	addHull(t, repo, p.ID, "ORION-9Z", "FRAME_MYSTERY_FREIGHTER", 300, "trade")

	got, err := repo.CountHeavyHulls(ctx, shared.MustNewPlayerID(p.ID))
	require.NoError(t, err)
	require.Equal(t, 1, got, "an unrecognised large hull counts — over-counting buys fewer, the safe direction")

	warns := log.warnings()
	require.Len(t, warns, 1, "the capacity net firing MUST log loudly — it is the only signal the frame list is wrong")
	require.Contains(t, warns[0].message, "FRAME_MYSTERY_FREIGHTER", "the warning must name the unrecognised frame so the list can be corrected")
	require.Contains(t, warns[0].message, "ORION-9Z", "the warning must name the hull")
}

// Below the threshold with an unrecognised frame is an ordinary hull: no count, and no noise —
// otherwise every probe and hauler would warn and the signal would be worthless.
func TestCountHeavyHulls_BelowThresholdUnknownFrameIsSilent(t *testing.T) {
	repo, p, log, ctx := censusRepo(t)

	addHull(t, repo, p.ID, "ORION-1", "FRAME_MYSTERY_SMALL", 100, "trade")
	addHull(t, repo, p.ID, "ORION-2", "FRAME_LIGHT_FREIGHTER", 80, "trade")

	got, err := repo.CountHeavyHulls(ctx, shared.MustNewPlayerID(p.ID))
	require.NoError(t, err)
	require.Equal(t, 0, got)
	require.Empty(t, log.warnings(), "ordinary hulls must not warn or the signal is worthless")
}

// A fresh fleet owns no heavy ⇒ 0, so the reserve is taken against a full cap.
func TestCountHeavyHulls_NoHeaviesIsZero(t *testing.T) {
	repo, p, _, ctx := censusRepo(t)

	addHull(t, repo, p.ID, "ORION-1", "FRAME_LIGHT_FREIGHTER", 80, "trade")
	addHull(t, repo, p.ID, "ORION-2", "FRAME_PROBE", 0, "sensing_parked")

	got, err := repo.CountHeavyHulls(ctx, shared.MustNewPlayerID(p.ID))
	require.NoError(t, err)
	require.Equal(t, 0, got)
}

// Regression for the observed "19 heavies" (9 live + 10 dead-era): the same agent re-registers
// under a NEW player_id each era and ship symbols are reused with stale frame symbols. Counting
// across eras would OVER-count, wrongly closing the capability and stalling heavy buying forever.
func TestCountHeavyHulls_ExcludesDeadEraGhostHulls(t *testing.T) {
	repo, live, _, ctx := censusRepo(t)

	dead := &persistence.PlayerModel{AgentSymbol: "ORION", Token: "tok-dead", CreatedAt: time.Now()}
	require.NoError(t, repo.db.Create(dead).Error)

	addHull(t, repo, dead.ID, "ORION-2B", "FRAME_HEAVY_FREIGHTER", 225, "trade")
	addHull(t, repo, dead.ID, "ORION-3C", "FRAME_HEAVY_FREIGHTER", 225, "trade")
	addHull(t, repo, live.ID, "ORION-2B", "FRAME_PROBE", 0, "") // symbol reused this era
	addHull(t, repo, live.ID, "ORION-9Z", "FRAME_HEAVY_FREIGHTER", 225, "trade")

	got, err := repo.CountHeavyHulls(ctx, shared.MustNewPlayerID(live.ID))
	require.NoError(t, err)
	require.Equal(t, 1, got, "dead-era ghost hulls must not inflate the live heavy census")
}

// A missing DB is an ERROR, never a silent zero: zero reads as "no heavies owned" and would
// authorise a buy against an unreadable fleet (RULINGS #4 fail-closed).
func TestCountHeavyHulls_NoDBIsAnError(t *testing.T) {
	repo := NewShipRepository(nil, nil, nil, nil, nil, nil)

	_, err := repo.CountHeavyHulls(context.Background(), shared.MustNewPlayerID(1))
	require.Error(t, err, "an unreadable fleet must surface, not read as zero heavies owned")
}
