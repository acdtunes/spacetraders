package grpc

// treasury_ports.go — the daemon's ONE treasury reader.
//
// Answering "how many credits do we have" with a live `Get Agent` call per money guard
// measures 0.167 req/s, 8.3% of the 2.00 req/s ceiling, and does not fall under
// request coalescing because the reads are invalidation-driven rather than duplicated —
// each buy, sell, refuel and jump empties the agent cache, forcing the next read cold.
//
// The reader assembled here prefers the transaction ledger, which already carries the
// same balance, and falls back to the (coalesced, short-TTL-cached) live call when the
// ledger is too old to trust. The fallback matters: on a quiet fleet inter-transaction
// gaps are unbounded, so the live path is the COMMON path when nothing is trading.
//
// The reader is constructed once in the daemon and shared by the tour coordinator, the
// trade-route circuit, the fleet autosizer and the contract scaler, so every money guard
// reads treasury the same way. Bootstrap is deliberately NOT routed through it — cold
// start has a legitimately empty ledger and must stay live-first.

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// liveAgentCredits adapts the API client's GetAgent to persistence.LiveCredits: it owns
// the token resolution and the AgentData unwrap so the ledger reader knows nothing about
// tokens or agents. Every failure is an ERROR — never a zero — because its only caller
// treats a treasury it could not read as "do not spend" (RULINGS #4).
type liveAgentCredits struct{ api agentReader }

// Credits live-reads the agent's balance through the coalesced GetAgent path.
func (r *liveAgentCredits) Credits(ctx context.Context) (int64, error) {
	if r.api == nil {
		return 0, errors.New("no API client wired for the live treasury read")
	}
	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return 0, fmt.Errorf("player token unavailable: %w", err)
	}
	agent, err := r.api.GetAgent(ctx, token)
	if err != nil {
		return 0, fmt.Errorf("live agent read failed: %w", err)
	}
	if agent == nil {
		return 0, errors.New("live agent read returned no agent")
	}
	return int64(agent.Credits), nil
}

// NewLedgerTreasuryReader builds the daemon's ledger-first treasury reader over the
// transaction ledger, with the live agent read as its fallback and the default 30s
// freshness bound.
func NewLedgerTreasuryReader(db *gorm.DB, api agentReader) *persistence.LedgerTreasury {
	return persistence.NewLedgerTreasury(db, &liveAgentCredits{api: api}, shared.NewRealClock(), 0)
}
