package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// NewConfigCommand creates the config command with subcommands
func NewConfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage configuration settings",
		Long: `Manage SpaceTraders configuration settings.

Configuration is loaded from multiple sources with priority:
1. Environment variables (ST_* prefix)
2. Config file (config.yaml)
3. Default values

User preferences (default player) are stored in ~/.spacetraders/config.json

Examples:
  spacetraders config show
  spacetraders config set-player --agent ENDURANCE
  spacetraders config set-player --player-id 1
  spacetraders config clear-player`,
	}

	// Add subcommands
	cmd.AddCommand(newConfigShowCommand())
	cmd.AddCommand(newConfigSetPlayerCommand())
	cmd.AddCommand(newConfigClearPlayerCommand())

	return cmd
}

// newConfigShowCommand creates the config show subcommand
func newConfigShowCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show current configuration",
		Long: `Display the current configuration settings.

Shows both system configuration and user preferences.

Example:
  spacetraders config show`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load system config
			cfg, err := config.LoadConfig("")
			if err != nil {
				fmt.Printf("Warning: Failed to load config: %v\n", err)
				fmt.Println("Using default configuration.")
				cfg = config.LoadConfigOrDefault("")
			}

			// Load user config
			userConfigHandler, err := config.NewUserConfigHandler()
			if err != nil {
				return fmt.Errorf("failed to create user config handler: %w", err)
			}

			userCfg, err := userConfigHandler.Load()
			if err != nil {
				fmt.Printf("Warning: Failed to load user config: %v\n\n", err)
				userCfg = &config.UserConfig{}
			}

			// Display configuration
			fmt.Println("SpaceTraders Configuration")
			fmt.Println("==========================")

			fmt.Println("User Preferences:")
			fmt.Printf("  Config file:      %s\n", userConfigHandler.GetConfigPath())
			if userCfg.DefaultPlayerID != nil {
				fmt.Printf("  Default Player:   ID=%d\n", *userCfg.DefaultPlayerID)
			} else if userCfg.DefaultAgent != "" {
				fmt.Printf("  Default Player:   Agent=%s\n", userCfg.DefaultAgent)
			} else {
				fmt.Printf("  Default Player:   (not set)\n")
			}

			fmt.Println("\nDatabase:")
			fmt.Printf("  Type:             %s\n", cfg.Database.Type)
			if cfg.Database.URL != "" {
				fmt.Printf("  URL:              %s\n", maskPassword(cfg.Database.URL))
			} else {
				fmt.Printf("  Host:             %s\n", cfg.Database.Host)
				fmt.Printf("  Port:             %d\n", cfg.Database.Port)
				fmt.Printf("  Database:         %s\n", cfg.Database.Name)
				fmt.Printf("  User:             %s\n", cfg.Database.User)
			}
			fmt.Printf("  Max Connections:  %d\n", cfg.Database.Pool.MaxOpen)

			fmt.Println("\nSpaceTraders API:")
			fmt.Printf("  Base URL:         %s\n", cfg.API.BaseURL)
			fmt.Printf("  Timeout:          %s\n", cfg.API.Timeout)
			fmt.Printf("  Rate Limit:       %d req/s (burst: %d)\n",
				cfg.API.RateLimit.Requests, cfg.API.RateLimit.Burst)
			fmt.Printf("  Max Retries:      %d\n", cfg.API.Retry.MaxAttempts)

			fmt.Println("\nRouting Service:")
			fmt.Printf("  Address:          %s\n", cfg.Routing.Address)
			fmt.Printf("  TSP Timeout:      %s\n", cfg.Routing.Timeout.TSP)
			fmt.Printf("  VRP Timeout:      %s\n", cfg.Routing.Timeout.VRP)

			fmt.Println("\nDaemon:")
			fmt.Printf("  Address:          %s\n", cfg.Daemon.Address)
			fmt.Printf("  Socket Path:      %s\n", cfg.Daemon.SocketPath)
			fmt.Printf("  Max Containers:   %d\n", cfg.Daemon.MaxContainers)
			fmt.Printf("  Health Interval:  %s\n", cfg.Daemon.HealthCheckInterval)

			fmt.Println("\nLogging:")
			fmt.Printf("  Level:            %s\n", cfg.Logging.Level)
			fmt.Printf("  Format:           %s\n", cfg.Logging.Format)
			fmt.Printf("  Output:           %s\n", cfg.Logging.Output)

			return nil
		},
	}

	return cmd
}

// newConfigSetPlayerCommand creates the config set-player subcommand
func newConfigSetPlayerCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set-player",
		Short: "Set default player",
		Long: `Set the default player to use for commands.

Specify the player using either --player-id or --agent flag.
The default player will be used when no player is specified in commands.

Examples:
  spacetraders config set-player --player-id 1
  spacetraders config set-player --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if playerID == 0 && agentSymbol == "" {
				return fmt.Errorf("either --player-id or --agent flag is required")
			}

			// Create user config handler
			userConfigHandler, err := config.NewUserConfigHandler()
			if err != nil {
				return fmt.Errorf("failed to create user config handler: %w", err)
			}

			// Verify player exists in database
			cfg, err := config.LoadConfig("")
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			db, err := database.NewConnection(&cfg.Database)
			if err != nil {
				return fmt.Errorf("failed to connect to database: %w", err)
			}

			playerRepo := persistence.NewGormPlayerRepository(db)
			ctx := context.Background()

			var verifiedPlayer *persistence.PlayerModel
			if playerID > 0 {
				// Verify by player ID
				var model persistence.PlayerModel
				result := db.Where("player_id = ?", playerID).First(&model)
				if result.Error != nil {
					return fmt.Errorf("player with ID %d not found", playerID)
				}
				verifiedPlayer = &model

				// Set by player ID
				if err := userConfigHandler.SetDefaultPlayer(playerID); err != nil {
					return fmt.Errorf("failed to set default player: %w", err)
				}
			} else {
				// Verify by agent symbol
				player, err := playerRepo.FindByAgentSymbol(ctx, agentSymbol)
				if err != nil {
					return fmt.Errorf("player with agent '%s' not found", agentSymbol)
				}

				// Set by agent symbol
				if err := userConfigHandler.SetDefaultAgent(agentSymbol); err != nil {
					return fmt.Errorf("failed to set default agent: %w", err)
				}

				// Also set player ID for convenience
				if err := userConfigHandler.SetDefaultPlayer(player.ID); err != nil {
					return fmt.Errorf("failed to set default player ID: %w", err)
				}

				verifiedPlayer = &persistence.PlayerModel{
					PlayerID:    player.ID,
					AgentSymbol: player.AgentSymbol,
				}
			}

			fmt.Println("✓ Default player set successfully")
			fmt.Printf("  Player ID:    %d\n", verifiedPlayer.PlayerID)
			fmt.Printf("  Agent Symbol: %s\n", verifiedPlayer.AgentSymbol)
			fmt.Printf("\nCommands will now use this player by default.\n")
			fmt.Printf("Override with --player-id or --agent flags.\n")

			return nil
		},
	}

	return cmd
}

// newConfigClearPlayerCommand creates the config clear-player subcommand
func newConfigClearPlayerCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clear-player",
		Short: "Clear default player setting",
		Long: `Remove the default player setting.

After clearing, you must explicitly specify --player-id or --agent
for all commands that require player context.

Example:
  spacetraders config clear-player`,
		RunE: func(cmd *cobra.Command, args []string) error {
			userConfigHandler, err := config.NewUserConfigHandler()
			if err != nil {
				return fmt.Errorf("failed to create user config handler: %w", err)
			}

			if err := userConfigHandler.ClearDefaultPlayer(); err != nil {
				return fmt.Errorf("failed to clear default player: %w", err)
			}

			fmt.Println("✓ Default player cleared")
			fmt.Println("\nYou must now specify --player-id or --agent for all commands.")

			return nil
		},
	}

	return cmd
}

// maskPassword masks passwords in connection strings for display
func maskPassword(url string) string {
	// Simple masking - could be improved
	return url // TODO: Implement proper password masking
}

// prettyPrint formats JSON for display
func prettyPrint(v interface{}) string {
	bytes, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(bytes)
}
