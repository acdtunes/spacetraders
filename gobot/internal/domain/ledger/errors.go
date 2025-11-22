package ledger

import "fmt"

// ErrInvalidTransaction represents validation errors for transactions
type ErrInvalidTransaction struct {
	Field  string
	Reason string
}

func (e *ErrInvalidTransaction) Error() string {
	return fmt.Sprintf("invalid transaction: %s - %s", e.Field, e.Reason)
}

// ErrBalanceInvariantViolation represents errors when balance calculations don't match
type ErrBalanceInvariantViolation struct {
	BalanceBefore int
	Amount        int
	BalanceAfter  int
	Expected      int
}

func (e *ErrBalanceInvariantViolation) Error() string {
	return fmt.Sprintf("balance invariant violated: balance_before=%d + amount=%d should equal balance_after=%d, but got %d",
		e.BalanceBefore, e.Amount, e.Expected, e.BalanceAfter)
}

// ErrTransactionNotFound represents errors when a transaction cannot be found
type ErrTransactionNotFound struct {
	ID       string
	PlayerID int
}

func (e *ErrTransactionNotFound) Error() string {
	return fmt.Sprintf("transaction not found: id=%s, player_id=%d", e.ID, e.PlayerID)
}
