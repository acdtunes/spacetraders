package persistence

import (
	"time"
)

// ShipModel represents the ships table
// This stores complete ship state that is the source of truth after daemon startup
type ShipModel struct {
	// Primary key fields
	ShipSymbol string       `gorm:"column:ship_symbol;primaryKey;not null"`
	PlayerID   int          `gorm:"column:player_id;primaryKey;not null"`
	Player     *PlayerModel `gorm:"foreignKey:PlayerID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`

	// Navigation state
	NavStatus   string     `gorm:"column:nav_status;default:'DOCKED'"`
	FlightMode  string     `gorm:"column:flight_mode;default:'CRUISE'"`
	ArrivalTime *time.Time `gorm:"column:arrival_time"`

	// Nav route origin + departure: where an IN_TRANSIT ship departed
	// from and when, carried from the API nav.route so DB consumers can compute
	// exact transit progress instead of approximating from poll timing. Empty/zero
	// and NULL respectively when the ship is not in transit. Additive columns with
	// no constraints (mirroring location_symbol/x/y + arrival_time), so AutoMigrate
	// adds them and no CHECK/enum drift gate is involved. Backed by migration 040.
	OriginSymbol  string     `gorm:"column:origin_symbol"`
	OriginX       float64    `gorm:"column:origin_x;default:0"`
	OriginY       float64    `gorm:"column:origin_y;default:0"`
	DepartureTime *time.Time `gorm:"column:departure_time"`

	// Location (denormalized for quick reconstruction)
	LocationSymbol string  `gorm:"column:location_symbol"`
	LocationX      float64 `gorm:"column:location_x;default:0"`
	LocationY      float64 `gorm:"column:location_y;default:0"`
	SystemSymbol   string  `gorm:"column:system_symbol"`

	// Fuel
	FuelCurrent  int `gorm:"column:fuel_current;default:0"`
	FuelCapacity int `gorm:"column:fuel_capacity;default:0"`

	// Cargo (JSONB for full item details)
	CargoCapacity  int    `gorm:"column:cargo_capacity;default:0"`
	CargoUnits     int    `gorm:"column:cargo_units;default:0"`
	CargoInventory string `gorm:"column:cargo_inventory;type:jsonb;default:'[]'"`

	// Ship specifications
	EngineSpeed int    `gorm:"column:engine_speed;default:0"`
	FrameSymbol string `gorm:"column:frame_symbol"`
	Role        string `gorm:"column:role"`
	Modules     string `gorm:"column:modules;type:jsonb;default:'[]'"`

	// Cooldown
	CooldownExpiration *time.Time `gorm:"column:cooldown_expiration"`

	// Assignment (existing)
	ContainerID      *string         `gorm:"column:container_id"` // Pointer to support NULL for idle ships
	Container        *ContainerModel `gorm:"foreignKey:ContainerID,PlayerID;references:ID,PlayerID;constraint:OnUpdate:CASCADE,OnDelete:SET NULL;"`
	AssignmentStatus string          `gorm:"column:assignment_status;default:'idle'"`
	AssignedAt       *time.Time      `gorm:"column:assigned_at"`
	ReleasedAt       *time.Time      `gorm:"column:released_at"`
	ReleaseReason    string          `gorm:"column:release_reason"`

	// Assignment owner distinguishes a coordinator container claim
	// from a captain reservation. "container" (default) or "captain".
	AssignmentOwner  string `gorm:"column:assignment_owner;default:'container'"`
	AssignmentReason string `gorm:"column:assignment_reason"`

	// DedicatedFleet is a permanent, operator-configured reservation for
	// a specific coordinator (e.g. "contract"). Empty means unreserved. Unlike
	// AssignmentOwner/ContainerID above, this is independent of any transient
	// container claim - it is a standing claim-filter, not a work assignment.
	DedicatedFleet string `gorm:"column:dedicated_fleet;default:''"`

	// ReservationOverrides is a per-hull cargo do-not-sell override set,
	// stored as a JSON object of good->bool. true force-reserves a good the default
	// would sell; false force-allows a default-reserved module's sale (deliberate
	// resale). A good absent from the object follows the code-level MODULE_*/MOUNT_*
	// classification. Like DedicatedFleet above, this is a standing per-hull tag
	// independent of any container assignment, so it must be preserved across the
	// restart-time API sync (which has no concept of it) or a reservation is
	// silently wiped and a staged module is re-exposed to coordinator liquidation.
	ReservationOverrides string `gorm:"column:reservation_overrides;type:jsonb;default:'{}'"`

	// RetiringAt is the operator's retirement mark; preserved across the API sync like DedicatedFleet.
	RetiringAt *time.Time `gorm:"column:retiring_at"`

	// Power/slot/crew data. Reactor and frame-slot fields are fixed
	// for the life of the hull - reactors/frames have no swap endpoint in the
	// SpaceTraders API. Flattened into columns (not JSON) to mirror the
	// existing single-value fields above (FuelCurrent/EngineSpeed/FrameSymbol
	// etc.); Mounts is a JSON list like Modules since it's a collection.
	// Additive columns only - no CHECK constraints on this model, so
	// AutoMigrate creates them with no manual migration required.
	ReactorSymbol            string `gorm:"column:reactor_symbol"`
	ReactorName              string `gorm:"column:reactor_name"`
	ReactorPowerOutput       int    `gorm:"column:reactor_power_output;default:0"`
	ReactorRequirementsPower int    `gorm:"column:reactor_requirements_power;default:0"`
	ReactorRequirementsCrew  int    `gorm:"column:reactor_requirements_crew;default:0"`
	ReactorRequirementsSlots int    `gorm:"column:reactor_requirements_slots;default:0"`
	ModuleSlots              int    `gorm:"column:module_slots;default:0"`
	MountingPoints           int    `gorm:"column:mounting_points;default:0"`
	Mounts                   string `gorm:"column:mounts;type:jsonb;default:'[]'"`
	CrewCurrent              int    `gorm:"column:crew_current;default:0"`
	CrewRequired             int    `gorm:"column:crew_required;default:0"`
	CrewCapacity             int    `gorm:"column:crew_capacity;default:0"`

	// Sync metadata
	SyncedAt time.Time `gorm:"column:synced_at;autoCreateTime"`
	Version  int       `gorm:"column:version;default:1"`
}

func (ShipModel) TableName() string {
	return "ships"
}

// CargoItemJSON is a JSON helper type for cargo inventory items
type CargoItemJSON struct {
	Symbol      string `json:"symbol"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Units       int    `json:"units"`
}

// ModuleJSON is a JSON helper type for ship modules
type ModuleJSON struct {
	Symbol       string           `json:"symbol"`
	Capacity     int              `json:"capacity"`
	Range        int              `json:"range"`
	Requirements RequirementsJSON `json:"requirements"`
}

// MountJSON is a JSON helper type for installed ship mounts (mining lasers,
// gas siphons, sensor arrays, weapons, etc.).
type MountJSON struct {
	Symbol       string           `json:"symbol"`
	Name         string           `json:"name"`
	Strength     int              `json:"strength"`
	Deposits     []string         `json:"deposits"`
	Requirements RequirementsJSON `json:"requirements"`
}

// RequirementsJSON is a JSON helper type for the power/crew/slot cost
// declared by a module or mount (SpaceTraders API schema: ShipRequirements).
type RequirementsJSON struct {
	Power int `json:"power"`
	Crew  int `json:"crew"`
	Slots int `json:"slots"`
}
