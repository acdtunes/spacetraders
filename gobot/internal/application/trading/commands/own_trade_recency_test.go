package commands

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	testOwnTradeMaxPct = ownTradePenaltyPctDefault
	testOwnTradeCold   = ownTradeColdMinutesDefault
)

func TestOwnTradeFreshnessMultiplierIsBounded(t *testing.T) {
	// The haircut can never exceed its configured share, never turn negative, and never
	// exceed 1 — a multiplier above 1 would REWARD ground the fleet just drained.
	floor := 1 - float64(testOwnTradeMaxPct)/100
	for _, age := range []float64{
		ownTradeAgeFloorMinutes, 0.6, 1, 5, 15, 29.9, 30, 60, 120, 239.9, 240, 1000, 1e9,
	} {
		m := ownTradeFreshnessMultiplier(age, testOwnTradeMaxPct, testOwnTradeCold)
		require.GreaterOrEqualf(t, m, floor, "age %v dropped below the configured bound", age)
		require.LessOrEqualf(t, m, 1.0, "age %v was rewarded, not penalised", age)
	}
	// A pathological configured percent still cannot invert the ranking, because the resolver
	// clamps before the multiplier ever sees it.
	require.GreaterOrEqual(t,
		ownTradeFreshnessMultiplier(ownTradeAgeFloorMinutes, resolveOwnTradePenaltyPct(9000), testOwnTradeCold),
		0.0)
}

func TestOwnTradeFreshnessMultiplierDecaysWithElapsedTime(t *testing.T) {
	// Strictly monotone in age over the whole penalised range: more rest is always ranked
	// better, so the penalty can never create a local preference for fresher ground.
	ages := []float64{ownTradeAgeFloorMinutes, 1, 2, 5, 10, 15, 30, 45, 60, 90, 120, 180, 239}
	prev := ownTradeFreshnessMultiplier(ages[0], testOwnTradeMaxPct, testOwnTradeCold)
	require.Less(t, prev, 1.0, "the freshest ground must be penalised at all")
	for _, age := range ages[1:] {
		m := ownTradeFreshnessMultiplier(age, testOwnTradeMaxPct, testOwnTradeCold)
		require.Greaterf(t, m, prev, "age %v was not ranked above the younger gap before it", age)
		prev = m
	}
	require.Equal(t, 1.0, ownTradeFreshnessMultiplier(testOwnTradeCold, testOwnTradeMaxPct, testOwnTradeCold),
		"the penalty must reach exactly zero at the cold horizon, not merely approach it")
}

func TestOwnTradeFreshnessMultiplierLeavesColdAndUnknownGroundAlone(t *testing.T) {
	// Never-visited ground, ground older than the horizon, and ground with no readable stamp
	// are all unpenalised — the fail-open direction the ranker requires.
	for name, age := range map[string]float64{
		"unknown-sentinel": 0,
		"negative":         -5,
		"at-horizon":       testOwnTradeCold,
		"past-horizon":     testOwnTradeCold + 1,
		"very-old":         30 * 24 * 60,
	} {
		require.Equalf(t, 1.0, ownTradeFreshnessMultiplier(age, testOwnTradeMaxPct, testOwnTradeCold),
			"%s ground must not be de-ranked", name)
	}
	// A disarmed penalty and a nonsensical horizon are both inert rather than erratic.
	require.Equal(t, 1.0, ownTradeFreshnessMultiplier(1, 0, testOwnTradeCold))
	require.Equal(t, 1.0, ownTradeFreshnessMultiplier(1, testOwnTradeMaxPct, 0))
}

func TestOwnTradeFreshnessMultiplierCannotOverturnARicherGround(t *testing.T) {
	// The bound exists so a real spread difference always wins. Worst case — the freshest
	// possible ground against fully-rested ground — the ratio a rival must beat is small
	// enough that a merely-better board still outranks a just-drained one.
	worst := ownTradeFreshnessMultiplier(ownTradeAgeFloorMinutes, testOwnTradeMaxPct, testOwnTradeCold)
	require.Less(t, 1/worst, 2.0,
		"a candidate worth twice as much on the board must never be de-ranked below a drained one")

	cmd := &RunTourCoordinatorCommand{}
	drainedButRich := repositionCandidate{system: "X1-RICH", score: 20000, hops: 1, ownTradeAgeMinutes: 0.5}
	restedButThin := repositionCandidate{system: "X1-THIN", score: 9000, hops: 1}
	require.Greater(t, repositionRankKey(drainedButRich, cmd), repositionRankKey(restedButThin, cmd))
}

func TestRepositionRankKeyDeRanksRecentlyWorkedGround(t *testing.T) {
	cmd := &RunTourCoordinatorCommand{}
	fresh := repositionCandidate{system: "X1-HOT", score: 10000, hops: 1, ownTradeAgeMinutes: 1}
	rested := repositionCandidate{system: "X1-COLD", score: 10000, hops: 1}
	require.Less(t, repositionRankKey(fresh, cmd), repositionRankKey(rested, cmd),
		"equal boards must be separated by how recently the fleet worked them")

	// Composed with the reach path's per-hop deadhead decay rather than replacing it.
	reach := &RunTourCoordinatorCommand{RepositionReachEnabled: true}
	twoHopRested := repositionCandidate{system: "X1-COLD", score: 10000, hops: 2}
	require.Less(t, repositionRankKey(twoHopRested, reach), repositionRankKey(rested, reach))
}

func TestRepositionRankKeyIsUnchangedWithoutRecencyData(t *testing.T) {
	// An unwired reader stamps nothing, so every candidate keeps the key it had before this
	// existed — on BOTH the legacy and the reach ranking paths.
	unstamped := repositionCandidate{system: "X1-AA11", score: 7331, hops: 3}
	legacy := &RunTourCoordinatorCommand{}
	require.Equal(t, float64(unstamped.score), repositionRankKey(unstamped, legacy))

	reach := &RunTourCoordinatorCommand{RepositionReachEnabled: true}
	require.Equal(t,
		repositionDecayedScore(unstamped, resolveRepositionReachHopDecay(0)),
		repositionRankKey(unstamped, reach))
}

func TestRepositionRankKeyKillSwitchRestoresTheBlindPreRank(t *testing.T) {
	drained := repositionCandidate{system: "X1-HOT", score: 10000, hops: 1, ownTradeAgeMinutes: 1}
	require.Equal(t, float64(drained.score),
		repositionRankKey(drained, &RunTourCoordinatorCommand{OwnTradePenaltyDisabled: true}))
	require.Less(t, repositionRankKey(drained, &RunTourCoordinatorCommand{}), float64(drained.score),
		"the de-ranking must be live with no config present")
}

func TestOwnTradeResolversClampTheirKnobs(t *testing.T) {
	require.Equal(t, ownTradePenaltyPctDefault, resolveOwnTradePenaltyPct(0))
	require.Equal(t, ownTradePenaltyPctDefault, resolveOwnTradePenaltyPct(-1))
	require.Equal(t, 20, resolveOwnTradePenaltyPct(20))
	require.Equal(t, 100, resolveOwnTradePenaltyPct(1000))

	require.Equal(t, ownTradeColdMinutesDefault, resolveOwnTradeColdMinutes(0))
	require.Equal(t, 90, resolveOwnTradeColdMinutes(90))
	require.Equal(t, ownTradeColdMinutesMax, resolveOwnTradeColdMinutes(99999),
		"the horizon doubles as the ledger scan's lookback and must stay bounded")

	// The daemon sizes the scan from the same knob, so the window can never fall short of
	// the horizon the coordinator penalises over.
	require.Equal(t, time.Duration(ownTradeColdMinutesDefault)*time.Minute, OwnTradeRecencyLookback(0))
	require.Equal(t, 90*time.Minute, OwnTradeRecencyLookback(90))
}

func TestOwnTradeAgeMinutesSentinels(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	require.Equal(t, 0.0, ownTradeAgeMinutes(time.Time{}, now), "an unstamped system is unknown, not fresh")
	require.Equal(t, ownTradeAgeFloorMinutes, ownTradeAgeMinutes(now.Add(time.Hour), now),
		"a future stamp is a clock fault, and must not read as unknown")
	require.Equal(t, ownTradeAgeFloorMinutes, ownTradeAgeMinutes(now, now))
	require.InDelta(t, 42.0, ownTradeAgeMinutes(now.Add(-42*time.Minute), now), 1e-9)
}

func TestOwnTradeAgeNoteRendersOnlyWhenKnown(t *testing.T) {
	require.Equal(t, "", ownTradeAgeNote(0))
	require.Equal(t, ",own-trade=12m", ownTradeAgeNote(12.4))
}

// --- the ledger-backed reader ---

type fakeOwnTradeRepo struct {
	rows map[string]time.Time
	err  error
	hits int
}

func (f *fakeOwnTradeRepo) LastTradeByWaypoint(
	_ context.Context, _ shared.PlayerID, _ time.Time,
) (map[string]time.Time, error) {
	f.hits++
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

var _ ledger.OwnTradeRecencyReader = (*fakeOwnTradeRepo)(nil)

func TestLedgerOwnTradeRecencyReaderRollsWaypointsUpToTheirSystem(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	clock := &shared.MockClock{CurrentTime: now}
	repo := &fakeOwnTradeRepo{rows: map[string]time.Time{
		"X1-DH68-AB8Z": now.Add(-40 * time.Minute),
		"X1-DH68-CD1Q": now.Add(-3 * time.Minute), // the system's clock is its most recent market
		"X1-US72-F23D": now.Add(-90 * time.Minute),
	}}
	r := NewLedgerOwnTradeRecencyReader(repo, clock, time.Hour)

	table := r.LastTradeBySystem(context.Background(), 9)
	require.Equal(t, now.Add(-3*time.Minute), table["X1-DH68"])
	require.Equal(t, now.Add(-90*time.Minute), table["X1-US72"])
	require.Len(t, table, 2)

	// Cached inside the TTL, re-read past it.
	r.LastTradeBySystem(context.Background(), 9)
	require.Equal(t, 1, repo.hits)
	clock.Advance(ownTradeCacheTTL + time.Second)
	r.LastTradeBySystem(context.Background(), 9)
	require.Equal(t, 2, repo.hits)
}

func TestLedgerOwnTradeRecencyReaderFailsOpen(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)

	// A nil reader, a nil repo and an unparseable player all yield "no table", which the
	// multiplier reads as unknown ground and leaves unpenalised.
	var nilReader *LedgerOwnTradeRecencyReader
	require.Nil(t, nilReader.LastTradeBySystem(ctx, 9))
	require.Nil(t, NewLedgerOwnTradeRecencyReader(nil, &shared.MockClock{CurrentTime: now}, time.Hour).LastTradeBySystem(ctx, 9))
	require.Nil(t, NewLedgerOwnTradeRecencyReader(&fakeOwnTradeRepo{}, &shared.MockClock{CurrentTime: now}, time.Hour).
		LastTradeBySystem(ctx, 0))

	// A failing read with nothing cached penalises nobody.
	failing := &fakeOwnTradeRepo{err: errors.New("ledger unavailable")}
	require.Nil(t, NewLedgerOwnTradeRecencyReader(failing, &shared.MockClock{CurrentTime: now}, time.Hour).
		LastTradeBySystem(ctx, 9))

	// A failing read AFTER a good one serves the stale table: it can only understate
	// freshness, so it may misjudge worked ground as rested but never invent crowding.
	flaky := &fakeOwnTradeRepo{rows: map[string]time.Time{"X1-DH68-AB8Z": now.Add(-time.Minute)}}
	clock := &shared.MockClock{CurrentTime: now}
	r := NewLedgerOwnTradeRecencyReader(flaky, clock, time.Hour)
	require.Len(t, r.LastTradeBySystem(ctx, 9), 1)
	flaky.err = errors.New("ledger went away")
	clock.Advance(ownTradeCacheTTL + time.Second)
	require.Len(t, r.LastTradeBySystem(ctx, 9), 1)
}

func TestOwnTradeRecencyTableIsNilSafeOnTheHandler(t *testing.T) {
	// The stamping path must survive an unwired handler: a nil table's every lookup is the
	// zero time, which is the unknown sentinel, which is no penalty.
	var h *RunTourCoordinatorHandler
	require.Nil(t, h.ownTradeRecencyTable(context.Background(), 9))
	unwired := &RunTourCoordinatorHandler{}
	table := unwired.ownTradeRecencyTable(context.Background(), 9)
	require.Nil(t, table)
	require.Equal(t, 0.0, ownTradeAgeMinutes(table["X1-DH68"], time.Now()))
}
