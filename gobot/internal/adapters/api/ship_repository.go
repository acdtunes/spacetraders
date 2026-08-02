package api

import (
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// shipListCacheTTL defines how long ship list cache is valid
// 15 seconds is enough to prevent redundant calls across coordinators
// while still allowing fresh data for navigation decisions
const shipListCacheTTL = 15 * time.Second

// cachedShipList stores ship list with timestamp for TTL expiration
type cachedShipList struct {
	ships     []*navigation.Ship
	fetchedAt time.Time
}

// ShipRepository implements ShipRepository using the SpaceTraders API + Database
// After daemon startup, the database is the source of truth for ship state.
// Ships are synced from API on startup, and all queries read from the database.
// API calls are only made for state-changing operations (navigate, dock, orbit, refuel, cargo).
//
// Caching Strategy:
//   - In-memory cache (15s TTL): Prevents redundant DB reads
//     when multiple coordinators call FindAllByPlayer in quick succession
type ShipRepository struct {
	apiClient        domainPorts.APIClient
	playerRepo       player.PlayerRepository
	waypointRepo     system.WaypointRepository
	waypointProvider system.IWaypointProvider
	db               *gorm.DB     // Database connection for ship state persistence
	clock            shared.Clock // Clock for timestamps
	shipListCache    sync.Map     // key: playerID (int) -> *cachedShipList

	// Optional arrival scheduler - notified after navigation to schedule state transition
	arrivalScheduler navigation.ArrivalScheduler

	// positionReanchors is notified whenever a sync writes a position that CONTRADICTS
	// the row it replaced — proof a completed move was never persisted. Optional
	// (setter-injected); see ship_position_reanchor.go.
	positionReanchors PositionReanchorObserver

	// CAS-retry knob. maxCASRetries<=0 means "use defaultMaxCASRetries". Defaults
	// to its zero value so retry is LIVE by default across every construction path
	// (RULINGS #5); the daemon overrides it from DaemonConfig via SetCASRetryPolicy.
	maxCASRetries int
}

// defaultMaxCASRetries is the number of re-find + re-apply attempts SaveWithRetry
// makes on a ships.version conflict before falling back to last-write-wins, when
// the daemon has not configured max_cas_retries.
const defaultMaxCASRetries = 3

// NewShipRepository creates a new hybrid API+DB ship repository
func NewShipRepository(
	apiClient domainPorts.APIClient,
	playerRepo player.PlayerRepository,
	waypointRepo system.WaypointRepository,
	waypointProvider system.IWaypointProvider,
	db *gorm.DB,
	clock shared.Clock,
) *ShipRepository {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &ShipRepository{
		apiClient:        apiClient,
		playerRepo:       playerRepo,
		waypointRepo:     waypointRepo,
		waypointProvider: waypointProvider,
		db:               db,
		clock:            clock,
	}
}

// SetArrivalScheduler sets the scheduler that will be notified after navigation
// to schedule arrival state transitions. This uses setter injection to avoid
// circular dependencies during construction.
func (r *ShipRepository) SetArrivalScheduler(scheduler navigation.ArrivalScheduler) {
	r.arrivalScheduler = scheduler
}

// SetCASRetryPolicy configures the optimistic-concurrency retry knob for
// SaveWithRetry. maxRetries<=0 selects the built-in default (defaultMaxCASRetries).
// Wired from DaemonConfig at boot; setter injection mirrors SetArrivalScheduler.
func (r *ShipRepository) SetCASRetryPolicy(maxRetries int) {
	r.maxCASRetries = maxRetries
}

// resolvedCASRetries reports the effective retry bound: the configured value,
// or defaultMaxCASRetries when unset.
func (r *ShipRepository) resolvedCASRetries() int {
	if r.maxCASRetries <= 0 {
		return defaultMaxCASRetries
	}
	return r.maxCASRetries
}
