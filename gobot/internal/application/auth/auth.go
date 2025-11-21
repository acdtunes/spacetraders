package auth

import (
	"context"
	"fmt"
	"reflect"

	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Context keys for passing authentication data through context
type authContextKey int

const (
	playerTokenKey authContextKey = iota + 1000 // Offset from logger keys
)

// WithPlayerToken injects a player authentication token into the context
func WithPlayerToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, playerTokenKey, token)
}

// PlayerTokenFromContext extracts the player authentication token from context
// Returns an error if the token is not found in the context
func PlayerTokenFromContext(ctx context.Context) (string, error) {
	token, ok := ctx.Value(playerTokenKey).(string)
	if !ok || token == "" {
		return "", fmt.Errorf("player token not found in context")
	}
	return token, nil
}

// PlayerTokenMiddleware creates middleware that injects player tokens into context
// It extracts player identification (PlayerID or AgentSymbol) from the request,
// fetches the player from the repository, and injects the token into context
func PlayerTokenMiddleware(playerRepo player.PlayerRepository) mediator.Middleware {
	return func(ctx context.Context, request mediator.Request, next mediator.HandlerFunc) (mediator.Response, error) {
		// Extract player identifier from request using reflection
		playerID, agentSymbol := extractPlayerIdentifier(request)

		// Fetch player if identifier found
		var playerEntity *player.Player
		var err error

		if !playerID.IsZero() {
			playerEntity, err = playerRepo.FindByID(ctx, playerID)
			if err != nil {
				return nil, fmt.Errorf("failed to find player by ID %s: %w", playerID.String(), err)
			}
		} else if agentSymbol != "" {
			playerEntity, err = playerRepo.FindByAgentSymbol(ctx, agentSymbol)
			if err != nil {
				return nil, fmt.Errorf("failed to find player by agent symbol %s: %w", agentSymbol, err)
			}
		}

		// Inject token into context if player found
		if playerEntity != nil {
			ctx = WithPlayerToken(ctx, playerEntity.Token)
		}

		// Continue to next middleware or handler
		return next(ctx, request)
	}
}

// extractPlayerIdentifier uses reflection to extract player identification from request
// Returns (playerID, agentSymbol) - one or both may be set
func extractPlayerIdentifier(request mediator.Request) (shared.PlayerID, string) {
	var playerID shared.PlayerID
	var agentSymbol string

	requestValue := reflect.ValueOf(request)
	if requestValue.Kind() == reflect.Ptr {
		requestValue = requestValue.Elem()
	}

	if requestValue.Kind() != reflect.Struct {
		return shared.PlayerID{}, ""
	}

	requestType := requestValue.Type()

	// Check for PlayerID field (shared.PlayerID type)
	if field, found := requestType.FieldByName("PlayerID"); found {
		fieldValue := requestValue.FieldByName("PlayerID")

		// Check if it's the shared.PlayerID type
		if field.Type.String() == "shared.PlayerID" {
			playerID = fieldValue.Interface().(shared.PlayerID)
		} else if field.Type.Kind() == reflect.Int {
			// Legacy fallback for int type
			if intVal := int(fieldValue.Int()); intVal > 0 {
				playerID, _ = shared.NewPlayerID(intVal)
			}
		} else if field.Type.Kind() == reflect.Uint {
			// Legacy fallback for uint type
			if uintVal := int(fieldValue.Uint()); uintVal > 0 {
				playerID, _ = shared.NewPlayerID(uintVal)
			}
		}
	}

	// Check for AgentSymbol field
	if _, found := requestType.FieldByName("AgentSymbol"); found {
		fieldValue := requestValue.FieldByName("AgentSymbol")
		if fieldValue.Kind() == reflect.String {
			agentSymbol = fieldValue.String()
		}
	}

	return playerID, agentSymbol
}
