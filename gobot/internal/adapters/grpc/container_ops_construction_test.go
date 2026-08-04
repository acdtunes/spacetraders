package grpc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func strPtr(s string) *string   { return &s }
func f64Ptr(f float64) *float64 { return &f }

// seedOverridePlayer inserts a players row so the FK-enforcing test harness accepts a
// pipeline row referencing it. Local to this package (the persistence package's seedPlayer is not
// exported to grpc tests).
func seedOverridePlayer(t *testing.T, db *gorm.DB, id int, symbol string) {
	t.Helper()
	require.NoError(t, db.Create(&persistence.PlayerModel{
		ID: id, AgentSymbol: symbol, Token: "tok", CreatedAt: time.Now(),
	}).Error)
}

// TestMutateConstructionGoodOverride_LiveSetPersistsAndClears drives the acceptance end to
// end through the REAL persistence path (an in-memory pipeline row = the restart-durable store):
// setting FAB_MATS to {LIMITED} on the running pipeline persists exactly that good's override,
// leaves the non-overridden ADVANCED_CIRCUITRY at the global default (byte-identical), and a
// subsequent --clear reverts FAB_MATS to the global default. The reload via FindByConstructionSite
// is the daemon-bounce equivalent — the override survives it (RULINGS #2).
func TestMutateConstructionGoodOverride_LiveSetPersistsAndClears(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedOverridePlayer(t, db, 1, "PDB3-AGENT")

	repo := persistence.NewGormManufacturingPipelineRepository(db)
	ctx := context.Background()

	// An active (PLANNING) construction pipeline with the global MODERATE floor and no overrides.
	pipeline := manufacturing.NewConstructionPipeline("X1-VB74-I55", 1, 3, 5)
	pipeline.SetMinSupply("MODERATE")
	require.NoError(t, repo.Create(ctx, pipeline))

	s := &DaemonServer{db: db}

	// Loosen FAB_MATS only.
	res, err := s.MutateConstructionGoodOverride(ctx, "X1-VB74-I55", 1, "FAB_MATS",
		goodOverridePatch{minSupply: strPtr("LIMITED")}, false)
	require.NoError(t, err)
	require.True(t, res.Changed)

	reloaded, err := repo.FindByConstructionSite(ctx, "X1-VB74-I55", 1)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	require.Equal(t, "LIMITED", reloaded.GoodOverrides()["FAB_MATS"].MinSupply)
	// The non-overridden good keeps the global floor — byte-identical to today.
	require.Equal(t, "MODERATE",
		reloaded.GoodOverrides().MinSupplyFor("ADVANCED_CIRCUITRY", reloaded.MinSupply()),
		"a non-overridden good must still resolve to the global floor")
	_, advPresent := reloaded.GoodOverrides()["ADVANCED_CIRCUITRY"]
	require.False(t, advPresent, "only the targeted good gets an override entry")

	// Clear FAB_MATS → reverts to the global default.
	clr, err := s.MutateConstructionGoodOverride(ctx, "X1-VB74-I55", 1, "FAB_MATS", goodOverridePatch{}, true)
	require.NoError(t, err)
	require.True(t, clr.Changed)
	require.True(t, clr.Cleared)

	afterClear, err := repo.FindByConstructionSite(ctx, "X1-VB74-I55", 1)
	require.NoError(t, err)
	_, present := afterClear.GoodOverrides()["FAB_MATS"]
	require.False(t, present, "clearing reverts the good to the global default")
	require.Equal(t, "MODERATE",
		afterClear.GoodOverrides().MinSupplyFor("FAB_MATS", afterClear.MinSupply()),
		"after clear FAB_MATS resolves to the global floor again")
}

// TestMutateConstructionGoodOverride_NoActivePipelineErrors: setting an override for a site with no
// running construction pipeline is a clear operator error, not a silent no-op.
func TestMutateConstructionGoodOverride_NoActivePipelineErrors(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedOverridePlayer(t, db, 1, "PDB3-AGENT")

	s := &DaemonServer{db: db}
	_, err = s.MutateConstructionGoodOverride(context.Background(), "X1-NONE-I1", 1, "FAB_MATS",
		goodOverridePatch{minSupply: strPtr("LIMITED")}, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no active construction pipeline")
}

// --- sp-duljg: live max-workers verb (DaemonServer write path) -----------------------------------

// TestMutateConstructionMaxWorkers_LiveSetPersistsAndSurvivesReload drives the acceptance
// end to end through the REAL persistence path (the pipeline row is the restart-durable store):
// raising a RUNNING pipeline's max_workers from its launch value to 10 persists exactly that, and a
// reload via FindByConstructionSite — the daemon-bounce equivalent — still reports 10 (RULINGS #2).
// resolveWorkerCap reads MaxWorkers() off this same row every drain tick, so the change is honored
// live with no restart. Re-setting to 10 is a reported no-op that skips the write.
func TestMutateConstructionMaxWorkers_LiveSetPersistsAndSurvivesReload(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedOverridePlayer(t, db, 1, "DULJG-AGENT")

	repo := persistence.NewGormManufacturingPipelineRepository(db)
	ctx := context.Background()

	// A running construction pipeline launched with a cap of 1 (the "worker cap 1" incident).
	pipeline := manufacturing.NewConstructionPipeline("X1-FB5-I56", 1, 3, 1)
	require.NoError(t, repo.Create(ctx, pipeline))

	s := &DaemonServer{db: db}

	res, err := s.MutateConstructionMaxWorkers(ctx, "X1-FB5-I56", 1, 10)
	require.NoError(t, err)
	require.True(t, res.Changed)
	require.Equal(t, 10, res.WorkerCap)

	// Reload = daemon bounce: the live-set cap survived (RULINGS #2).
	reloaded, err := repo.FindByConstructionSite(ctx, "X1-FB5-I56", 1)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	require.Equal(t, 10, reloaded.MaxWorkers(), "the live-set cap must survive a daemon restart")

	// Re-setting to the current value is a no-op — skips the DB write, reported honestly.
	noop, err := s.MutateConstructionMaxWorkers(ctx, "X1-FB5-I56", 1, 10)
	require.NoError(t, err)
	require.False(t, noop.Changed)
	require.Equal(t, 10, noop.WorkerCap)
}

// TestMutateConstructionMaxWorkers_NoActivePipelineErrors: capping a site with no running
// construction pipeline is a clear operator error, not a silent no-op.
func TestMutateConstructionMaxWorkers_NoActivePipelineErrors(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedOverridePlayer(t, db, 1, "DULJG-AGENT")

	s := &DaemonServer{db: db}
	_, err = s.MutateConstructionMaxWorkers(context.Background(), "X1-NONE-I1", 1, 10)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no active construction pipeline")
}

// TestMutateConstructionMaxWorkers_RejectsNonPositiveCount: the daemon single-writer (RULINGS #3)
// enforces the >=1 bound and fails closed, so a raw gRPC caller cannot drive the drain's errgroup to
// SetLimit(0) (which would deadlock the tick). A rejected count must leave the stored cap untouched —
// no partial write.
func TestMutateConstructionMaxWorkers_RejectsNonPositiveCount(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedOverridePlayer(t, db, 1, "DULJG-AGENT")

	repo := persistence.NewGormManufacturingPipelineRepository(db)
	ctx := context.Background()
	pipeline := manufacturing.NewConstructionPipeline("X1-FB5-I56", 1, 3, 4)
	require.NoError(t, repo.Create(ctx, pipeline))

	s := &DaemonServer{db: db}
	for _, count := range []int{0, -3} {
		_, err := s.MutateConstructionMaxWorkers(ctx, "X1-FB5-I56", 1, count)
		require.Error(t, err, "count %d must be rejected before any write", count)

		reloaded, ferr := repo.FindByConstructionSite(ctx, "X1-FB5-I56", 1)
		require.NoError(t, ferr)
		require.Equal(t, 4, reloaded.MaxWorkers(), "a rejected count must not touch the stored cap")
	}
}

// These tests pin applyGoodOverride, the PURE merge/clear at the heart of the live
// `construction override` verb: it produces the next per-good GoodGatingOverrides map from a patch,
// leaving every other good byte-identical and clamping the price-ceiling multiplier to the domain
// guardrail (RULINGS #4). The find→persist plumbing (MutateConstructionGoodOverride) wraps it; the
// daemon is the single writer (RULINGS #3).

func TestApplyGoodOverride_SetsAbsentGood_LeavesOthersUntouched(t *testing.T) {
	current := manufacturing.GoodGatingOverrides{
		"ADVANCED_CIRCUITRY": {MinSupply: "MODERATE"},
	}
	next, result, changed := applyGoodOverride(current, "FAB_MATS",
		goodOverridePatch{minSupply: strPtr("LIMITED")}, false)

	require.True(t, changed)
	require.Equal(t, "LIMITED", result.MinSupply)
	require.Equal(t, "LIMITED", next["FAB_MATS"].MinSupply)
	require.Equal(t, manufacturing.GoodGatingOverride{MinSupply: "MODERATE"}, next["ADVANCED_CIRCUITRY"],
		"a non-targeted good must be byte-identical after a per-good override")
}

func TestApplyGoodOverride_MergePreservesUnpatchedKnobs(t *testing.T) {
	current := manufacturing.GoodGatingOverrides{
		"FAB_MATS": {Strategy: "prefer-buy", PriceCeilingMult: 2.0},
	}
	// Patch only min-supply: strategy and price-ceiling must survive (tune one dimension).
	next, result, changed := applyGoodOverride(current, "FAB_MATS",
		goodOverridePatch{minSupply: strPtr("SCARCE")}, false)

	require.True(t, changed)
	require.Equal(t, "prefer-buy", result.Strategy)
	require.Equal(t, 2.0, result.PriceCeilingMult)
	require.Equal(t, "SCARCE", result.MinSupply)
	require.Equal(t, result, next["FAB_MATS"])
}

func TestApplyGoodOverride_ClampsMultToDomainCap(t *testing.T) {
	next, result, changed := applyGoodOverride(nil, "FAB_MATS",
		goodOverridePatch{priceCeilingMult: f64Ptr(50)}, false)

	require.True(t, changed)
	require.Equal(t, manufacturing.MaxPriceCeilingMultiplier, result.PriceCeilingMult,
		"the daemon single-writer clamps the multiplier so a raw gRPC caller cannot bypass the guardrail")
	require.Equal(t, manufacturing.MaxPriceCeilingMultiplier, next["FAB_MATS"].PriceCeilingMult)
}

func TestApplyGoodOverride_NoOpWhenValueUnchanged(t *testing.T) {
	current := manufacturing.GoodGatingOverrides{
		"FAB_MATS": {MinSupply: "LIMITED"},
	}
	_, _, changed := applyGoodOverride(current, "FAB_MATS",
		goodOverridePatch{minSupply: strPtr("LIMITED")}, false)
	require.False(t, changed, "setting a good to its current value is a no-op (skips the DB write)")
}

func TestApplyGoodOverride_ClearRemovesGood_RevertsToGlobal(t *testing.T) {
	current := manufacturing.GoodGatingOverrides{
		"FAB_MATS":           {MinSupply: "LIMITED"},
		"ADVANCED_CIRCUITRY": {MinSupply: "MODERATE"},
	}
	next, _, changed := applyGoodOverride(current, "FAB_MATS", goodOverridePatch{}, true)

	require.True(t, changed)
	_, present := next["FAB_MATS"]
	require.False(t, present, "clearing removes the good's override, reverting it to the global default")
	require.Contains(t, next, "ADVANCED_CIRCUITRY", "clearing one good leaves the others intact")
}

func TestApplyGoodOverride_ClearAbsentGoodIsNoOp(t *testing.T) {
	current := manufacturing.GoodGatingOverrides{"ADVANCED_CIRCUITRY": {MinSupply: "MODERATE"}}
	_, _, changed := applyGoodOverride(current, "FAB_MATS", goodOverridePatch{}, true)
	require.False(t, changed, "clearing a good with no override is a no-op")
}

func TestApplyGoodOverride_DoesNotMutateInput(t *testing.T) {
	current := manufacturing.GoodGatingOverrides{"FAB_MATS": {MinSupply: "MODERATE"}}
	_, _, _ = applyGoodOverride(current, "FAB_MATS", goodOverridePatch{minSupply: strPtr("SCARCE")}, false)
	require.Equal(t, "MODERATE", current["FAB_MATS"].MinSupply, "the input map must not be mutated in place")
}

// stubAPIClient satisfies domainPorts.APIClient without implementing every method.
// Embedding the interface gives a valid implementation; unused methods panic if
// called, but this test only checks identity, never invokes API methods.
type stubAPIClient struct {
	domainPorts.APIClient
}

// TestGetAPIClientReturnsInjectedClient reproduces st-0tw.1: construction ops must
// use the shared, rate-limited API client injected at construction time, not a
// fresh SpaceTradersClient whose own limiter bypasses the account-wide budget.
func TestGetAPIClientReturnsInjectedClient(t *testing.T) {
	injected := &stubAPIClient{}
	s := &DaemonServer{apiClient: injected}

	got := s.getAPIClient()

	require.Same(t, injected, got,
		"getAPIClient must return the injected shared client, not a fresh instance")
}

// --- gate DELIVERY floors: the daemon single-writer (RULINGS #3) ---------------------------------

// TestMutateConstructionDeliveryFloors_PersistsAndSurvivesReload drives the acceptance through
// the REAL persistence path. This is the property the whole knob rests on: the pipeline ROW is
// both the store and the restart-recovery source, so the drain's per-leg re-read sees the tune
// with no restart and a daemon bounce does not revert it (RULINGS #2). Deliberately NOT a
// manufacturing config key: that coordinator is pattern B, and its resolve*Config would clobber
// a live tune from config.yaml on the next build.
func TestMutateConstructionDeliveryFloors_PersistsAndSurvivesReload(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedOverridePlayer(t, db, 1, "FLOORS-AGENT")

	repo := persistence.NewGormManufacturingPipelineRepository(db)
	ctx := context.Background()

	// A running pipeline with UNSET floors — the reader resolves the armed MODERATE/HIGH.
	pipeline := manufacturing.NewConstructionPipeline("X1-VB74-I55", 1, 3, 5)
	require.NoError(t, repo.Create(ctx, pipeline))
	require.Equal(t, "", pipeline.DeliveryBuyFloor(), "unset is the launch state")

	s := &DaemonServer{db: db}

	res, err := s.MutateConstructionDeliveryFloors(ctx, "X1-VB74-I55", 1, "LIMITED", "MODERATE")
	require.NoError(t, err)
	require.True(t, res.Changed)
	require.Equal(t, "LIMITED", res.BuyFloor)
	require.Equal(t, "MODERATE", res.ResumeFloor)

	// Reload = daemon bounce: the live-set floors survived it.
	reloaded, err := repo.FindByConstructionSite(ctx, "X1-VB74-I55", 1)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	require.Equal(t, "LIMITED", reloaded.DeliveryBuyFloor(), "the buy floor must survive a daemon restart")
	require.Equal(t, "MODERATE", reloaded.DeliveryResumeFloor(), "the resume floor must survive a daemon restart")

	// Re-sending the same pair is an honest no-op that skips the write.
	noop, err := s.MutateConstructionDeliveryFloors(ctx, "X1-VB74-I55", 1, "LIMITED", "MODERATE")
	require.NoError(t, err)
	require.False(t, noop.Changed)
	require.Equal(t, "LIMITED", noop.BuyFloor)
}

// TestMutateConstructionDeliveryFloors_OneSidedTuneKeepsTheOtherFloor: an EMPTY argument means
// "leave unchanged", never "reset". Those two intents must not share an encoding — an empty
// argument that meant reset would make "leave unchanged" inexpressible on a knob an operator
// tunes one end at a time. The unnamed side is still validated against the named one.
func TestMutateConstructionDeliveryFloors_OneSidedTuneKeepsTheOtherFloor(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedOverridePlayer(t, db, 1, "FLOORS-AGENT")

	repo := persistence.NewGormManufacturingPipelineRepository(db)
	ctx := context.Background()
	pipeline := manufacturing.NewConstructionPipeline("X1-VB74-I55", 1, 3, 5)
	pipeline.SetDeliveryFloors("LIMITED", "HIGH")
	require.NoError(t, repo.Create(ctx, pipeline))

	s := &DaemonServer{db: db}

	// Raise only the buy floor; the persisted HIGH resume floor is retained, not blanked.
	res, err := s.MutateConstructionDeliveryFloors(ctx, "X1-VB74-I55", 1, "MODERATE", "")
	require.NoError(t, err)
	require.True(t, res.Changed)
	require.Equal(t, "MODERATE", res.BuyFloor)
	require.Equal(t, "HIGH", res.ResumeFloor, "an unset argument must keep the persisted floor")

	reloaded, err := repo.FindByConstructionSite(ctx, "X1-VB74-I55", 1)
	require.NoError(t, err)
	require.Equal(t, "MODERATE", reloaded.DeliveryBuyFloor())
	require.Equal(t, "HIGH", reloaded.DeliveryResumeFloor())
}

// TestMutateConstructionDeliveryFloors_FailsClosedOnACollapsedGap: the CLI validates, but the
// daemon is the single writer and re-checks the ORDERING fail-closed, so a caller that reached
// it another way cannot persist a zero-gap hysteresis — which would reintroduce exactly the
// boundary chatter the second floor exists to prevent. A rejected pair must leave the stored
// floors untouched: no partial write.
func TestMutateConstructionDeliveryFloors_FailsClosedOnACollapsedGap(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedOverridePlayer(t, db, 1, "FLOORS-AGENT")

	repo := persistence.NewGormManufacturingPipelineRepository(db)
	ctx := context.Background()
	pipeline := manufacturing.NewConstructionPipeline("X1-VB74-I55", 1, 3, 5)
	pipeline.SetDeliveryFloors("LIMITED", "MODERATE")
	require.NoError(t, repo.Create(ctx, pipeline))

	s := &DaemonServer{db: db}

	for _, tc := range []struct{ name, buy, resume string }{
		{"resume below buy", "HIGH", "MODERATE"},
		{"resume equals buy", "HIGH", "HIGH"},
		// One-sided: MODERATE against the PERSISTED MODERATE resume floor is still a zero gap.
		{"one-sided collapse against the persisted floor", "MODERATE", ""},
		// ABUNDANT is the top of the ladder: nothing can sit strictly above it.
		{"top-of-ladder buy floor", "ABUNDANT", ""},
		// SCARCE is the bottom: nothing can sit strictly below it, so it is refused as a
		// resume floor against EVERY buy floor. The CLI guards this too; the daemon is the
		// fail-closed backstop for a caller that arrived another way.
		{"bottom-of-ladder resume floor", "", "SCARCE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.MutateConstructionDeliveryFloors(ctx, "X1-VB74-I55", 1, tc.buy, tc.resume)
			require.Error(t, err, "a collapsed hysteresis gap must be refused before any write")

			reloaded, ferr := repo.FindByConstructionSite(ctx, "X1-VB74-I55", 1)
			require.NoError(t, ferr)
			require.Equal(t, "LIMITED", reloaded.DeliveryBuyFloor(), "a rejected pair must not touch the stored floors")
			require.Equal(t, "MODERATE", reloaded.DeliveryResumeFloor())
		})
	}
}

// TestMutateConstructionDeliveryFloors_RejectsAnInvalidSupplyLevel: shared.ParseSupplyLevel is
// lenient by design (unknown -> MODERATE) because it parses scanned market data. Routing operator
// input through it unchecked would turn a typo into a silently-different floor, so the writer
// validates against the strict vocabulary first and names the accepted set.
func TestMutateConstructionDeliveryFloors_RejectsAnInvalidSupplyLevel(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedOverridePlayer(t, db, 1, "FLOORS-AGENT")

	repo := persistence.NewGormManufacturingPipelineRepository(db)
	ctx := context.Background()
	pipeline := manufacturing.NewConstructionPipeline("X1-VB74-I55", 1, 3, 5)
	pipeline.SetDeliveryFloors("LIMITED", "HIGH")
	require.NoError(t, repo.Create(ctx, pipeline))

	s := &DaemonServer{db: db}

	_, err = s.MutateConstructionDeliveryFloors(ctx, "X1-VB74-I55", 1, "PLENTIFUL", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "PLENTIFUL", "the refusal must name the rejected value")
	require.Contains(t, err.Error(), "SCARCE", "the refusal must list the accepted vocabulary")

	_, err = s.MutateConstructionDeliveryFloors(ctx, "X1-VB74-I55", 1, "", "LOTS")
	require.Error(t, err)
	require.Contains(t, err.Error(), "LOTS")

	reloaded, err := repo.FindByConstructionSite(ctx, "X1-VB74-I55", 1)
	require.NoError(t, err)
	require.Equal(t, "LIMITED", reloaded.DeliveryBuyFloor(), "a rejected value must not touch the stored floors")
	require.Equal(t, "HIGH", reloaded.DeliveryResumeFloor())
}

// TestMutateConstructionDeliveryFloors_ResolvesTheDefaultSideButPersistsRawUnset covers the
// most likely FIRST command against a fresh pipeline: set one floor, leave the other alone.
// The RESULT must report the pair actually in force — the response's contract is the RESOLVED
// floors, "what the knob became, not what was sent" — so the unset side reports the armed
// default and neither side is ever blank. The ROW meanwhile keeps the RAW unset value: freezing
// today's default onto the row would silently pin a pipeline to it and make "unset" unreachable.
func TestMutateConstructionDeliveryFloors_ResolvesTheDefaultSideButPersistsRawUnset(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedOverridePlayer(t, db, 1, "FLOORS-AGENT")

	repo := persistence.NewGormManufacturingPipelineRepository(db)
	ctx := context.Background()

	// A fresh pipeline: BOTH floors unset, exactly as `construction start` leaves it.
	pipeline := manufacturing.NewConstructionPipeline("X1-VB74-I55", 1, 3, 5)
	require.NoError(t, repo.Create(ctx, pipeline))

	s := &DaemonServer{db: db}

	res, err := s.MutateConstructionDeliveryFloors(ctx, "X1-VB74-I55", 1, "LIMITED", "")
	require.NoError(t, err)
	require.NotEmpty(t, res.BuyFloor, "the confirmation must never print a blank floor")
	require.NotEmpty(t, res.ResumeFloor, "the confirmation must never print a blank floor")
	require.Equal(t, "LIMITED", res.BuyFloor)
	require.Equal(t, string(gate.DefaultResumeFloor), res.ResumeFloor,
		"the unset side must report the armed default it actually resolves to")
	require.False(t, res.BuyFloorIsDefault, "an explicitly-set floor is not a default")
	require.True(t, res.ResumeFloorIsDefault,
		"the operator must be able to tell which half of the pair they actually control")

	// The ROW keeps the raw unset value — the default is resolved at read time, not frozen here.
	reloaded, err := repo.FindByConstructionSite(ctx, "X1-VB74-I55", 1)
	require.NoError(t, err)
	require.Equal(t, "LIMITED", reloaded.DeliveryBuyFloor())
	require.Equal(t, "", reloaded.DeliveryResumeFloor(),
		"an unset floor must stay unset on the row, never be frozen to today's default")
}

// TestMutateConstructionDeliveryFloors_CollapsedGapErrorDisclosesTheDefaultedSide: on a
// one-sided tune the refusal compares against a value the operator never typed. Naming one
// MODERATE and being told "MODERATE must be strictly above the buy floor MODERATE" reads as
// the knob contradicting itself unless the error discloses where the second MODERATE came from.
func TestMutateConstructionDeliveryFloors_CollapsedGapErrorDisclosesTheDefaultedSide(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedOverridePlayer(t, db, 1, "FLOORS-AGENT")

	repo := persistence.NewGormManufacturingPipelineRepository(db)
	ctx := context.Background()
	pipeline := manufacturing.NewConstructionPipeline("X1-VB74-I55", 1, 3, 5)
	require.NoError(t, repo.Create(ctx, pipeline))

	s := &DaemonServer{db: db}

	// Fresh row, only --resume-floor given: it is checked against the DEFAULT buy floor.
	_, err = s.MutateConstructionDeliveryFloors(ctx, "X1-VB74-I55", 1, "", "MODERATE")
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "unset buy floor defaults to",
		"the refusal must disclose the implicit value it compared against")
	require.Contains(t, err.Error(), string(gate.DefaultBuyFloor))

	// The mirror: only --buy-floor given, checked against the persisted resume floor's default.
	pipeline2 := manufacturing.NewConstructionPipeline("X1-FB5-I56", 1, 3, 5)
	require.NoError(t, repo.Create(ctx, pipeline2))
	_, err = s.MutateConstructionDeliveryFloors(ctx, "X1-FB5-I56", 1, "ABUNDANT", "")
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "unset resume floor defaults to")
}

// TestMutateConstructionDeliveryFloors_NoActivePipelineErrors: tuning the floors of a site with
// no running construction pipeline is a clear operator error, not a silent no-op.
func TestMutateConstructionDeliveryFloors_NoActivePipelineErrors(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedOverridePlayer(t, db, 1, "FLOORS-AGENT")

	s := &DaemonServer{db: db}
	_, err = s.MutateConstructionDeliveryFloors(context.Background(), "X1-NONE-I1", 1, "LIMITED", "MODERATE")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no active construction pipeline")
}
