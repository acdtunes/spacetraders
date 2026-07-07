package cli

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	playerCmd "github.com/andrescamacho/spacetraders-go/internal/application/player/commands"
	playerQuery "github.com/andrescamacho/spacetraders-go/internal/application/player/queries"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// NewPlayerCommand creates the player command with subcommands
func NewPlayerCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "player",
		Short: "Manage players and agents",
		Long: `Manage players and agents in the local database.

Players represent your SpaceTraders agents with their authentication tokens.
Use these commands to register new agents, list existing ones, and view details.

Examples:
  spacetraders player register --agent ENDURANCE --token <jwt-token>
  spacetraders player list
  spacetraders player info --agent ENDURANCE
  spacetraders player info --player-id 1`,
	}

	// Add subcommands
	cmd.AddCommand(newPlayerRegisterCommand())
	cmd.AddCommand(newPlayerListCommand())
	cmd.AddCommand(newPlayerInfoCommand())

	return cmd
}

// newPlayerRegisterCommand creates the player register subcommand
func newPlayerRegisterCommand() *cobra.Command {
	var (
		agentSymbol string
		token       string
		faction     string
		newAgent    bool
	)

	cmd := &cobra.Command{
		Use:   "register",
		Short: "Register a new player/agent",
		Long: `Register a new player/agent with the SpaceTraders API token.

You must first register with the SpaceTraders API at https://spacetraders.io
to obtain your unique agent symbol and JWT token.

The token will be stored securely in the local database and used for all
API requests on behalf of this agent.

Example:
  spacetraders player register --agent ENDURANCE --token eyJ... --faction COSMIC`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if newAgent {
				return runPlayerRegisterNewCommand(agentSymbol, faction)
			}

			if agentSymbol == "" {
				return fmt.Errorf("--agent flag is required")
			}
			if token == "" {
				return fmt.Errorf("--token flag is required")
			}

			// Load config and connect to database
			cfg, err := config.LoadConfig("")
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			db, err := database.NewConnection(&cfg.Database)
			if err != nil {
				return fmt.Errorf("failed to connect to database: %w", err)
			}

			// Create repository and handler
			playerRepo := persistence.NewGormPlayerRepository(db)
			handler := playerCmd.NewRegisterPlayerHandler(playerRepo)

			// Prepare metadata
			metadata := make(map[string]interface{})
			if faction != "" {
				metadata["starting_faction"] = faction
			}

			// Execute command
			ctx := context.Background()
			response, err := handler.Handle(ctx, &playerCmd.RegisterPlayerCommand{
				AgentSymbol: agentSymbol,
				Token:       token,
				Metadata:    metadata,
			})
			if err != nil {
				return fmt.Errorf("failed to register player: %w", err)
			}

			result := response.(*playerCmd.RegisterPlayerResponse)

			fmt.Println("✓ Player registered successfully")
			fmt.Printf("  Agent Symbol: %s\n", result.Player.AgentSymbol)
			fmt.Printf("  Player ID:    %d\n", result.Player.ID)
			if faction != "" {
				fmt.Printf("  Faction:      %s\n", faction)
			}
			fmt.Println("\nSet as default player with: spacetraders config set-player --agent", agentSymbol)

			return nil
		},
	}

	cmd.Flags().StringVar(&agentSymbol, "agent", "", "Agent symbol (required)")
	cmd.Flags().StringVar(&token, "token", "", "SpaceTraders API JWT token (required unless --new)")
	cmd.Flags().StringVar(&faction, "faction", "", "Starting faction (optional)")
	cmd.Flags().BoolVar(&newAgent, "new", false, "Register a new agent via the API using ST_ACCOUNT_TOKEN and create its era row")

	return cmd
}

// newPlayerListCommand creates the player list subcommand
func newPlayerListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all registered players",
		Long: `List all players/agents registered in the local database.

Shows player ID, agent symbol, credits, and registration date.

Example:
  spacetraders player list`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load config and connect to database
			cfg, err := config.LoadConfig("")
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			db, err := database.NewConnection(&cfg.Database)
			if err != nil {
				return fmt.Errorf("failed to connect to database: %w", err)
			}

			// Query all players directly (TODO: add ListAll to repository)
			var models []persistence.PlayerModel
			result := db.Find(&models)
			if result.Error != nil {
				return fmt.Errorf("failed to list players: %w", result.Error)
			}

			if len(models) == 0 {
				fmt.Println("No players registered.")
				fmt.Println("\nRegister a player with: spacetraders player register --agent <symbol> --token <jwt>")
				return nil
			}

			// Display table
			// NOTE: Credits not shown in list view - use 'player info' to fetch fresh credits from API
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tAGENT SYMBOL\tCREATED")
			fmt.Fprintln(w, "--\t------------\t-------")

			for _, model := range models {
				created := model.CreatedAt.Format("2006-01-02")
				fmt.Fprintf(w, "%d\t%s\t%s\n",
					model.ID,
					model.AgentSymbol,
					created,
				)
			}

			w.Flush()

			return nil
		},
	}

	return cmd
}

// newPlayerInfoCommand creates the player info subcommand
func newPlayerInfoCommand() *cobra.Command {
	var showToken bool

	cmd := &cobra.Command{
		Use:   "info",
		Short: "Show detailed player information",
		Long: `Show detailed information about a specific player.

Specify the player using either --player-id or --agent flag.

The API token is masked by default so it does not accumulate in logs or
transcripts. Pass --show-token to print the full token.

Examples:
  spacetraders player info --player-id 1
  spacetraders player info --agent ENDURANCE
  spacetraders player info --show-token`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load config and connect to database
			cfg, err := config.LoadConfig("")
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			db, err := database.NewConnection(&cfg.Database)
			if err != nil {
				return fmt.Errorf("failed to connect to database: %w", err)
			}

			// Create repository, API client, and handler
			playerRepo := persistence.NewGormPlayerRepository(db)
			apiClient := api.NewSpaceTradersClient()
			handler := playerQuery.NewGetPlayerHandler(playerRepo, apiClient)

			// Resolve the effective player (flags > persisted default) and inject its
			// token so the handler can fetch live credits from the agent API. Without
			// this injection GetPlayerHandler's PlayerTokenFromContext lookup fails with
			// "player token not found in context".
			ctx := context.Background()
			resolved, err := resolveDefaultPlayer(ctx, playerRepo)
			if err != nil {
				return err
			}
			ctx = auth.WithPlayerToken(ctx, resolved.Token)

			resolvedID := resolved.ID.Value()
			response, err := handler.Handle(ctx, &playerQuery.GetPlayerQuery{
				PlayerID: &resolvedID,
			})
			if err != nil {
				return fmt.Errorf("failed to get player: %w", err)
			}

			result := response.(*playerQuery.GetPlayerResponse)
			p := result.Player

			// Display player info
			fmt.Printf("Player Information\n")
			fmt.Printf("==================\n\n")
			fmt.Printf("Player ID:     %d\n", p.ID.Value())
			fmt.Printf("Agent Symbol:  %s\n", p.AgentSymbol)
			fmt.Printf("Credits:       %d\n", p.Credits)

			if p.StartingFaction != "" {
				fmt.Printf("Faction:       %s\n", p.StartingFaction)
			}

			if p.Metadata != nil {
				if hq, ok := p.Metadata["headquarters"].(string); ok {
					fmt.Printf("Headquarters:  %s\n", hq)
				}
				if accountID, ok := p.Metadata["account_id"].(string); ok {
					fmt.Printf("Account ID:    %s\n", accountID)
				}
				if lastSynced, ok := p.Metadata["last_synced"].(string); ok {
					if t, err := time.Parse(time.RFC3339, lastSynced); err == nil {
						fmt.Printf("Last Synced:   %s\n", t.Format("2006-01-02 15:04:05"))
					}
				}
			}

			fmt.Printf("\nToken: %s\n", maskToken(p.Token, showToken))

			return nil
		},
	}

	cmd.Flags().BoolVar(&showToken, "show-token", false, "Print the full API token instead of a masked prefix")

	return cmd
}

// maskToken renders an API token for display. SpaceTraders tokens are long-lived
// bearer credentials, so by default only a short prefix is shown to keep full
// tokens out of logs and transcripts. Callers pass showFull (via --show-token)
// to reveal the whole token when a human explicitly asks for it.
func maskToken(token string, showFull bool) string {
	if showFull {
		return token
	}

	const prefixLen = 12
	if len(token) <= prefixLen {
		// Too short to reveal a prefix without exposing most of the secret;
		// slicing would also panic, so hide it entirely.
		return "..."
	}

	return token[:prefixLen] + "..."
}
