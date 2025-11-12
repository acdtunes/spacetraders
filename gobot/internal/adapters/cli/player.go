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
	"github.com/andrescamacho/spacetraders-go/internal/application/player"
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
			handler := player.NewRegisterPlayerHandler(playerRepo)

			// Prepare metadata
			metadata := make(map[string]interface{})
			if faction != "" {
				metadata["starting_faction"] = faction
			}

			// Execute command
			ctx := context.Background()
			response, err := handler.Handle(ctx, &player.RegisterPlayerCommand{
				AgentSymbol: agentSymbol,
				Token:       token,
				Metadata:    metadata,
			})
			if err != nil {
				return fmt.Errorf("failed to register player: %w", err)
			}

			result := response.(*player.RegisterPlayerResponse)

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
	cmd.Flags().StringVar(&token, "token", "", "SpaceTraders API JWT token (required)")
	cmd.Flags().StringVar(&faction, "faction", "", "Starting faction (optional)")

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
					model.PlayerID,
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
	cmd := &cobra.Command{
		Use:   "info",
		Short: "Show detailed player information",
		Long: `Show detailed information about a specific player.

Specify the player using either --player-id or --agent flag.

Examples:
  spacetraders player info --player-id 1
  spacetraders player info --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
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

			// Create repository, API client, and handler
			playerRepo := persistence.NewGormPlayerRepository(db)
			apiClient := api.NewSpaceTradersClient()
			handler := player.NewGetPlayerHandler(playerRepo, apiClient)

			// Execute command
			ctx := context.Background()
			var playerIDPtr *int
			if playerIdent.PlayerID > 0 {
				playerIDPtr = &playerIdent.PlayerID
			}

			response, err := handler.Handle(ctx, &player.GetPlayerCommand{
				PlayerID:    playerIDPtr,
				AgentSymbol: playerIdent.AgentSymbol,
			})
			if err != nil {
				return fmt.Errorf("failed to get player: %w", err)
			}

			result := response.(*player.GetPlayerResponse)
			p := result.Player

			// Display player info
			fmt.Printf("Player Information\n")
			fmt.Printf("==================\n\n")
			fmt.Printf("Player ID:     %d\n", p.ID)
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

			fmt.Printf("\nToken: %s...\n", p.Token[:20])

			return nil
		},
	}

	return cmd
}
