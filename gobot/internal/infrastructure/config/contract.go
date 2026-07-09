package config

// ContractConfig configures contract-economics tunables for the contract
// workflow (cmd/spacetraders-daemon). sp-snmb.
type ContractConfig struct {
	// ValueFloor is the minimum total contract payout (OnAccepted +
	// OnFulfilled, in credits - see domain/contract.Contract.TotalPayout)
	// required before RunWorkflowHandler will accept a negotiated contract.
	// A full negotiate/accept/deliver/fulfill cycle burns roughly the same
	// ship-hours regardless of payout size, so a contract below this floor
	// is deliberately left unaccepted (and therefore, since Contract.Fulfill
	// hard-requires Accepted()==true, can never be fulfilled) rather than
	// consuming a cycle on a disproportionately small payout. Tune via
	// ST_CONTRACT_VALUE_FLOOR or contract.value_floor in config.yaml.
	ValueFloor int `mapstructure:"value_floor" validate:"omitempty,min=0"`
}
