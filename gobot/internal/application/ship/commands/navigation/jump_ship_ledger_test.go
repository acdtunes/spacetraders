package navigation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	ledgerCommands "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// This file is the acceptance gate for sp-shq63: the jump gate fee must reach
// the financial ledger.
//
// THE FALSIFIER IS RECONCILIATION, NOT INSERTION. Asserting "a JUMP row appears"
// passes with the balance wired wrong, and a ledger whose balance is wrong in the
// OVER-reporting direction is precisely what authorises an unaffordable buy. Every
// test below therefore asserts the ledger's latest balance_after against the
// SIMULATED LIVE AGENT CREDITS (ledgerJumpAPIClient.credits) — one oracle, mutated
// by the fake server itself, never a second hardcoded copy of the expected number.

// ledgerJumpAPIClient models a live agent whose credits actually move. JumpShip
// debits the gate fee from `credits` exactly as the server does, then reports the
// post-transaction balance in-band, so `credits` is an independent oracle for what
// the ledger must agree with.
type ledgerJumpAPIClient struct {
	ports.APIClient

	credits int // the agent's LIVE balance — the reconciliation oracle
	fee     int // the gate fee the server charges per jump

	// omitAgentBlock reproduces an API response with no data.agent: the
	// authoritative balance is then unavailable and the ledger must fall back
	// to chain reconstruction rather than record nothing.
	omitAgentBlock bool

	jumps int
}

func (c *ledgerJumpAPIClient) JumpShip(_ context.Context, _ string, _ string, _ string) (*ports.JumpResult, error) {
	c.jumps++
	c.credits -= c.fee // the server charges the gate fee

	result := &ports.JumpResult{
		DestinationSystem:   "X1-CD34",
		DestinationWaypoint: "X1-CD34-GATE",
		CooldownSeconds:     60,
		TotalPrice:          c.fee,
	}
	if !c.omitAgentBlock {
		credits := c.credits
		result.AgentCredits = &credits
	}
	return result, nil
}

func (c *ledgerJumpAPIClient) GetJumpGate(_ context.Context, _, _, _ string) (*ports.JumpGateData, error) {
	return &ports.JumpGateData{Symbol: "X1-AB12-GATE", Connections: []string{"X1-CD34-GATE"}}, nil
}

// jumpLedgerHarness wires the REAL RecordTransactionHandler over a real
// (constraint-enforcing) test DB behind a real mediator, so the jump handler
// exercises the true recording path rather than a spy that would accept any
// balance at all.
type jumpLedgerHarness struct {
	handler  *JumpShipHandler
	api      *ledgerJumpAPIClient
	repo     ledger.TransactionRepository
	playerID shared.PlayerID
	db       *gorm.DB
}

func newJumpLedgerHarness(t *testing.T, startingCredits, fee int) *jumpLedgerHarness {
	t.Helper()

	db, err := database.NewTestConnection()
	require.NoError(t, err)

	playerRow := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerRow).Error)
	playerID := shared.MustNewPlayerID(playerRow.ID)

	txRepo := persistence.NewGormTransactionRepository(db)
	med := mediator.NewMediator()
	require.NoError(t, mediator.RegisterHandler[*ledgerCommands.RecordTransactionCommand](
		med, ledgerCommands.NewRecordTransactionHandler(txRepo, nil)))

	gate := newJumpGateWaypoint(t, "X1-AB12-GATE")
	ship := newJumpTestShip(t, "PROBE-1", gate)
	apiClient := &ledgerJumpAPIClient{credits: startingCredits, fee: fee}

	handler := NewJumpShipHandler(
		&stubJumpShipRepo{ship: ship},
		&stubJumpPlayerRepo{playerEntity: player.NewPlayer(playerID, "TORWIND", "tok")},
		apiClient,
		med,
		&stubJumpContainerRepo{},
		nil,
		&shared.MockClock{CurrentTime: time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)},
	)

	return &jumpLedgerHarness{handler: handler, api: apiClient, repo: txRepo, playerID: playerID, db: db}
}

func (h *jumpLedgerHarness) jump(t *testing.T) {
	t.Helper()
	pid := h.playerID.Value()
	_, err := h.handler.Handle(context.Background(), &JumpShipCommand{
		ShipSymbol:        "PROBE-1",
		DestinationSystem: "X1-CD34",
		PlayerID:          &pid,
	})
	require.NoError(t, err)
}

// latest returns the most recent ledger row, failing if the ledger is empty —
// an empty read must never be mistaken for a satisfied assertion.
func (h *jumpLedgerHarness) latest(t *testing.T) *ledger.Transaction {
	t.Helper()
	txs, err := h.repo.FindByPlayer(context.Background(), h.playerID, ledger.QueryOptions{
		Limit: 1, OrderBy: "timestamp DESC, created_at DESC, id DESC",
	})
	require.NoError(t, err)
	require.Len(t, txs, 1, "ledger is empty: the jump gate fee was never recorded")
	return txs[0]
}

// record drives an ordinary (non-jump) transaction through the same ledger, used
// to seed a chain the jump must then close across.
func (h *jumpLedgerHarness) record(t *testing.T, txType string, amount int, authoritative *int) {
	t.Helper()
	handler := ledgerCommands.NewRecordTransactionHandler(h.repo, nil)
	_, err := handler.Handle(context.Background(), &ledgerCommands.RecordTransactionCommand{
		PlayerID: h.playerID.Value(), TransactionType: txType, Amount: amount,
		BalanceBefore: 0, BalanceAfter: amount,
		AuthoritativeBalance: authoritative, Description: "seed",
	})
	require.NoError(t, err)
}

// TestJumpFeeReconcilesLedgerWithLiveCredits is THE acceptance test. After a jump
// the ledger's latest balance_after must equal the agent's live credits.
//
// Kills the mutation "drop the ledger write": with no JUMP row the latest
// balance_after is still the pre-jump anchor (100000) while live credits are
// 100000-fee, so the equality fails by exactly the fee.
func TestJumpFeeReconcilesLedgerWithLiveCredits(t *testing.T) {
	const startingCredits, fee = 100000, 5343
	h := newJumpLedgerHarness(t, startingCredits, fee)

	// Anchor the chain with an ordinary transaction, so the jump has a chain to
	// close ACROSS rather than being the very first row.
	anchor := startingCredits
	h.record(t, string(ledger.TransactionTypeSellCargo), 12000, &anchor)
	require.Equal(t, startingCredits, h.latest(t).BalanceAfter(), "precondition: chain anchored at live credits")

	h.jump(t)

	require.Equal(t, 1, h.api.jumps, "precondition: the fake server actually charged one jump")

	latest := h.latest(t)
	require.Equal(t, string(ledger.TransactionTypeJump), string(latest.TransactionType()))
	require.Equal(t, h.api.credits, latest.BalanceAfter(),
		"ledger's latest balance_after must equal the agent's LIVE credits after the jump")
	require.Equal(t, startingCredits-fee, h.api.credits, "sanity: the server debited exactly the fee")
}

// TestJumpFeeChainClosesAcrossTheJump proves the chain is continuous THROUGH the
// jump: the row before it, the jump row, and the row after it must form an
// unbroken balance chain that still lands on live credits at the end.
func TestJumpFeeChainClosesAcrossTheJump(t *testing.T) {
	const startingCredits, fee = 250000, 5100
	h := newJumpLedgerHarness(t, startingCredits, fee)

	anchor := startingCredits
	h.record(t, string(ledger.TransactionTypeSellCargo), 30000, &anchor)

	h.jump(t)

	jumpRow := h.latest(t)
	require.Equal(t, startingCredits, jumpRow.BalanceBefore(), "jump row must chain off the anchor's balance_after")
	require.Equal(t, startingCredits-fee, jumpRow.BalanceAfter())

	// A later ordinary transaction with no authoritative balance must reconstruct
	// from the JUMP row — i.e. the jump is now part of the chain, not a hole in it.
	h.record(t, string(ledger.TransactionTypeRefuel), -400, nil)

	after := h.latest(t)
	require.Equal(t, startingCredits-fee, after.BalanceBefore(),
		"the post-jump transaction must chain off the JUMP row, proving the jump closed the chain")
	require.Equal(t, startingCredits-fee-400, after.BalanceAfter())
}

// TestJumpFeeReanchorsRatherThanAppends kills the mutation "record the row
// without AuthoritativeBalance". It forces the reconstructed chain to DISAGREE
// with reality by moving credits out-of-band (income the ledger never saw), so a
// row that merely appends `amount` lands on the wrong balance while a row that
// re-anchors to data.agent.credits lands on the right one. A plain
// insertion-only assertion cannot tell these apart.
func TestJumpFeeReanchorsRatherThanAppends(t *testing.T) {
	const fee = 5200
	h := newJumpLedgerHarness(t, 100000, fee)

	anchor := 100000
	h.record(t, string(ledger.TransactionTypeSellCargo), 5000, &anchor)

	// Out-of-band income the ledger never observed: the agent really holds
	// 180000 before the jump, but the recorded chain still says 100000.
	h.api.credits = 180000

	h.jump(t)

	latest := h.latest(t)
	require.Equal(t, h.api.credits, latest.BalanceAfter(),
		"balance_after must be the API's in-band credits (re-anchor), not lastBalance+amount (append)")
	require.Equal(t, 180000-fee, latest.BalanceAfter())
	require.NotEqual(t, 100000-fee, latest.BalanceAfter(),
		"a reconstructed-only row would have landed here; AuthoritativeBalance was not passed")
}

// TestJumpFeeIsRecordedAsAnExpense kills the mutation "record the fee with the
// wrong sign". A positive amount would make the jump look like INCOME and push
// the ledger's balance ABOVE the agent's real credits — the exact over-reporting
// direction a fail-closed money guard cannot survive.
func TestJumpFeeIsRecordedAsAnExpense(t *testing.T) {
	const startingCredits, fee = 100000, 5400
	h := newJumpLedgerHarness(t, startingCredits, fee)

	anchor := startingCredits
	h.record(t, string(ledger.TransactionTypeSellCargo), 1000, &anchor)

	h.jump(t)

	latest := h.latest(t)
	require.Equal(t, -fee, latest.Amount(), "a gate fee is an expense: amount must be negative")
	require.Less(t, latest.BalanceAfter(), latest.BalanceBefore(), "a jump must never raise the balance")
	require.Equal(t, startingCredits, latest.BalanceBefore(),
		"balance_before = credits - amount; a flipped sign would derive credits + fee here")
	require.Equal(t, string(ledger.CategoryTravelCosts), string(latest.Category()))
}

// TestJumpFeeRecordsTheDepartureSystem kills the mutation "record the fee without
// naming where the hull left from".
//
// The fee is a property of the DEPARTURE gate — the origin system explains 99.7% of the
// variance, and the same edge costs 27% more in one direction than the other — so a row
// that names only the destination cannot be attributed to the gate that charged it. The
// per-gate table is built by grouping on exactly this field; without it the table can only
// be reconstructed by a window function over each hull's jump history, which mis-attributes
// silently whenever a hull's previous row is not where this jump actually started.
func TestJumpFeeRecordsTheDepartureSystem(t *testing.T) {
	const startingCredits, fee = 100000, 5400
	h := newJumpLedgerHarness(t, startingCredits, fee)

	anchor := startingCredits
	h.record(t, string(ledger.TransactionTypeSellCargo), 1000, &anchor)

	h.jump(t)

	meta := h.latest(t).Metadata()
	require.NotNil(t, meta, "the fee row must carry metadata to be attributable")
	require.Equal(t, "X1-AB12", meta["origin_system"],
		"the row must name the system the hull DEPARTED, not only where it arrived")
	// Both endpoints, so a mutation that swapped them cannot pass.
	require.Equal(t, "X1-CD34", meta["destination_system"])
}

// TestJumpFeeWithoutAgentBlockStillRecords covers the API omitting data.agent.
// The spend is real either way, so the row must still be written (reconstructed
// from the chain) — recording nothing would restore the over-reporting gap.
func TestJumpFeeWithoutAgentBlockStillRecords(t *testing.T) {
	const startingCredits, fee = 100000, 5000
	h := newJumpLedgerHarness(t, startingCredits, fee)
	h.api.omitAgentBlock = true

	anchor := startingCredits
	h.record(t, string(ledger.TransactionTypeSellCargo), 2000, &anchor)

	h.jump(t)

	latest := h.latest(t)
	require.Equal(t, string(ledger.TransactionTypeJump), string(latest.TransactionType()))
	require.Equal(t, -fee, latest.Amount())
	require.Equal(t, startingCredits-fee, latest.BalanceAfter(), "reconstructed from the chain when no agent block is present")
}

// TestZeroFeeJumpWritesNoRow: ledger.Validate rejects amount == 0, so a free jump
// must be skipped rather than attempted-and-logged-as-an-error. Guards against a
// naive "always record" that would spam ERROR logs on every free jump.
func TestZeroFeeJumpWritesNoRow(t *testing.T) {
	h := newJumpLedgerHarness(t, 100000, 0)

	anchor := 100000
	h.record(t, string(ledger.TransactionTypeSellCargo), 7000, &anchor)

	h.jump(t)

	require.Equal(t, string(ledger.TransactionTypeSellCargo), string(h.latest(t).TransactionType()),
		"a zero-fee jump must not write a row; the seed transaction must still be the latest")
}

// TestJumpSucceedsWhenLedgerIsUnavailable: recording is best-effort. The hull has
// already moved server-side, so a ledger failure must never fail the jump and
// strand the caller's model of where the hull is. Here the mediator has NO
// RecordTransactionCommand handler registered, so Send returns an error.
func TestJumpSucceedsWhenLedgerIsUnavailable(t *testing.T) {
	h := newJumpLedgerHarness(t, 100000, 5000)

	gate := newJumpGateWaypoint(t, "X1-AB12-GATE")
	handler := NewJumpShipHandler(
		&stubJumpShipRepo{ship: newJumpTestShip(t, "PROBE-1", gate)},
		&stubJumpPlayerRepo{playerEntity: player.NewPlayer(h.playerID, "TORWIND", "tok")},
		h.api,
		mediator.NewMediator(), // empty: no handler registered for RecordTransactionCommand
		&stubJumpContainerRepo{},
		nil,
		&shared.MockClock{CurrentTime: time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)},
	)

	pid := h.playerID.Value()
	resp, err := handler.Handle(context.Background(), &JumpShipCommand{
		ShipSymbol: "PROBE-1", DestinationSystem: "X1-CD34", PlayerID: &pid,
	})
	require.NoError(t, err, "a ledger failure must not fail the jump")
	require.True(t, resp.(*JumpShipResponse).Success)
}
