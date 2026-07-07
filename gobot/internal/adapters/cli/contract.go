package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// contractStore is the subset of contract persistence the CLI needs for
// read-only observability. Implemented directly against the contracts
// table since the domain repository only exposes active-contract queries.
type contractStore interface {
	ListContracts(ctx context.Context, playerID int) ([]persistence.ContractModel, error)
	GetContract(ctx context.Context, id string) (*persistence.ContractModel, error)
}

// gormContractStore reads contract rows directly via GORM (read-only, no API calls).
type gormContractStore struct {
	db *gorm.DB
}

func newContractStore() (contractStore, error) {
	store, _, err := newContractStoreAndPlayerRepo()
	return store, err
}

// newContractStoreAndPlayerRepo builds the contract store together with a player
// repository backed by the same database connection, so `contract list` can resolve
// the default player without opening a second connection.
func newContractStoreAndPlayerRepo() (contractStore, player.PlayerRepository, error) {
	cfg, err := config.LoadConfig("")
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load config: %w", err)
	}

	db, err := database.NewConnection(&cfg.Database)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	return &gormContractStore{db: db}, persistence.NewGormPlayerRepository(db), nil
}

func (s *gormContractStore) ListContracts(ctx context.Context, playerID int) ([]persistence.ContractModel, error) {
	var models []persistence.ContractModel
	result := s.db.WithContext(ctx).Where("player_id = ?", playerID).Find(&models)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to list contracts: %w", result.Error)
	}
	return models, nil
}

func (s *gormContractStore) GetContract(ctx context.Context, id string) (*persistence.ContractModel, error) {
	var model persistence.ContractModel
	result := s.db.WithContext(ctx).Where("id = ?", id).First(&model)
	if result.Error != nil {
		return nil, fmt.Errorf("contract not found: %s", id)
	}
	return &model, nil
}

// marshalDeliveries serializes deliveries the same way the contract
// repository persists them, for use by tests building fixtures.
func marshalDeliveries(deliveries []contract.Delivery) (string, error) {
	if deliveries == nil {
		deliveries = []contract.Delivery{}
	}
	data, err := json.Marshal(deliveries)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// contractRow is one line of `contract list` output.
type contractRow struct {
	ID            string        `json:"id"`
	ShortID       string        `json:"short_id"`
	Type          string        `json:"type"`
	Faction       string        `json:"faction"`
	Accepted      bool          `json:"accepted"`
	Fulfilled     bool          `json:"fulfilled"`
	Deadline      string        `json:"deadline"`
	TimeRemaining time.Duration `json:"time_remaining_ns"`
	Overdue       bool          `json:"overdue"`
	TotalPayment  int           `json:"total_payment"`
}

// contractDeliveryDetail is per-delivery progress for `contract get`.
type contractDeliveryDetail struct {
	TradeSymbol       string `json:"trade_symbol"`
	DestinationSymbol string `json:"destination_symbol"`
	UnitsRequired     int    `json:"units_required"`
	UnitsFulfilled    int    `json:"units_fulfilled"`
}

// contractDetail is the full `contract get` output.
type contractDetail struct {
	ID                 string                   `json:"id"`
	Type               string                   `json:"type"`
	Faction            string                   `json:"faction"`
	Accepted           bool                     `json:"accepted"`
	Fulfilled          bool                     `json:"fulfilled"`
	DeadlineToAccept   string                   `json:"deadline_to_accept"`
	Deadline           string                   `json:"deadline"`
	TimeRemaining      time.Duration            `json:"time_remaining_ns"`
	Overdue            bool                     `json:"overdue"`
	PaymentOnAccepted  int                      `json:"payment_on_accepted"`
	PaymentOnFulfilled int                      `json:"payment_on_fulfilled"`
	Deliveries         []contractDeliveryDetail `json:"deliveries"`
}

func shortContractID(id string) string {
	const shortLen = 9
	if len(id) <= shortLen {
		return id
	}
	return id[:shortLen]
}

func deadlineRemaining(deadline string) (time.Duration, bool) {
	parsed, err := time.Parse(time.RFC3339, deadline)
	if err != nil {
		return 0, false
	}
	remaining := time.Until(parsed)
	return remaining, remaining <= 0
}

func unmarshalDeliveries(raw string) ([]contract.Delivery, error) {
	var deliveries []contract.Delivery
	if raw == "" {
		return deliveries, nil
	}
	if err := json.Unmarshal([]byte(raw), &deliveries); err != nil {
		return nil, fmt.Errorf("failed to unmarshal deliveries: %w", err)
	}
	return deliveries, nil
}

// listContractRows builds the `contract list` rows from stored contracts.
func listContractRows(ctx context.Context, store contractStore, playerID int) ([]contractRow, error) {
	models, err := store.ListContracts(ctx, playerID)
	if err != nil {
		return nil, err
	}

	rows := make([]contractRow, 0, len(models))
	for _, m := range models {
		remaining, overdue := deadlineRemaining(m.Deadline)
		rows = append(rows, contractRow{
			ID:            m.ID,
			ShortID:       shortContractID(m.ID),
			Type:          m.Type,
			Faction:       m.FactionSymbol,
			Accepted:      m.Accepted,
			Fulfilled:     m.Fulfilled,
			Deadline:      m.Deadline,
			TimeRemaining: remaining,
			Overdue:       overdue,
			TotalPayment:  m.PaymentOnAccepted + m.PaymentOnFulfilled,
		})
	}
	return rows, nil
}

// getContractDetail builds the `contract get` detail for one contract.
func getContractDetail(ctx context.Context, store contractStore, id string) (*contractDetail, error) {
	model, err := store.GetContract(ctx, id)
	if err != nil {
		return nil, err
	}

	deliveries, err := unmarshalDeliveries(model.DeliveriesJSON)
	if err != nil {
		return nil, err
	}

	details := make([]contractDeliveryDetail, 0, len(deliveries))
	for _, d := range deliveries {
		details = append(details, contractDeliveryDetail{
			TradeSymbol:       d.TradeSymbol,
			DestinationSymbol: d.DestinationSymbol,
			UnitsRequired:     d.UnitsRequired,
			UnitsFulfilled:    d.UnitsFulfilled,
		})
	}

	remaining, overdue := deadlineRemaining(model.Deadline)

	return &contractDetail{
		ID:                 model.ID,
		Type:               model.Type,
		Faction:            model.FactionSymbol,
		Accepted:           model.Accepted,
		Fulfilled:          model.Fulfilled,
		DeadlineToAccept:   model.DeadlineToAccept,
		Deadline:           model.Deadline,
		TimeRemaining:      remaining,
		Overdue:            overdue,
		PaymentOnAccepted:  model.PaymentOnAccepted,
		PaymentOnFulfilled: model.PaymentOnFulfilled,
		Deliveries:         details,
	}, nil
}

func formatRemaining(d time.Duration, overdue bool) string {
	if overdue {
		return "OVERDUE"
	}
	if d < 0 {
		d = 0
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	if days > 0 {
		return fmt.Sprintf("%dd%dh", days, hours)
	}
	minutes := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh%dm", hours, minutes)
}

// runContractList prints contract rows for a player, as a table or JSON.
func runContractList(ctx context.Context, store contractStore, playerID int, jsonOut bool) error {
	rows, err := listContractRows(ctx, store, playerID)
	if err != nil {
		return fmt.Errorf("failed to list contracts: %w", err)
	}

	if jsonOut {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(rows)
	}

	if len(rows) == 0 {
		fmt.Println("No contracts.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tTYPE\tFACTION\tACCEPTED\tFULFILLED\tDEADLINE\tREMAINING\tTOTAL_PAYMENT")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%t\t%t\t%s\t%s\t%d\n",
			r.ShortID, r.Type, r.Faction, r.Accepted, r.Fulfilled, r.Deadline,
			formatRemaining(r.TimeRemaining, r.Overdue), r.TotalPayment)
	}
	return w.Flush()
}

// runContractGet prints the full detail of one contract, as text or JSON.
func runContractGet(ctx context.Context, store contractStore, id string, jsonOut bool) error {
	detail, err := getContractDetail(ctx, store, id)
	if err != nil {
		return fmt.Errorf("failed to get contract: %w", err)
	}

	if jsonOut {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(detail)
	}

	fmt.Printf("Contract:          %s\n", detail.ID)
	fmt.Printf("Type:              %s\n", detail.Type)
	fmt.Printf("Faction:           %s\n", detail.Faction)
	fmt.Printf("Accepted:          %t\n", detail.Accepted)
	fmt.Printf("Fulfilled:         %t\n", detail.Fulfilled)
	fmt.Printf("Deadline to accept: %s\n", detail.DeadlineToAccept)
	fmt.Printf("Deadline:          %s\n", detail.Deadline)
	fmt.Printf("Time remaining:    %s\n", formatRemaining(detail.TimeRemaining, detail.Overdue))
	fmt.Printf("Payment on accept: %d\n", detail.PaymentOnAccepted)
	fmt.Printf("Payment on fulfill: %d\n", detail.PaymentOnFulfilled)

	if len(detail.Deliveries) == 0 {
		fmt.Println("Deliveries:        none")
		return nil
	}

	fmt.Println("Deliveries:")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  GOOD\tDESTINATION\tREQUIRED\tFULFILLED")
	for _, d := range detail.Deliveries {
		fmt.Fprintf(w, "  %s\t%s\t%d\t%d\n", d.TradeSymbol, d.DestinationSymbol, d.UnitsRequired, d.UnitsFulfilled)
	}
	return w.Flush()
}

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
	cmd.AddCommand(newContractListCommand())
	cmd.AddCommand(newContractGetCommand())

	return cmd
}

// newContractListCommand creates the contract list subcommand
func newContractListCommand() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List contracts for a player",
		Long: `List contracts for a player, one row per contract, including deadline
and time remaining - the decision-critical column for evaluating whether a
contract is still worth pursuing.

Examples:
  spacetraders contract list --player-id 1
  spacetraders contract list --player-id 1 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, playerRepo, err := newContractStoreAndPlayerRepo()
			if err != nil {
				return err
			}

			// Resolve the effective player (flags > persisted default) so `contract list`
			// honors the default set via `config set-player`.
			ctx := context.Background()
			resolved, err := resolveDefaultPlayer(ctx, playerRepo)
			if err != nil {
				return err
			}

			return runContractList(ctx, store, resolved.ID.Value(), jsonOut)
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")

	return cmd
}

// newContractGetCommand creates the contract get subcommand
func newContractGetCommand() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "get <id>",
		Short: "Show full detail for a contract",
		Long: `Show full detail for one contract, including per-delivery progress
(good, units required, units fulfilled) and both payment components.

Examples:
  spacetraders contract get contract-abc123 --player-id 1
  spacetraders contract get contract-abc123 --player-id 1 --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := newContractStore()
			if err != nil {
				return err
			}

			return runContractGet(context.Background(), store, args[0], jsonOut)
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")

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
			client, err := connectDaemon()
			if err != nil {
				return err
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
