package cli

import (
	"context"
	"fmt"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// ModuleInfoDTO is a single ship module in a CLI response.
type ModuleInfoDTO struct {
	Symbol   string
	Name     string
	Capacity int
	Range    int
	// Power, Crew, and Slots are the module's own install requirements
	// (sp-el60) - what installing it draws from the ship's reactor power
	// budget, crew capacity, and module-slot budget respectively.
	Power int
	Crew  int
	Slots int
}

// ModuleModificationResponse is the CLI-side result of an install or remove.
type ModuleModificationResponse struct {
	Success       bool
	ShipSymbol    string
	ModuleSymbol  string
	CargoCapacity int
	Fee           int
	Modules       []ModuleInfoDTO
	Message       string
	Error         string
}

// ModuleFeasibilityDTO is the CLI-side offline install-feasibility verdict
// for a candidate module (sp-el60), populated only when the list request
// carried a candidate symbol.
//
// RequirementsKnown is true only when the candidate's own power/crew/slot
// requirements were actually resolved server-side - from another ship in
// the fleet that has the symbol installed, since there is no catalog of
// unowned module specs anywhere (sp-el60 acceptance fix). When false,
// RequirementsPower/Crew/Slots are unset/zero and CanInstall is always
// false; callers must present the requirements as "unknown", never as a
// real zero-cost spec, and must never report CAN-INSTALL.
type ModuleFeasibilityDTO struct {
	CandidateSymbol string
	CanInstall      bool
	PowerShort      int
	SlotShort       int
	CrewShort       int

	RequirementsKnown bool
	RequirementsPower int
	RequirementsCrew  int
	RequirementsSlots int
}

// ShipModulesResponse is the CLI-side result of listing a ship's modules,
// plus its power/slot/crew budget summary (sp-el60) computed offline from
// the DB-cached ship state.
type ShipModulesResponse struct {
	ShipSymbol string
	Modules    []ModuleInfoDTO
	Error      string

	ReactorPowerOutput int
	PowerUsed          int
	ModuleSlots        int
	ModuleSlotsUsed    int
	MountingPoints     int
	MountingPointsUsed int
	CrewCurrent        int
	CrewRequired       int
	CrewCapacity       int

	// Feasibility is populated only when the caller supplied a candidate symbol.
	Feasibility *ModuleFeasibilityDTO
}

func protoToModuleDTOs(modules []*pb.ShipModuleInfo) []ModuleInfoDTO {
	out := make([]ModuleInfoDTO, 0, len(modules))
	for _, m := range modules {
		out = append(out, ModuleInfoDTO{
			Symbol:   m.Symbol,
			Name:     m.Name,
			Capacity: int(m.Capacity),
			Range:    int(m.Range),
			Power:    int(m.Power),
			Crew:     int(m.Crew),
			Slots:    int(m.Slots),
		})
	}
	return out
}

// InstallModule installs a module (which must be in the ship's cargo) onto the ship.
func (c *DaemonClient) InstallModule(
	ctx context.Context,
	shipSymbol string,
	moduleSymbol string,
	playerID int,
	agentSymbol string,
) (*ModuleModificationResponse, error) {
	req := &pb.InstallModuleRequest{
		ShipSymbol:   shipSymbol,
		ModuleSymbol: moduleSymbol,
		PlayerId:     int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.InstallModule(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return &ModuleModificationResponse{
		Success:       resp.Success,
		ShipSymbol:    resp.ShipSymbol,
		ModuleSymbol:  resp.ModuleSymbol,
		CargoCapacity: int(resp.CargoCapacity),
		Fee:           int(resp.Fee),
		Modules:       protoToModuleDTOs(resp.Modules),
		Message:       resp.Message,
		Error:         resp.Error,
	}, nil
}

// RemoveModule removes an installed module from the ship back into its cargo.
func (c *DaemonClient) RemoveModule(
	ctx context.Context,
	shipSymbol string,
	moduleSymbol string,
	playerID int,
	agentSymbol string,
) (*ModuleModificationResponse, error) {
	req := &pb.RemoveModuleRequest{
		ShipSymbol:   shipSymbol,
		ModuleSymbol: moduleSymbol,
		PlayerId:     int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}

	resp, err := c.client.RemoveModule(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	return &ModuleModificationResponse{
		Success:       resp.Success,
		ShipSymbol:    resp.ShipSymbol,
		ModuleSymbol:  resp.ModuleSymbol,
		CargoCapacity: int(resp.CargoCapacity),
		Fee:           int(resp.Fee),
		Modules:       protoToModuleDTOs(resp.Modules),
		Message:       resp.Message,
		Error:         resp.Error,
	}, nil
}

// ListShipModules lists the modules installed on a ship, along with its
// power/slot/crew budget summary computed offline from cached ship state
// (sp-el60). When candidateSymbol is non-empty, the response also carries an
// offline install-feasibility verdict for that not-yet-installed module. The
// candidate's own power/crew/slot requirements are resolved server-side, not
// supplied by the caller (sp-el60 acceptance fix) — there is no catalog of
// unowned module specs anywhere, so the only real data source is another
// ship in the fleet that has the symbol installed. See
// ModuleFeasibilityDTO.RequirementsKnown for the fail-closed signal when no
// ship anywhere ever has.
func (c *DaemonClient) ListShipModules(
	ctx context.Context,
	shipSymbol string,
	playerID int,
	agentSymbol string,
	candidateSymbol string,
) (*ShipModulesResponse, error) {
	req := &pb.ListShipModulesRequest{
		ShipSymbol: shipSymbol,
		PlayerId:   int32(playerID),
	}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	if candidateSymbol != "" {
		req.CandidateSymbol = &candidateSymbol
	}

	resp, err := c.client.ListShipModules(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}

	out := &ShipModulesResponse{
		ShipSymbol: resp.ShipSymbol,
		Modules:    protoToModuleDTOs(resp.Modules),
		Error:      resp.Error,

		ReactorPowerOutput: int(resp.ReactorPowerOutput),
		PowerUsed:          int(resp.PowerUsed),
		ModuleSlots:        int(resp.ModuleSlots),
		ModuleSlotsUsed:    int(resp.ModuleSlotsUsed),
		MountingPoints:     int(resp.MountingPoints),
		MountingPointsUsed: int(resp.MountingPointsUsed),
		CrewCurrent:        int(resp.CrewCurrent),
		CrewRequired:       int(resp.CrewRequired),
		CrewCapacity:       int(resp.CrewCapacity),
	}

	if f := resp.Feasibility; f != nil {
		out.Feasibility = &ModuleFeasibilityDTO{
			CandidateSymbol:   f.CandidateSymbol,
			CanInstall:        f.CanInstall,
			PowerShort:        int(f.PowerShort),
			SlotShort:         int(f.SlotShort),
			CrewShort:         int(f.CrewShort),
			RequirementsKnown: f.RequirementsKnown,
			RequirementsPower: int(f.RequirementsPower),
			RequirementsCrew:  int(f.RequirementsCrew),
			RequirementsSlots: int(f.RequirementsSlots),
		}
	}

	return out, nil
}
