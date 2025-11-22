package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// NewContractCommand creates the contract command with subcommands
func NewContractCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "contract",
		Short: "Manage contract operations",
		Long: `Manage contract operations with automatic fleet coordination.

Contract commands allow you to automate contract execution using all available idle light hauler ships.

Examples:
  spacetraders contract start
  spacetraders container list
  spacetraders container stop <container-id>`,
	}

	// Add subcommands
	cmd.AddCommand(newContractStartCommand())

	return cmd
}

// newContractStartCommand creates the contract start subcommand
func newContractStartCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start contract fleet coordinator",
		Long: `Start a contract fleet coordinator that uses all available idle light hauler ships for continuous contract execution.

The coordinator will:
- Dynamically discover all idle light hauler ships
- Negotiate contracts continuously
- Assign each contract to the ship closest to the purchase market
- Balance ship positions after contract delivery if ship selection changes
- Execute contracts in sequence (one contract at a time)
- Run until stopped

Ships are selected dynamically from the pool of idle haulers. No pre-assignment needed.

Examples:
  spacetraders contract start --player-id 1
  spacetraders contract start --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {

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

			result, err := client.ContractFleetCoordinator(ctx, nil, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("contract fleet coordinator failed: %w", err)
			}

			// Display result
			fmt.Println("✓ Contract fleet coordinator started successfully")
			fmt.Printf("  Container ID:     %s\n", result.ContainerID)
			fmt.Printf("  Agent:            %s (player %d)\n", playerIdent.AgentSymbol, playerIdent.PlayerID)
			fmt.Println("\n  The coordinator will use all available idle light hauler ships.")
			fmt.Println("  Ships are selected dynamically for each contract.")
			fmt.Println("  The coordinator will continuously negotiate and execute contracts.")
			fmt.Println("  Use 'spacetraders container stop " + result.ContainerID + "' to stop the coordinator.")

			return nil
		},
	}

	return cmd
}
