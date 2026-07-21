package commands

// ContractScalerTunableDefaults returns the contract auto-scaler's live-tunable knob → documented-
// default map — the SINGLE source the daemon's tune bounds-registry reads for the ceiling's default, so
// a registry default can never drift from the coordinator's own. The scaler exposes exactly ONE
// operator lever: contract_fleet_max_hulls (the ceiling). The sole money guard — the 200000 cushion —
// is a compile-time const, deliberately NOT tunable.
func ContractScalerTunableDefaults() map[string]int {
	return map[string]int{
		ceilingKey: DefaultContractFleetMaxHulls,
	}
}
