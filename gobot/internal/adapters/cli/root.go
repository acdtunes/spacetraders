package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	// Global flags
	socketPath string
	playerID   int
	agentSymbol string
	verbose    bool
)

// NewRootCommand creates the root command for the CLI
func NewRootCommand() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "spacetraders",
		Short: "SpaceTraders CLI - Interact with the SpaceTraders daemon",
		Long: `SpaceTraders CLI provides commands to interact with your SpaceTraders fleet.
The CLI communicates with the daemon via Unix socket for efficient operation.

Examples:
  spacetraders navigate --ship AGENT-1 --destination X1-GZ7-B1
  spacetraders dock --ship AGENT-1
  spacetraders container list
  spacetraders container logs <container-id>`,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			// Global setup (if needed)
		},
	}

	// Global flags
	rootCmd.PersistentFlags().StringVar(&socketPath, "socket", getDefaultSocketPath(),
		"Path to daemon Unix socket")
	rootCmd.PersistentFlags().IntVar(&playerID, "player-id", 0,
		"Player ID (required if agent not specified)")
	rootCmd.PersistentFlags().StringVar(&agentSymbol, "agent", "",
		"Agent symbol (alternative to player-id)")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false,
		"Enable verbose output")

	// Add subcommands
	rootCmd.AddCommand(NewNavigateCommand())
	rootCmd.AddCommand(NewDockCommand())
	rootCmd.AddCommand(NewOrbitCommand())
	rootCmd.AddCommand(NewRefuelCommand())
	rootCmd.AddCommand(NewContainerCommand())
	rootCmd.AddCommand(NewHealthCommand())
	rootCmd.AddCommand(NewPlayerCommand())
	rootCmd.AddCommand(NewShipCommand())
	rootCmd.AddCommand(NewConfigCommand())

	return rootCmd
}

// getDefaultSocketPath returns the default socket path
func getDefaultSocketPath() string {
	if path := os.Getenv("SPACETRADERS_SOCKET"); path != "" {
		return path
	}
	return "/tmp/spacetraders-daemon.sock"
}

// Execute runs the root command
func Execute() {
	rootCmd := NewRootCommand()
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
