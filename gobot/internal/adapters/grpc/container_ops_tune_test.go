package grpc

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	expansionCmd "github.com/andrescamacho/spacetraders-go/internal/application/expansion/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/probebuy"
	scoutingCmd "github.com/andrescamacho/spacetraders-go/internal/application/scouting/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
	"gorm.io/gorm"
)

// These tests cover the generic runtime tune mechanism (sp-vwek) and its first two
// migrated engines, the market-freshness sizer and the frontier expansion
// coordinator (sp-0z7f). The mechanism generalizes the sp-ev0n worker-cap pattern:
// a `tune` verb read-modify-writes ONE knob in a RUNNING container's persisted
// config column (the daemon as single writer, RULINGS #3), a static bounds registry
// rejects out-of-bounds/unknown tunes BEFORE any write, and the running coordinator
// re-reads its config at each tick start (liveconfig.Reader) so the change lands on
// the NEXT tick — no restart, no rebuild, no config.yaml edit.
//
// Test budget: 12 distinct behaviors × 2 = 24 max unit tests. This file holds 10;
// the liveconfig package holds 2; the CLI surface holds 3. Total 15/24.

// ---- fixtures ---------------------------------------------------------------

func tuneTestDB(t *testing.T) (*gorm.DB, *persistence.ContainerRepositoryGORM, int) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "TUNE-AGENT", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	return db, persistence.NewContainerRepository(db), player.ID
}

func seedTuneContainer(t *testing.T, db *gorm.DB, playerID int, id, containerType, commandType, status string, config map[string]interface{}) {
	t.Helper()
	raw, err := json.Marshal(config)
	require.NoError(t, err)
	now := time.Now()
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: id, PlayerID: playerID,
		ContainerType: containerType, CommandType: commandType,
		Status: status, Config: string(raw),
		StartedAt: &now, HeartbeatAt: &now,
	}).Error)
}

func containerConfigJSON(t *testing.T, repo *persistence.ContainerRepositoryGORM, id string, playerID int) string {
	t.Helper()
	model, err := repo.Get(context.Background(), id, playerID)
	require.NoError(t, err)
	require.NotNil(t, model)
	return model.Config
}

// ---- coordinator port fakes (driven-port stubs for the two engines) ---------

type tuneFakeFreshness struct {
	snapshots []domainScouting.SystemFreshnessSnapshot
}

func (f *tuneFakeFreshness) SystemsFreshness(_ context.Context, _ int) ([]domainScouting.SystemFreshnessSnapshot, error) {
	return f.snapshots, nil
}

type tuneFakePostRepo struct{ posts []*domainScouting.ScoutPost }

func (f *tuneFakePostRepo) ListActive(_ context.Context, _ int) ([]*domainScouting.ScoutPost, error) {
	return f.posts, nil
}
func (f *tuneFakePostRepo) Upsert(_ context.Context, _ *domainScouting.ScoutPost) error { return nil }
func (f *tuneFakePostRepo) Remove(_ context.Context, _ int, _ string) error             { return nil }

type tuneFakeFleet struct {
	idle []*navigation.Ship
	all  []*navigation.Ship
}

func (f *tuneFakeFleet) FindIdleByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return f.idle, nil
}
func (f *tuneFakeFleet) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return f.all, nil
}

type tuneFakeLedger struct{ txns []*ledger.Transaction }

func (f *tuneFakeLedger) Create(_ context.Context, _ *ledger.Transaction) error { return nil }
func (f *tuneFakeLedger) FindByID(_ context.Context, _ ledger.TransactionID, _ shared.PlayerID) (*ledger.Transaction, error) {
	return nil, nil
}
func (f *tuneFakeLedger) CountByPlayer(_ context.Context, _ shared.PlayerID, _ ledger.QueryOptions) (int, error) {
	return len(f.txns), nil
}
func (f *tuneFakeLedger) FindByPlayer(_ context.Context, _ shared.PlayerID, opts ledger.QueryOptions) ([]*ledger.Transaction, error) {
	out := make([]*ledger.Transaction, 0, len(f.txns))
	for _, tx := range f.txns {
		if opts.StartDate != nil && tx.Timestamp().Before(*opts.StartDate) {
			continue
		}
		out = append(out, tx)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp().After(out[j].Timestamp()) })
	if opts.Limit > 0 && len(out) > opts.Limit {
		out = out[:opts.Limit]
	}
	return out, nil
}

type tuneFakeTreasury struct{ credits int }

func (f *tuneFakeTreasury) LiveCredits(_ context.Context, _ shared.PlayerID) (int, error) {
	return f.credits, nil
}

type tuneFakePurchaser struct {
	quotePrice int
	buyCalls   int
}

func (f *tuneFakePurchaser) QuoteProbe(_ context.Context, _ shared.PlayerID, _ probebuy.ProbeTarget) (int, string, error) {
	return f.quotePrice, "X1-HOME-YARD", nil
}
func (f *tuneFakePurchaser) BuyProbe(_ context.Context, _ shared.PlayerID, _ int, _ probebuy.ProbeTarget) (int, string, error) {
	f.buyCalls++
	return f.quotePrice, "PROBE-NEW", nil
}

type tuneFakeRecorder struct{ events []*captain.Event }

func (f *tuneFakeRecorder) Record(_ context.Context, e *captain.Event) error {
	f.events = append(f.events, e)
	return nil
}

func tuneProbe(t *testing.T, playerID int, symbol string) *navigation.Ship {
	t.Helper()
	loc, err := shared.NewWaypoint("X1-HOME-A1", 0, 0)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(100, 100)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(0, 0, nil)
	require.NoError(t, err)
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(playerID), loc, fuel, 100, 0, cargo, 30, "FRAME_PROBE", "SATELLITE", nil, navigation.NavStatusInOrbit)
	require.NoError(t, err)
	return ship
}

func tuneProbeTxn(t *testing.T, playerID int, ts time.Time, price int) *ledger.Transaction {
	t.Helper()
	tx, err := ledger.NewTransaction(
		shared.MustNewPlayerID(playerID), ts, ledger.TransactionTypePurchaseShip,
		-price, price+10, 10, "Purchased SHIP_PROBE",
		map[string]interface{}{"ship_type": "SHIP_PROBE"}, "", "", "tune-test",
	)
	require.NoError(t, err)
	return tx
}

const (
	tuneSizerContainerID          = "market_freshness_sizer_coordinator-player-tune-test"
	tuneFrontierContainerID       = "frontier_expansion_coordinator-player-tune-test"
	sizerContainerType            = "MARKET_FRESHNESS_SIZER_COORDINATOR"
	frontierContainerType         = "FRONTIER_EXPANSION_COORDINATOR"
	scoutPostContainerType        = "SCOUT_POST_COORDINATOR"
	shipyardBackfillContainerType = "SHIPYARD_BACKFILL_COORDINATOR"
	contractCoordinatorType       = "CONTRACT_FLEET_COORDINATOR"
)

// newTunedSizer wires a real freshness-sizer handler whose live-config reader reads
// the REAL container repo — demand 2 (60 markets × 120s / 3600s SLA per system),
// supply 1, rich treasury, cheap probe, one ledger probe buy 5 minutes ago. Whether
// a tick buys is then governed ONLY by the cooldown + spend-cap knobs under test.
func newTunedSizer(t *testing.T, repo *persistence.ContainerRepositoryGORM, playerID int, now time.Time) (*scoutingCmd.RunMarketFreshnessSizerCoordinatorHandler, *tuneFakePurchaser) {
	t.Helper()
	fr := &tuneFakeFreshness{snapshots: []domainScouting.SystemFreshnessSnapshot{{
		SystemSymbol: "X1-VB74", MarketCount: 60, OldestAgeSeconds: 100,
		MeasuredCycleSeconds: 120, CycleSamples: 59,
	}}}
	lr := &tuneFakeLedger{txns: []*ledger.Transaction{tuneProbeTxn(t, playerID, now.Add(-5*time.Minute), 90000)}}
	h := scoutingCmd.NewRunMarketFreshnessSizerCoordinatorHandler(
		fr, &tuneFakePostRepo{}, &tuneFakeFleet{all: []*navigation.Ship{tuneProbe(t, playerID, "PROBE-A")}},
		lr, &shared.MockClock{CurrentTime: now},
	)
	h.SetTreasuryReader(&tuneFakeTreasury{credits: 1_000_000})
	pu := &tuneFakePurchaser{quotePrice: 30000}
	h.SetProbePurchaser(pu)
	h.SetLiveConfigReader(NewContainerConfigReader(repo))
	return h, pu
}

// ---- acceptance: the motivating retune, live, no restart --------------------

// THE sp-0z7f ACCEPTANCE (freshness sizer): the exact motivating retune — purchase
// cooldown 10m→1m and spend cap 100k→500k — applied LIVE through the tune mechanism
// against the persisted config column, observed to change the buy gate on the NEXT
// reconcile tick, with the launch command untouched (no restart, no rebuild).
func TestTune_FreshnessSizer_LiveRetuneChangesBuyGateNextTick_NoRestart(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	now := time.Now()
	seedTuneContainer(t, db, playerID, tuneSizerContainerID, sizerContainerType, "market_freshness_sizer_coordinator", "RUNNING", map[string]interface{}{
		"container_id":           tuneSizerContainerID,
		"purchase_cooldown_secs": 600,
		"max_spend_per_cycle":    100000,
	})
	h, pu := newTunedSizer(t, repo, playerID, now)
	cmd := &scoutingCmd.RunMarketFreshnessSizerCoordinatorCommand{
		PlayerID: shared.MustNewPlayerID(playerID), ContainerID: tuneSizerContainerID,
		PurchaseCooldownSecs: 600, MaxSpendPerCycle: 100000,
	}
	s := &DaemonServer{containerRepo: repo}
	ctx := context.Background()

	// Tick 1 — launch values govern: last probe buy was 5m ago, cooldown is 10m → no buy.
	require.NoError(t, h.ReconcileOnce(ctx, cmd))
	require.Zero(t, pu.buyCalls, "tick 1: the 10m launch cooldown must block the buy")

	// LIVE RETUNE #1 (the motivating cooldown change): 10m → 1m. No restart.
	out, err := s.MutateContainerConfigKey(ctx, tuneSizerContainerID, "", "purchase_cooldown_secs", 60, playerID)
	require.NoError(t, err)
	require.True(t, out.Changed)
	require.Equal(t, 600, out.OldEffective)
	require.Equal(t, 60, out.NewEffective)

	// Tick 2 — cooldown now clears (5m > 1m), but the 100k spend cap binds: 90k already
	// spent in the trailing window + a 30k quote > 100k → still no buy. The cooldown
	// tune demonstrably took effect (the gate moved from cooldown to spend cap).
	require.NoError(t, h.ReconcileOnce(ctx, cmd))
	require.Zero(t, pu.buyCalls, "tick 2: cooldown cleared but the 100k spend cap must block the buy")

	// LIVE RETUNE #2 (the motivating spend change): 100k → 500k. No restart.
	out, err = s.MutateContainerConfigKey(ctx, tuneSizerContainerID, "", "max_spend_per_cycle", 500000, playerID)
	require.NoError(t, err)
	require.True(t, out.Changed)

	// Tick 3 — both retuned gates clear → the buy fires.
	require.NoError(t, h.ReconcileOnce(ctx, cmd))
	require.Equal(t, 1, pu.buyCalls, "tick 3: with cooldown 1m and spend cap 500k the buy must fire")

	// No restart happened: the launch-frozen command still carries the old values —
	// the coordinator acted on the LIVE column, not on a rebuilt command.
	require.Equal(t, 600, cmd.PurchaseCooldownSecs)
	require.Equal(t, 100000, cmd.MaxSpendPerCycle)
}

// THE sp-0z7f ACCEPTANCE (frontier): the same live cooldown retune lands on the
// frontier coordinator's next tick — resolved by OPERATION TYPE (the `--operation
// frontier` path, FindActiveCoordinatorByType) rather than by container id.
func TestTune_Frontier_LiveRetuneViaOperation_ChangesBuyGateNextTick(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	now := time.Now()
	seedTuneContainer(t, db, playerID, tuneFrontierContainerID, frontierContainerType, "frontier_expansion_coordinator", "RUNNING", map[string]interface{}{
		"container_id":           tuneFrontierContainerID,
		"purchase_cooldown_secs": 600,
	})
	// One standing post with an unmanned slot (demand 1), zero probes (supply 0),
	// probe buy 5m ago, rich treasury, cheap quote — only the cooldown gates the buy.
	lr := &tuneFakeLedger{txns: []*ledger.Transaction{tuneProbeTxn(t, playerID, now.Add(-5*time.Minute), 1000)}}
	h := expansionCmd.NewRunFrontierExpansionCoordinatorHandler(
		&tuneFakePostRepo{posts: []*domainScouting.ScoutPost{{PlayerID: playerID, SystemSymbol: "X1-A", Kind: domainScouting.PostKindStanding}}},
		&tuneFakeFleet{}, lr, &shared.MockClock{CurrentTime: now},
	)
	h.SetTreasuryReader(&tuneFakeTreasury{credits: 1_000_000})
	pu := &tuneFakePurchaser{quotePrice: 1000}
	h.SetProbePurchaser(pu)
	h.SetLiveConfigReader(NewContainerConfigReader(repo))
	cmd := &expansionCmd.RunFrontierExpansionCoordinatorCommand{
		PlayerID: shared.MustNewPlayerID(playerID), ContainerID: tuneFrontierContainerID,
		PurchaseCooldownSecs: 600,
	}
	s := &DaemonServer{containerRepo: repo}
	ctx := context.Background()

	require.NoError(t, h.ReconcileOnce(ctx, cmd))
	require.Zero(t, pu.buyCalls, "tick 1: the 10m launch cooldown must block the buy")

	out, err := s.MutateContainerConfigKey(ctx, "", "frontier", "purchase_cooldown_secs", 60, playerID)
	require.NoError(t, err)
	require.True(t, out.Changed)
	require.Equal(t, tuneFrontierContainerID, out.ContainerID, "the operation alias must resolve to the active frontier coordinator")

	require.NoError(t, h.ReconcileOnce(ctx, cmd))
	require.Equal(t, 1, pu.buyCalls, "tick 2: the 1m live cooldown must let the buy fire")
}

// ---- rejection: bounds + unknown keys, no write ------------------------------

// A tune outside its registry bounds, with a negative value, or naming a key the
// engine does not expose is REJECTED before any write: the config column must be
// byte-identical afterwards (no silent partial state).
func TestTune_RejectsInvalidTunes_NoWrite(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	seedTuneContainer(t, db, playerID, tuneSizerContainerID, sizerContainerType, "market_freshness_sizer_coordinator", "RUNNING", map[string]interface{}{
		"container_id": tuneSizerContainerID, "purchase_cooldown_secs": 600,
	})
	s := &DaemonServer{containerRepo: repo}
	before := containerConfigJSON(t, repo, tuneSizerContainerID, playerID)

	cases := []struct {
		name  string
		key   string
		value int
	}{
		{"cooldown below min (10s floor)", "purchase_cooldown_secs", 5},
		{"cooldown above max (86400s ceiling)", "purchase_cooldown_secs", 90000},
		{"spend cap above max (5M ceiling)", "max_spend_per_cycle", 6_000_000},
		{"negative value", "max_spend_per_cycle", -1},
		{"unknown key for this engine", "warp_speed", 42},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.MutateContainerConfigKey(context.Background(), tuneSizerContainerID, "", tc.key, tc.value, playerID)
			require.Error(t, err)
			require.Equal(t, before, containerConfigJSON(t, repo, tuneSizerContainerID, playerID),
				"a rejected tune must leave the config column byte-identical")
		})
	}
}

// Target resolution failures are clear operator errors: an unknown operation alias,
// a missing container, a STOPPED container, and an engine with no tunable registry.
func TestTune_RejectsUnresolvableTargets(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	seedTuneContainer(t, db, playerID, "stopped-sizer", sizerContainerType, "market_freshness_sizer_coordinator", "STOPPED", map[string]interface{}{"container_id": "stopped-sizer"})
	seedTuneContainer(t, db, playerID, "gas-coord-1", "GAS_COORDINATOR", "gas_coordinator", "RUNNING", map[string]interface{}{"container_id": "gas-coord-1"})
	s := &DaemonServer{containerRepo: repo}
	ctx := context.Background()

	_, err := s.MutateContainerConfigKey(ctx, "", "bogus-operation", "max_spend_per_cycle", 1000, playerID)
	require.Error(t, err, "an unknown operation alias must be rejected")

	_, err = s.MutateContainerConfigKey(ctx, "no-such-container", "", "max_spend_per_cycle", 1000, playerID)
	require.Error(t, err, "a missing container must be rejected")

	_, err = s.MutateContainerConfigKey(ctx, "stopped-sizer", "", "max_spend_per_cycle", 1000, playerID)
	require.Error(t, err, "a STOPPED container must be rejected — tune targets RUNNING/PENDING work")

	_, err = s.MutateContainerConfigKey(ctx, "gas-coord-1", "", "max_spend_per_cycle", 1000, playerID)
	require.Error(t, err, "an engine with no tunable-knob registry must be rejected")

	_, err = s.MutateContainerConfigKey(ctx, "", "", "max_spend_per_cycle", 1000, playerID)
	require.Error(t, err, "one of container id or operation is required")
}

// ---- revert: 0 restores the documented default -------------------------------

// `tune <key> 0` reverts the knob: the key is removed from the config column, so
// the coordinator's default chain applies — the NEXT tick (and any restart rebuild)
// runs on the documented default const, reported honestly in the outcome.
func TestTune_ZeroRevertsKnobToDocumentedDefault(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	seedTuneContainer(t, db, playerID, tuneSizerContainerID, sizerContainerType, "market_freshness_sizer_coordinator", "RUNNING", map[string]interface{}{
		"container_id": tuneSizerContainerID, "purchase_cooldown_secs": 600,
	})
	s := &DaemonServer{containerRepo: repo}
	ctx := context.Background()

	out, err := s.MutateContainerConfigKey(ctx, tuneSizerContainerID, "", "purchase_cooldown_secs", 0, playerID)
	require.NoError(t, err)
	require.True(t, out.Changed, "reverting a set knob is an effective change")
	require.Equal(t, 600, out.OldEffective)
	require.Equal(t, "live-config", out.OldSource)
	require.Equal(t, 60, out.NewEffective, "the documented default (1m cooldown) applies after revert")
	require.Equal(t, "default", out.NewSource)

	// The column no longer carries a positive value for the key — the live reader
	// (the coordinator's tick-start snapshot) sees "no live value → default".
	snap, err := NewContainerConfigReader(repo).Snapshot(ctx, tuneSizerContainerID, playerID)
	require.NoError(t, err)
	_, set := snap.PositiveInt("purchase_cooldown_secs")
	require.False(t, set, "revert must clear the key from the config column")

	// Re-reverting an already-default knob is an honest no-op.
	out, err = s.MutateContainerConfigKey(ctx, tuneSizerContainerID, "", "purchase_cooldown_secs", 0, playerID)
	require.NoError(t, err)
	require.False(t, out.Changed)
}

// ---- restart-recovery: the config column is the recovery source --------------

// A tuned value SURVIVES restart recovery: recovery rebuilds the launch command from
// the config column (buildCommandForType), which now carries the tuned value — while
// an untuned knob still resolves to its default. This is the RULINGS #2 guarantee
// the sp-ev0n worker cap proved, applied to the sizer's money knobs.
func TestTune_TunedValueSurvivesRestartRecovery(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	seedTuneContainer(t, db, playerID, tuneSizerContainerID, sizerContainerType, "market_freshness_sizer_coordinator", "RUNNING", map[string]interface{}{
		"container_id": tuneSizerContainerID, "sla_seconds": 0,
	})
	s := &DaemonServer{containerRepo: repo, containerSpecs: map[string]ContainerSpec{}}
	s.registerContainerSpecs()
	ctx := context.Background()

	_, err := s.MutateContainerConfigKey(ctx, tuneSizerContainerID, "", "max_spend_per_cycle", 350000, playerID)
	require.NoError(t, err)

	// RESTART: reload the persisted config through the JSON round-trip recovery does
	// (numbers come back as float64) and rebuild through the SAME factory recovery uses.
	var reloaded map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(containerConfigJSON(t, repo, tuneSizerContainerID, playerID)), &reloaded))
	rebuilt, err := s.buildCommandForType("market_freshness_sizer_coordinator", reloaded, playerID, tuneSizerContainerID)
	require.NoError(t, err)
	cmd, ok := rebuilt.(*scoutingCmd.RunMarketFreshnessSizerCoordinatorCommand)
	require.True(t, ok)

	require.Equal(t, 350000, cmd.MaxSpendPerCycle, "recovery must read the TUNED value from the config column, not the default")
	require.Zero(t, cmd.SLASeconds, "an untuned knob stays unset — the coordinator default chain applies")
}

// ---- idempotency + audit ------------------------------------------------------

// Setting a knob to its current value is an honest no-op (no DB write), and every
// EFFECTIVE tune — and ONLY an effective tune — emits the config.tuned audit event:
// these knobs move real credits, so a change is never a silent DB write.
func TestTune_IdempotentNoOp_AndAuditOnEffectiveTunesOnly(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	seedTuneContainer(t, db, playerID, tuneSizerContainerID, sizerContainerType, "market_freshness_sizer_coordinator", "RUNNING", map[string]interface{}{
		"container_id": tuneSizerContainerID,
	})
	rec := &tuneFakeRecorder{}
	SetCaptainEventRecorder(rec)
	t.Cleanup(func() { SetCaptainEventRecorder(nil) })
	s := &DaemonServer{containerRepo: repo}
	ctx := context.Background()

	out, err := s.MutateContainerConfigKey(ctx, tuneSizerContainerID, "", "purchase_cooldown_secs", 120, playerID)
	require.NoError(t, err)
	require.True(t, out.Changed)
	require.Len(t, rec.events, 1, "an effective tune must emit exactly one audit event")
	require.Equal(t, captain.EventConfigTuned, rec.events[0].Type)
	require.Equal(t, playerID, rec.events[0].PlayerID)
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(rec.events[0].Payload), &payload))
	require.Equal(t, tuneSizerContainerID, payload["container_id"])
	require.Equal(t, "purchase_cooldown_secs", payload["key"])
	require.EqualValues(t, 60, payload["old_effective"], "the pre-tune effective value (default 1m) is audited")
	require.EqualValues(t, 120, payload["new_effective"])

	// Idempotent re-tune: same value → no write, no audit.
	out, err = s.MutateContainerConfigKey(ctx, tuneSizerContainerID, "", "purchase_cooldown_secs", 120, playerID)
	require.NoError(t, err)
	require.False(t, out.Changed, "re-tuning to the current value must be a no-op")
	require.Len(t, rec.events, 1, "a no-op must not emit an audit event")

	// Rejected tune: no write happened, so nothing to audit.
	_, err = s.MutateContainerConfigKey(ctx, tuneSizerContainerID, "", "purchase_cooldown_secs", 5, playerID)
	require.Error(t, err)
	require.Len(t, rec.events, 1, "a rejected tune must not emit an audit event")
}

// ---- registry invariants --------------------------------------------------------

// The bounds registry is the single documented source of truth for what is tunable:
// its key set and defaults must MATCH the coordinator-exported defaults maps (drift
// here would make a tune silently ineffective), every entry must carry sane bounds
// and metadata, and NO *_treasury_pct knob may ever exceed the compile-time 25%
// treasury guard — the guard is never weakened, made tunable, or bypassable.
func TestTuneRegistry_MatchesCoordinatorDefaults_AndNeverWeakensTreasuryGuard(t *testing.T) {
	registry := tunableKnobsByContainerType()

	engines := []struct {
		containerType string
		defaults      map[string]int
	}{
		{sizerContainerType, scoutingCmd.SizerTunableDefaults()},
		{frontierContainerType, expansionCmd.FrontierTunableDefaults()},
		{scoutPostContainerType, scoutingCmd.ScoutPostTunableDefaults()},
		{shipyardBackfillContainerType, scoutingCmd.ShipyardBackfillTunableDefaults()},
		{contractCoordinatorType, ContractCoordinatorTunableDefaults()},
	}
	for _, engine := range engines {
		knobs, ok := registry[engine.containerType]
		require.True(t, ok, "engine %s must be registered", engine.containerType)
		require.Len(t, knobs, len(engine.defaults), "registry keys must exactly match the coordinator's tunable set for %s", engine.containerType)
		for key, def := range engine.defaults {
			bound, ok := knobs[key]
			require.True(t, ok, "knob %s.%s must be registered", engine.containerType, key)
			require.Equal(t, def, bound.Default, "registry default for %s must equal the coordinator's documented default", key)
			require.Equal(t, "int", bound.Type)
			require.Greater(t, bound.Max, 0, "%s must carry a positive Max", key)
			require.GreaterOrEqual(t, bound.Max, bound.Min, "%s bounds must be ordered", key)
			require.NotEmpty(t, bound.Unit, "%s must carry a unit", key)
			require.NotEmpty(t, bound.Description, "%s must carry a description", key)
		}
	}

	// The treasury-fraction rule: any *_treasury_pct knob ever registered is capped at
	// the compile-time 25 guard. (Vacuously true today — neither migrated engine
	// exposes one — but the rule is pinned for every future registry entry.)
	for containerType, knobs := range registry {
		for key, bound := range knobs {
			if strings.Contains(key, "treasury_pct") {
				require.LessOrEqual(t, bound.Max, 25, "%s.%s: a treasury-pct knob may never exceed the 25%% hard guard", containerType, key)
				require.GreaterOrEqual(t, bound.Min, 1, "%s.%s: a treasury-pct knob floor is 1", containerType, key)
			}
		}
	}
}

// ---- reader fail-safe -----------------------------------------------------------

// When the live config becomes unreadable mid-run (row gone, transient DB gap) the
// tick falls back to the LAUNCH command's values — fail-safe, never a half-applied
// config: with live cooldown 60 the tick buys; with the row deleted the launch 10m
// cooldown governs again and the buy is blocked.
func TestTune_LiveConfigUnreadable_TickFallsBackToLaunchValues(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	now := time.Now()
	seedTuneContainer(t, db, playerID, tuneSizerContainerID, sizerContainerType, "market_freshness_sizer_coordinator", "RUNNING", map[string]interface{}{
		"container_id":           tuneSizerContainerID,
		"purchase_cooldown_secs": 60, // live: 1m — clears the 5m-old buy
		"max_spend_per_cycle":    500000,
	})
	h, pu := newTunedSizer(t, repo, playerID, now)
	cmd := &scoutingCmd.RunMarketFreshnessSizerCoordinatorCommand{
		PlayerID: shared.MustNewPlayerID(playerID), ContainerID: tuneSizerContainerID,
		PurchaseCooldownSecs: 600, MaxSpendPerCycle: 500000, // launch: 10m — blocks it
	}
	ctx := context.Background()

	require.NoError(t, h.ReconcileOnce(ctx, cmd))
	require.Equal(t, 1, pu.buyCalls, "with the live column readable, the 1m live cooldown lets the buy fire")

	require.NoError(t, repo.Remove(ctx, tuneSizerContainerID, playerID))

	require.NoError(t, h.ReconcileOnce(ctx, cmd))
	require.Equal(t, 1, pu.buyCalls, "with the live column unreadable, the 10m LAUNCH cooldown governs — no buy")
}

// ---- show: effective knobs + sources + bounds -----------------------------------

// The minimal `tune --show` for the migrated engines: every registered knob with its
// EFFECTIVE value, its source (live-config when the column carries a positive value —
// launch values share that store — else default), and its bounds, sorted by key.
func TestShowTunableConfig_ListsEffectiveValuesSourcesAndBounds(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	seedTuneContainer(t, db, playerID, tuneSizerContainerID, sizerContainerType, "market_freshness_sizer_coordinator", "RUNNING", map[string]interface{}{
		"container_id": tuneSizerContainerID, "purchase_cooldown_secs": 120,
	})
	s := &DaemonServer{containerRepo: repo}

	out, err := s.ShowTunableConfig(context.Background(), tuneSizerContainerID, "", playerID)
	require.NoError(t, err)
	require.Equal(t, tuneSizerContainerID, out.ContainerID)
	require.Equal(t, sizerContainerType, out.ContainerType)
	require.Len(t, out.Knobs, len(scoutingCmd.SizerTunableDefaults()), "every registered knob is listed")
	require.True(t, sort.SliceIsSorted(out.Knobs, func(i, j int) bool { return out.Knobs[i].Key < out.Knobs[j].Key }), "knobs are listed in stable key order")

	byKey := map[string]TunableKnobStatus{}
	for _, k := range out.Knobs {
		byKey[k.Key] = k
	}
	cooldown := byKey["purchase_cooldown_secs"]
	require.Equal(t, 120, cooldown.Effective)
	require.Equal(t, "live-config", cooldown.Source)
	require.Equal(t, 10, cooldown.Bound.Min)
	require.Equal(t, 86400, cooldown.Bound.Max)
	require.Equal(t, "seconds", cooldown.Bound.Unit)

	spend := byKey["max_spend_per_cycle"]
	require.Equal(t, 500000, spend.Effective, "an unset knob shows its documented default")
	require.Equal(t, "default", spend.Source)
	require.Equal(t, 5_000_000, spend.Bound.Max)
}
