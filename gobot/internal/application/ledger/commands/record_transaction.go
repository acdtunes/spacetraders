package commands

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// RecordTransactionCommand represents a command to record a financial transaction
type RecordTransactionCommand struct {
	PlayerID          int
	TransactionType   string
	Amount            int // Positive for income, negative for expenses
	BalanceBefore     int
	BalanceAfter      int
	Description       string
	Metadata          map[string]interface{}
	RelatedEntityType string
	RelatedEntityID   string
	OperationType     string     // Optional: operation type (e.g., "contract", "arbitrage", "rebalancing", "factory")
	Timestamp         *time.Time // Optional: if provided, use this timestamp; otherwise use current time

	// AuthoritativeBalance, when non-nil, is the agent's credit balance as
	// returned in-band by this transaction's OWN API response
	// (data.agent.credits from purchase/sell/refuel/accept/fulfill). It is
	// ground truth for balance_after and re-anchors the running chain to the
	// API. Prefer it over any reconstructed or separately-fetched balance:
	// a stale GetAgent snapshot must never overwrite the chain, but the
	// credits the server reports alongside the transaction always may.
	AuthoritativeBalance *int
}

// RecordTransactionResponse represents the result of recording a transaction
type RecordTransactionResponse struct {
	TransactionID string
	Timestamp     time.Time
}

// RecordTransactionHandler handles the RecordTransaction command
type RecordTransactionHandler struct {
	transactionRepo ledger.TransactionRepository
	clock           shared.Clock

	// Balance derivation reads the last row then writes the next; concurrent
	// recordings for one player (refuel hops + cargo buys land in the same
	// second mid-contract) raced that read-then-write and forked the running
	// balance. All recording flows through this single handler in the daemon
	// process, so a per-player mutex is sufficient serialization.
	balanceMu sync.Mutex
	playerMu  map[int]*sync.Mutex

	// Running balance per player, authoritative while the process lives.
	// Same-instant rows cannot be ordered reliably in the DB (random UUID
	// ids tie-break identical timestamps), so the serialized writer keeps
	// the chain in memory; the DB read only warms the cache after restart.
	lastBalance map[int]int
	balanceWarm map[int]bool
}

// NewRecordTransactionHandler creates a new RecordTransactionHandler
func NewRecordTransactionHandler(
	transactionRepo ledger.TransactionRepository,
	clock shared.Clock,
) *RecordTransactionHandler {
	// Default to real clock if not provided
	if clock == nil {
		clock = shared.NewRealClock()
	}

	return &RecordTransactionHandler{
		transactionRepo: transactionRepo,
		clock:           clock,
		playerMu:        make(map[int]*sync.Mutex),
		lastBalance:     make(map[int]int),
		balanceWarm:     make(map[int]bool),
	}
}

// Handle executes the RecordTransaction command
func (h *RecordTransactionHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RecordTransactionCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *RecordTransactionCommand")
	}

	// Parse and validate transaction type
	transactionType, err := ledger.ParseTransactionType(cmd.TransactionType)
	if err != nil {
		return nil, fmt.Errorf("invalid transaction type: %w", err)
	}

	// Create player ID
	playerID, err := shared.NewPlayerID(cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("invalid player ID: %w", err)
	}

	// Determine timestamp
	timestamp := h.clock.Now()
	if cmd.Timestamp != nil {
		timestamp = *cmd.Timestamp
	}

	mu := h.playerLock(cmd.PlayerID)
	mu.Lock()
	defer mu.Unlock()

	// Single-writer balance derivation. Three sources, in strict order of
	// authority (this is the sp-sc6u fix — balance_after had forked +~470k from
	// the live API by trusting reconstructed/stale values over API truth):
	//
	//  1. AuthoritativeBalance — the agent's credits as returned in-band by
	//     this transaction's OWN API response (data.agent.credits). Ground
	//     truth; it re-anchors the running chain to the API.
	//  2. Reconstruction — callers that skip the balance fetch pass
	//     balance_before=0. Chain balance_after off the last recorded balance.
	//     A zero balance_before with a nonzero amount is arithmetically
	//     impossible for a real transaction, so it unambiguously means "derive
	//     it for me": manufacturing sends 0/0, cargo/refuel send 0/amount —
	//     both reconstruct rather than corrupt or zero the chain.
	//  3. Explicit — caller supplied a self-consistent before/after pair it is
	//     sure of (no production caller does this anymore; retained for a
	//     genuine first-ever transaction and for callers that fetched truth).
	//
	// The serialized writer keeps the running balance in memory because
	// same-instant rows cannot be ordered reliably in the DB (random UUID ids
	// tie-break identical timestamps); the DB read only warms the cache.
	balanceBefore, balanceAfter := cmd.BalanceBefore, cmd.BalanceAfter
	switch {
	case cmd.AuthoritativeBalance != nil:
		balanceAfter = *cmd.AuthoritativeBalance
		balanceBefore = balanceAfter - cmd.Amount
	case balanceBefore == 0 && cmd.Amount != 0:
		h.warmBalance(ctx, cmd.PlayerID, playerID)
		balanceBefore = h.lastBalance[cmd.PlayerID]
		balanceAfter = balanceBefore + cmd.Amount
	}

	// Create transaction entity
	transaction, err := ledger.NewTransaction(
		playerID,
		timestamp,
		transactionType,
		cmd.Amount,
		balanceBefore,
		balanceAfter,
		cmd.Description,
		cmd.Metadata,
		cmd.RelatedEntityType,
		cmd.RelatedEntityID,
		cmd.OperationType,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create transaction: %w", err)
	}

	// Persist transaction
	if err := h.transactionRepo.Create(ctx, transaction); err != nil {
		return nil, fmt.Errorf("failed to persist transaction: %w", err)
	}
	// The serialized writer is now authoritative for this player's running
	// balance until the process dies. Whether the value came from in-band
	// credits, reconstruction, or an explicit pair, the persisted balance_after
	// is what the next record chains from.
	h.lastBalance[cmd.PlayerID] = balanceAfter
	h.balanceWarm[cmd.PlayerID] = true

	// Record transaction metrics
	// Extract category from transaction metadata (if available)
	category := ""
	if transaction.Category() != "" {
		category = string(transaction.Category())
	}

	// Extract agent symbol from metadata (if available)
	agentSymbol := ""
	if cmd.Metadata != nil {
		if agent, ok := cmd.Metadata["agent"].(string); ok {
			agentSymbol = agent
		}
	}
	// Fallback to a default agent if not provided
	if agentSymbol == "" {
		agentSymbol = "UNKNOWN"
	}

	// Record the transaction metrics
	metrics.RecordTransaction(
		cmd.PlayerID,
		agentSymbol,
		cmd.TransactionType,
		category,
		cmd.Amount,
		balanceAfter,
	)

	return &RecordTransactionResponse{
		TransactionID: transaction.ID().String(),
		Timestamp:     transaction.Timestamp(),
	}, nil
}

// warmBalance lazily seeds the in-memory running balance from the last persisted
// row after a restart. Caller must hold the player lock. It runs at most once
// per player per process (the DB read only warms a cold cache); every recorded
// transaction thereafter keeps lastBalance current in memory.
func (h *RecordTransactionHandler) warmBalance(ctx context.Context, playerIDInt int, playerID shared.PlayerID) {
	if h.balanceWarm[playerIDInt] {
		return
	}
	if last, err := h.transactionRepo.FindByPlayer(ctx, playerID, ledger.QueryOptions{
		Limit: 1, OrderBy: "timestamp DESC, created_at DESC, id DESC",
	}); err == nil && len(last) == 1 {
		h.lastBalance[playerIDInt] = last[0].BalanceAfter()
	}
	h.balanceWarm[playerIDInt] = true
}

func (h *RecordTransactionHandler) playerLock(playerID int) *sync.Mutex {
	h.balanceMu.Lock()
	defer h.balanceMu.Unlock()
	if h.playerMu[playerID] == nil {
		h.playerMu[playerID] = &sync.Mutex{}
	}
	return h.playerMu[playerID]
}
