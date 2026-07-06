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
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
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

	result, err := client.Register(ctx, accountToken, agentSymbol, faction)
	if err != nil {
		return fmt.Errorf("failed to register agent: %w", err)
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
		AgentSymbol: result.AgentSymbol,
		Token:       result.Token,
		CreatedAt:   now,
		Metadata:    metadata,
	}

	era := &persistence.EraModel{
		Name:         strings.ToLower(result.AgentSymbol),
		AgentSymbol:  result.AgentSymbol,
		RegisteredAt: &now,
	}
	if faction != "" {
		era.Faction = &faction
	}
	if resetDate, err := time.Parse(eraDateLayout, status.ResetDate); err == nil {
		era.UniverseResetDate = &resetDate
	}

	if err := store.CreatePlayerWithEra(ctx, player, era); err != nil {
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
	cfg, err := config.LoadConfig("")
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	db, err := database.NewConnection(&cfg.Database)
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}

	client := api.NewSpaceTradersClient()
	store := persistence.NewEraRepository(db)
	accountToken := os.Getenv("ST_ACCOUNT_TOKEN")

	return runPlayerRegisterNew(context.Background(), client, store, accountToken, agentSymbol, faction, os.Stdout)
}
