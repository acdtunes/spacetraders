package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// NewContractCommand creates the contract command with subcommands
func NewContractCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "contract",
		Short: "Manage contract operations",
		Long: `Manage contract operations including multi-ship fleet coordination.

Contract commands allow you to automate contract execution across one or more ships.

Examples:
  spacetraders contract start --ships SHIP-A,SHIP-B
  spacetraders container list
  spacetraders container stop <container-id>`,
	}

	// Add subcommands
	cmd.AddCommand(newContractStartCommand())

	return cmd
}

// newContractStartCommand creates the contract start subcommand
func newContractStartCommand() *cobra.Command {
	var (
		shipSymbols string
	)

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start contract fleet coordinator",
		Long: `Start a contract fleet coordinator that manages a pool of ships for continuous contract execution.

The coordinator will:
- Lock all specified ships to the contract pool
- Negotiate contracts continuously
- Assign each contract to the ship closest to the purchase market
- Execute contracts in sequence (one contract at a time)
- Run until stopped

Ships in the pool cannot be used for other operations until the coordinator is stopped.

Examples:
  spacetraders contract start --ships SHIP-A,SHIP-B --player-id 1
  spacetraders contract start --ships CARGO-1,CARGO-2 --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate flags
			if shipSymbols == "" {
				return fmt.Errorf("--ships flag is required (comma-separated list)")
			}

			// Parse ship symbols
			ships := strings.Split(shipSymbols, ",")
			if len(ships) == 0 {
				return fmt.Errorf("at least one ship symbol is required")
			}

			// Trim whitespace from ship symbols
			for i, ship := range ships {
				ships[i] = strings.TrimSpace(ship)
			}

			// Resolve player from flags or defaults
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}

			// Create gRPC client
			client, err := NewDaemonClient(socketPath)
			if err != nil {
				return fmt.Errorf("failed to connect to daemon: %w", err)
			}
			defer client.Close()

			// Execute contract fleet coordinator command
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			result, err := client.ContractFleetCoordinator(ctx, ships, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("contract fleet coordinator failed: %w", err)
			}

			// Display result
			fmt.Println("✓ Contract fleet coordinator started successfully")
			fmt.Printf("  Container ID:     %s\n", result.ContainerID)
			fmt.Printf("  Ship Pool:        %s\n", strings.Join(ships, ", "))
			fmt.Printf("  Agent:            %s (player %d)\n", playerIdent.AgentSymbol, playerIdent.PlayerID)
			fmt.Println("\n  All ships in the pool are now locked for contract operations.")
			fmt.Println("  The coordinator will continuously negotiate and execute contracts.")
			fmt.Println("  Use 'spacetraders container stop " + result.ContainerID + "' to stop the coordinator.")

			return nil
		},
	}

	// Add flags
	cmd.Flags().StringVarP(&shipSymbols, "ships", "s", "", "Comma-separated list of ship symbols for the contract pool (required)")
	cmd.MarkFlagRequired("ships")

	return cmd
}
