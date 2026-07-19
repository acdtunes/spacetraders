package watchkeeper

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
)

// agentCreditsAPI is the narrow slice of the SpaceTraders client the
// supervisor needs to read the player's LIVE agent credits — the same number
// the captain sees via `player info` (sp-sk68 D3). *api.SpaceTradersClient
// satisfies it.
type agentCreditsAPI interface {
	GetAgent(ctx context.Context, token string) (*player.AgentData, error)
}

// creditsProbeTimeout bounds a single live-credits fetch. refreshCredits runs
// on EVERY supervisor tick, before the detectors and the wake gate, so an
// unbounded GetAgent blocks interrupt delivery for the whole call. The shared
// client retries up to 10 times with per-request 30s HTTP timeouts and backoff
// sleeps that do NOT honor ctx, so one call during an API outage could hang a
// tick for ~10 minutes and push effective interrupt latency from <=30s to
// minutes for the outage's duration (sp-sk68 D3 follow-up). A short per-call
// deadline caps that stall: the very next tick retries, and the error path
// below already handles a failed probe.
const creditsProbeTimeout = 5 * time.Second

// SetAgentAPI wires the live agent-credit source and the player token used to
// authenticate it, without changing the constructor signature legacy tests
// depend on. When left unwired the supervisor falls back to the
// contract-anchored ledger reconstruction.
//
// The token is captured ONCE here. This codebase rotates players/tokens across
// eras, so after an era reset a still-running supervisor's token goes stale:
// GetAgent then fails every tick and refreshCredits keeps the last live value
// (or, if live was never observed, the ledger reconstruction). The
// universe-reset kill-switch (SetUniverseWatch / checkUniverseReset) halts the
// fleet on the reset it detects, and the watchkeeper MUST be restarted on
// era close to re-resolve the new token — live credits are not re-authenticated
// in place.
func (s *Supervisor) SetAgentAPI(client agentCreditsAPI, token string) {
	s.agentCredits = client
	s.playerToken = token
}

// refreshCredits updates s.lastCredits to the player's current credits,
// preferring the live agent API (ground truth — the value the captain sizes its
// thresholds from) and falling back to the contract-anchored ledger
// reconstruction only when the API is unavailable AND no live value has ever
// been observed. Every failure is logged, so a stale value is never evaluated
// against the wake gate silently.
//
// Once a live value has been observed, a transient GetAgent error RETAINS that
// last live value rather than flipping the gate back to the ledger
// reconstruction: the reconstruction can diverge from live, so a single API
// blip must not resurface an untruthful CreditsAbove/Below evaluation — and
// flip-flopping the source
// between ticks would also emit spurious credits-threshold crossings. When both
// sources are unavailable the last known value is retained, never reset to zero.
func (s *Supervisor) refreshCredits(ctx context.Context) {
	if s.agentCredits != nil {
		cctx, cancel := context.WithTimeout(ctx, creditsProbeTimeout)
		agent, err := s.agentCredits.GetAgent(cctx, s.playerToken)
		cancel()
		if err == nil {
			s.lastCredits = agent.Credits
			s.liveCreditsObserved = true
			return
		}
		if s.liveCreditsObserved {
			fmt.Printf("watchkeeper: live credits fetch failed, retaining last live value (%d): %v\n",
				s.lastCredits, err)
			return
		}
		fmt.Printf("watchkeeper: live credits fetch failed, falling back to reconstruction: %v\n", err)
	}
	credits, err := CurrentCredits(ctx, s.db, s.cfg.PlayerID)
	if err != nil {
		fmt.Printf("watchkeeper: credits reconstruction also failed, retaining last known (%d): %v\n",
			s.lastCredits, err)
		return
	}
	s.lastCredits = credits
}
