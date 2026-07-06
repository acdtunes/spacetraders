package tactics

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

type stateTransition struct {
	ensure        func(*navigation.Ship) (bool, error)
	callAPI       func(context.Context, *navigation.Ship, shared.PlayerID) error
	doneStatus    string
	alreadyStatus string
}

func runStateTransition(ctx context.Context, shipRepo navigation.ShipRepository, cmd types.ShipCommand, transition stateTransition) (string, error) {
	ship, err := types.LoadShip(ctx, shipRepo, cmd)
	if err != nil {
		return "", err
	}

	stateChanged, err := transition.ensure(ship)
	if err != nil {
		return "", err
	}

	if !stateChanged {
		return transition.alreadyStatus, nil
	}

	if err := transition.callAPI(ctx, ship, cmd.GetPlayerID()); err != nil {
		return "", err
	}

	return transition.doneStatus, nil
}
