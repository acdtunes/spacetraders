package main

// The call-site half is deliberately standalone, referencing nothing from the daemon package, so it
// can be run against ANY revision of main.go. sp-hxqao shipped a boot replay that passed every unit
// test of its own function while being wired where it could never fire: thorough coverage of a
// function says nothing about where it is plugged in.

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	domainTrading "github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// --- the call site ------------------------------------------------------------------------

// TestMainWiring_LaneDebtPersistenceIsArmedAndCorrectlyOrdered pins the three properties nothing
// below main.go can see: that the reload is wired at all, that the ledger exists first, and that it
// beats every handoff to an engine that accrues.
func TestMainWiring_LaneDebtPersistenceIsArmedAndCorrectlyOrdered(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	require.NoError(t, err)

	var armPos, ledgerPos, firstHandoffPos token.Pos
	noteHandoff := func(pos token.Pos) {
		if firstHandoffPos == token.NoPos || pos < firstHandoffPos {
			firstHandoffPos = pos
		}
	}

	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.KeyValueExpr:
			// The trade-route circuits receive the ledger as a struct field rather than a
			// setter, so the composite literal is a handoff too — and it is THIS bead's
			// handoff: the trade engine is the only writer of the full-lane keys.
			if ident, ok := node.Key.(*ast.Ident); ok && ident.Name == "laneCooldown" {
				noteHandoff(node.Pos())
			}
		case *ast.CallExpr:
			switch fn := node.Fun.(type) {
			case *ast.Ident:
				if fn.Name == "armLaneCooldownPersistence" {
					armPos = node.Pos()
				}
			case *ast.SelectorExpr:
				switch fn.Sel.Name {
				case "armLaneCooldownPersistence", "SetDebtPersister":
					// Either shape counts: the helper, or the persister wired inline.
					// Matching both makes this a check on the DEFECT rather than on the
					// shape of the fix.
					if armPos == token.NoPos {
						armPos = node.Pos()
					}
				case "NewLaneCooldownLedger":
					ledgerPos = node.Pos()
				case "SetSourceCooldown":
					noteHandoff(node.Pos())
				}
			}
		}
		return true
	})

	require.NotEqual(t, token.NoPos, armPos,
		"the lane-debt reload must be wired at the composition root, or the trade engine boots amnesiac and the fix is inert")
	require.NotEqual(t, token.NoPos, ledgerPos)
	require.Less(t, int(ledgerPos), int(armPos), "the reload must run after the ledger it fills exists")

	require.NotEqual(t, token.NoPos, firstHandoffPos, "no engine is handed the ledger, so nothing accrues into it")
	require.Less(t, int(armPos), int(firstHandoffPos),
		"the reload must run BEFORE the ledger reaches an engine that accrues: Restore refuses a key already carrying debt, so a later reload silently restores nothing")
}

// --- the behaviour ------------------------------------------------------------------------

type stubLaneDebtStore struct {
	loaded  []persistence.LaneDebt
	loadErr error
	saveErr error
	saved   []persistence.LaneDebt
}

func (s *stubLaneDebtStore) Save(_ context.Context, key domainTrading.LaneKey, debt float64, at time.Time) error {
	s.saved = append(s.saved, persistence.LaneDebt{Key: key, Debt: debt, AccruedAt: at})
	return s.saveErr
}

func (s *stubLaneDebtStore) LoadSince(_ context.Context, _ time.Time) ([]persistence.LaneDebt, error) {
	return s.loaded, s.loadErr
}

func laneDebtLedger() *domainTrading.LaneCooldownLedger {
	return domainTrading.NewLaneCooldownLedger(0.05, 0.015, 750*time.Minute)
}

func aLane() domainTrading.LaneKey {
	return domainTrading.LaneKey{Source: "X1-KP46-A1", Dest: "X1-KP46-B2", Good: "IRON"}
}

func TestArmLaneCooldownPersistence_RestoresStoredLanes(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	store := &stubLaneDebtStore{loaded: []persistence.LaneDebt{
		{Key: aLane(), Debt: 0.0325, AccruedAt: now.Add(-time.Hour)},
	}}
	ledger := laneDebtLedger()

	require.Equal(t, 1, armLaneCooldownPersistence(context.Background(), ledger, store, 750*time.Minute, now))
	require.Positive(t, ledger.Debt(aLane(), now))
}

// The write-through must be armed even when the reload found nothing — a first boot against an
// empty table is exactly the case that most needs it. Arming only on a non-empty reload would make
// the very first lane of every fresh deployment unrecoverable.
func TestArmLaneCooldownPersistence_ArmsTheWriteThroughOnAnEmptyStore(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	store := &stubLaneDebtStore{}
	ledger := laneDebtLedger()

	require.Equal(t, 0, armLaneCooldownPersistence(context.Background(), ledger, store, 750*time.Minute, now))

	ledger.Accrue(aLane(), 30, 60, now)
	require.Len(t, store.saved, 1, "the write-through must be armed even when the reload restored nothing")
	require.Equal(t, aLane(), store.saved[0].Key)
}

// A guard's repair path must never be the reason the daemon does not start, and a store error on
// the sell path must never propagate into a completed leg.
func TestArmLaneCooldownPersistence_SurvivesAnUnreadableAndUnwritableStore(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	store := &stubLaneDebtStore{loadErr: errors.New("database is down"), saveErr: errors.New("still down")}
	ledger := laneDebtLedger()

	require.Equal(t, 0, armLaneCooldownPersistence(context.Background(), ledger, store, 750*time.Minute, now))
	require.NotPanics(t, func() { ledger.Accrue(aLane(), 30, 60, now) })
	require.Positive(t, ledger.Debt(aLane(), now), "the debt is still live in memory; only its survival of the next restart is lost")
}

// A nil store leaves the ledger exactly as it was before this existed rather than panicking the
// boot — the same best-effort contract the purchase replay holds.
func TestArmLaneCooldownPersistence_NilStoreDoesNotPanic(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	require.NotPanics(t, func() {
		require.Equal(t, 0, armLaneCooldownPersistence(context.Background(), laneDebtLedger(), nil, 750*time.Minute, now))
	})
}
