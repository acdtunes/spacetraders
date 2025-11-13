package graph

import (
	"context"
	"fmt"
	"log"

	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// SystemGraphProvider provides system navigation graphs with database caching
//
// Checks database cache first, falls back to building from API if needed.
// Stores newly built graphs in database for future use.
type SystemGraphProvider struct {
	graphRepo    system.SystemGraphRepository
	graphBuilder system.IGraphBuilder
	playerID     int
}

// NewSystemGraphProvider creates a new system graph provider
func NewSystemGraphProvider(
	graphRepo system.SystemGraphRepository,
	graphBuilder system.IGraphBuilder,
	playerID int,
) system.ISystemGraphProvider {
	return &SystemGraphProvider{
		graphRepo:    graphRepo,
		graphBuilder: graphBuilder,
		playerID:     playerID,
	}
}

// GetGraph retrieves system navigation graph (checks cache first, builds from API if needed)
func (p *SystemGraphProvider) GetGraph(ctx context.Context, systemSymbol string, forceRefresh bool) (*system.GraphLoadResult, error) {
	// Try loading from database cache first (unless force refresh)
	if !forceRefresh {
		graph, err := p.loadFromDatabase(ctx, systemSymbol)
		if err != nil {
			log.Printf("Error loading graph from database: %v", err)
		} else if graph != nil {
			log.Printf("Loaded graph for %s from database cache", systemSymbol)
			return &system.GraphLoadResult{
				Graph:   graph,
				Source:  "database",
				Message: fmt.Sprintf("Loaded graph for %s from database cache", systemSymbol),
			}, nil
		}
	}

	// Build from API and cache it
	graph, err := p.buildFromAPI(ctx, systemSymbol)
	if err != nil {
		return nil, err
	}

	return &system.GraphLoadResult{
		Graph:   graph,
		Source:  "api",
		Message: fmt.Sprintf("Built graph for %s from API", systemSymbol),
	}, nil
}

// loadFromDatabase loads graph from database cache
func (p *SystemGraphProvider) loadFromDatabase(ctx context.Context, systemSymbol string) (map[string]interface{}, error) {
	graph, err := p.graphRepo.Get(ctx, systemSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to load graph: %w", err)
	}

	if graph != nil {
		log.Printf("Cache hit for %s", systemSymbol)
	} else {
		log.Printf("Cache miss for %s", systemSymbol)
	}

	return graph, nil
}

// buildFromAPI builds graph from API and saves to database
func (p *SystemGraphProvider) buildFromAPI(ctx context.Context, systemSymbol string) (map[string]interface{}, error) {
	log.Printf("Building navigation graph for %s from API", systemSymbol)

	// Build the graph using this player's API client
	graph, err := p.graphBuilder.BuildSystemGraph(ctx, systemSymbol, p.playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to build graph for %s: %w", systemSymbol, err)
	}

	// Save to database cache
	if err := p.saveToDatabase(ctx, systemSymbol, graph); err != nil {
		log.Printf("Warning: failed to save graph to database: %v", err)
		// Don't fail - caching failure shouldn't break the operation
	} else {
		log.Printf("Graph for %s cached in database", systemSymbol)
	}

	return graph, nil
}

// saveToDatabase saves graph to database cache
func (p *SystemGraphProvider) saveToDatabase(ctx context.Context, systemSymbol string, graph map[string]interface{}) error {
	if err := p.graphRepo.Save(ctx, systemSymbol, graph); err != nil {
		return fmt.Errorf("failed to save graph: %w", err)
	}

	log.Printf("Saved graph for %s to database", systemSymbol)
	return nil
}
