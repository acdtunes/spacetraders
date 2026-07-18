package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

// registrationStatusAPI is the narrow slice of registrationAPI the from-token
// path needs. Unlike --new it does NOT mint an agent (the caller supplies the
// token), so it only reads server status to derive the era's reset-date name.
// *api.SpaceTradersClient satisfies it, and the shared fake in the tests does too.
type registrationStatusAPI interface {
	GetServerStatus(ctx context.Context) (*api.ServerStatus, error)
}

// runPlayerRegisterFromToken imports an already-created agent (caller-supplied
// token) into the local DB AND opens its era row (sp-pr42). Before this, the
// from-token path wrote only the player row; `universe status` then reported
// "NO ERA" and era/reset detection ran blind off the primaryPlayerID players[0]
// fallback. It mirrors runPlayerRegisterNew minus the API Register call: same
// open-era guard, same <symbol>-<resetDate> era name, and the same atomic
// CreatePlayerWithEra so a player is never persisted without its era.
func runPlayerRegisterFromToken(ctx context.Context, client registrationStatusAPI, store registrationStore, agentSymbol, token, faction string, out io.Writer) error {
	if agentSymbol == "" {
		return fmt.Errorf("--agent flag is required")
	}
	if token == "" {
		return fmt.Errorf("--token flag is required")
	}

	open, err := store.FindOpenEra(ctx)
	if err != nil {
		return fmt.Errorf("failed to check open era: %w", err)
	}
	if open != nil {
		return fmt.Errorf("an OPEN era (%s) already exists; close it first", open.Name)
	}

	status, err := client.GetServerStatus(ctx)
	if err != nil {
		return fmt.Errorf("failed to get server status: %w", err)
	}

	// Derive (and validate) the era name from the server reset date before
	// persisting — era names are keyed by "<symbol>-<resetDate>" so the same
	// agent symbol can be reused across universe resets without colliding with
	// the unique eras.name constraint.
	resetDate, err := time.Parse(eraDateLayout, status.ResetDate)
	if err != nil {
		return fmt.Errorf("failed to parse server reset date %q: %w", status.ResetDate, err)
	}

	now := time.Now().UTC()

	metadata := ""
	if faction != "" {
		raw, err := json.Marshal(map[string]string{"starting_faction": faction})
		if err != nil {
			return fmt.Errorf("failed to encode metadata: %w", err)
		}
		metadata = string(raw)
	}

	player := &persistence.PlayerModel{
		AgentSymbol: agentSymbol,
		Token:       token,
		CreatedAt:   now,
		Metadata:    metadata,
	}

	era := &persistence.EraModel{
		Name:              strings.ToLower(agentSymbol) + "-" + resetDate.Format(eraDateLayout),
		AgentSymbol:       agentSymbol,
		RegisteredAt:      &now,
		UniverseResetDate: &resetDate,
	}
	if faction != "" {
		era.Faction = &faction
	}

	if err := store.CreatePlayerWithEra(ctx, player, era); err != nil {
		return fmt.Errorf("failed to persist player and era: %w", err)
	}

	fmt.Fprintln(out, "✓ Player registered")
	fmt.Fprintf(out, "  Agent Symbol: %s\n", player.AgentSymbol)
	fmt.Fprintf(out, "  Player ID:    %d\n", player.ID)
	fmt.Fprintf(out, "  Era:          %s\n", era.Name)
	fmt.Fprintln(out, "\nSet as default player with: spacetraders config set-player --agent", agentSymbol)

	return nil
}
