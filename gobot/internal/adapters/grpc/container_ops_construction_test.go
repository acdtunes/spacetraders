package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func strPtr(s string) *string   { return &s }
func f64Ptr(f float64) *float64 { return &f }

// seedOverridePlayer inserts a players row so the FK-enforcing test harness (sp-55aa) accepts a
// pipeline row referencing it. Local to this package (the persistence package's seedPlayer is not
// exported to grpc tests).
func seedOverridePlayer(t *testing.T, db *gorm.DB, id int, symbol string) {
	t.Helper()
	require.NoError(t, db.Create(&persistence.PlayerModel{
		ID: id, AgentSymbol: symbol, Token: "tok", CreatedAt: time.Now(),
	}).Error)
}

// TestMutateConstructionGoodOverride_LiveSetPersistsAndClears drives the sp-pdb3 acceptance end to
// end through the REAL persistence path (an in-memory pipeline row = the restart-durable store):
// setting FAB_MATS to {LIMITED, prefer-buy} on the running pipeline persists exactly that good's
// override, leaves the non-overridden ADVANCED_CIRCUITRY at the global default (byte-identical), and
// a subsequent --clear reverts FAB_MATS to the global default. The reload via FindByConstructionSite
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
		goodOverridePatch{minSupply: strPtr("LIMITED"), strategy: strPtr("prefer-buy")}, false)
	require.NoError(t, err)
	require.True(t, res.Changed)

	reloaded, err := repo.FindByConstructionSite(ctx, "X1-VB74-I55", 1)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	require.Equal(t, "LIMITED", reloaded.GoodOverrides()["FAB_MATS"].MinSupply)
	require.Equal(t, "prefer-buy", reloaded.GoodOverrides()["FAB_MATS"].Strategy)
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

// These tests pin applyGoodOverride (sp-pdb3), the PURE merge/clear at the heart of the live
// `construction override` verb: it produces the next per-good GoodGatingOverrides map from a patch,
// leaving every other good byte-identical and clamping the price-ceiling multiplier to the domain
// guardrail (RULINGS #4). The find→persist plumbing (MutateConstructionGoodOverride) wraps it; the
// daemon is the single writer (RULINGS #3).

func TestApplyGoodOverride_SetsAbsentGood_LeavesOthersUntouched(t *testing.T) {
	current := manufacturing.GoodGatingOverrides{
		"ADVANCED_CIRCUITRY": {MinSupply: "MODERATE"},
	}
	next, result, changed := applyGoodOverride(current, "FAB_MATS",
		goodOverridePatch{minSupply: strPtr("LIMITED"), strategy: strPtr("prefer-buy")}, false)

	require.True(t, changed)
	require.Equal(t, "LIMITED", result.MinSupply)
	require.Equal(t, "prefer-buy", result.Strategy)
	require.Equal(t, "LIMITED", next["FAB_MATS"].MinSupply)
	require.Equal(t, "prefer-buy", next["FAB_MATS"].Strategy)
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
