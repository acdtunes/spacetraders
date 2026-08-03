package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

type registrationAPI interface {
	GetServerStatus(ctx context.Context) (*api.ServerStatus, error)
	Register(ctx context.Context, accountToken, agentSymbol, faction string) (*api.RegisterResult, error)
}

type registrationStore interface {
	FindOpenEra(ctx context.Context) (*persistence.EraModel, error)
	CreatePlayerWithEra(ctx context.Context, player *persistence.PlayerModel, era *persistence.EraModel) error
}

func runPlayerRegisterNew(ctx context.Context, client registrationAPI, store registrationStore, accountToken, agentSymbol, faction string, out io.Writer) error {
	if accountToken == "" {
		return fmt.Errorf("account token is required: set ST_ACCOUNT_TOKEN")
	}
	if agentSymbol == "" {
		return fmt.Errorf("--agent flag is required")
	}

	// --faction shipped as "(optional)" with an empty default and was passed straight
	// into Register, which is the same 422 (code 3001, invalid_enum_value, received "")
	// that broke `universe transition` — this path was equally unable to mint
	// (sp-dqbzm). Resolve and validate it up front, before the irreversible Register
	// call and before any row is written.
	faction, err := resolveMintFaction(faction)
	if err != nil {
		return err
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

	// Parse (and validate) the server reset date before minting an agent
	// token: era names are keyed by "<symbol>-<resetDate>" so the same agent
	// symbol can be reused across universe resets without colliding with the
	// unique eras.name constraint. If we can't derive that name, refuse
	// before calling Register rather than minting a token for an era we
	// can't name.
	resetDate, err := time.Parse(eraDateLayout, status.ResetDate)
	if err != nil {
		return fmt.Errorf("failed to parse server reset date %q: %w", status.ResetDate, err)
	}

	result, err := client.Register(ctx, accountToken, agentSymbol, faction)
	if err != nil {
		return fmt.Errorf("failed to register agent: %w", err)
	}

	now := time.Now().UTC()

	// faction is non-empty by construction (resolveMintFaction substitutes the default),
	// so both sinks are written unconditionally — an always-true guard here would only
	// suggest an unreachable empty case.
	raw, err := json.Marshal(map[string]string{"starting_faction": faction})
	if err != nil {
		return fmt.Errorf("failed to encode metadata: %w", err)
	}
	metadata := string(raw)

	player := &persistence.PlayerModel{
		AgentSymbol: result.AgentSymbol,
		Token:       result.Token,
		CreatedAt:   now,
		Metadata:    metadata,
	}

	era := &persistence.EraModel{
		Name:              strings.ToLower(result.AgentSymbol) + "-" + resetDate.Format(eraDateLayout),
		AgentSymbol:       result.AgentSymbol,
		RegisteredAt:      &now,
		UniverseResetDate: &resetDate,
		Faction:           &faction,
	}

	if err := store.CreatePlayerWithEra(ctx, player, era); err != nil {
		fmt.Fprintln(out, "⚠ WARNING: agent registered with the API but FAILED TO PERSIST locally.")
		fmt.Fprintln(out, "  This token will not be shown again — SAVE IT NOW:")
		fmt.Fprintf(out, "  Agent Symbol: %s\n", player.AgentSymbol)
		fmt.Fprintf(out, "  Token:        %s\n", player.Token)
		return fmt.Errorf("failed to persist player and era: %w", err)
	}

	fmt.Fprintln(out, "✓ New agent registered")
	fmt.Fprintf(out, "  Agent Symbol: %s\n", player.AgentSymbol)
	fmt.Fprintf(out, "  Player ID:    %d\n", player.ID)
	fmt.Fprintf(out, "  Era:          %s\n", era.Name)
	fmt.Fprintln(out, "\nSet as default player with: spacetraders config set-player --agent", agentSymbol)

	return nil
}

func runPlayerRegisterNewCommand(agentSymbol, faction string) error {
	db, err := openDatabase()
	if err != nil {
		return err
	}

	client := api.NewSpaceTradersClient()
	store := persistence.NewEraRepository(db)
	accountToken := os.Getenv("ST_ACCOUNT_TOKEN")

	return runPlayerRegisterNew(context.Background(), client, store, accountToken, agentSymbol, faction, os.Stdout)
}
