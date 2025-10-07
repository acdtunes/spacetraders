# Requirements Specification: Frontend Refactoring

## Problem Statement

The SpaceTraders Fleet Visualization frontend codebase has accumulated technical debt that violates SOLID principles, contains significant code duplication, and has low cohesion with poor separation of concerns. The largest component (SpaceMap.tsx) contains 2011 lines, mixing multiple responsibilities. This makes the codebase difficult to maintain, test, and extend.

**Key Issues:**
1. ~200+ lines of duplicated PixiJS initialization/event handling code
2. Identical pagination logic repeated across multiple API functions
3. 2011-line SpaceMap component violating Single Responsibility Principle
4. Duplicate agent-adding logic in two components
5. No reusable UI patterns for dropdowns/modals
6. Unused type definitions cluttering the codebase
7. Tight coupling between components and services
8. Business logic embedded in React components

## Solution Overview

Perform a comprehensive refactoring of the frontend codebase to:
- Extract reusable PixiJS canvas management into custom hooks
- Consolidate duplicate code into shared utilities and services
- Split large components into focused, single-responsibility modules
- Remove unused code and type definitions
- Establish clear separation of concerns with improved directory structure
- Apply SOLID principles throughout the codebase
- Reduce total lines of code by ~40% through deduplication

All changes must maintain backward compatibility with existing APIs and data structures.

---

## Functional Requirements

### FR1: Maintain Application Functionality

**Priority:** Critical

**Description:** All existing features must continue to work exactly as they do now:
- Real-time ship tracking and visualization
- Agent management (add, remove, toggle visibility)
- System and galaxy view switching
- Ship filtering by status and agent
- Waypoint filtering by type
- Market data display
- Ship position interpolation and orbital mechanics
- Polling service for ship updates

**Acceptance Criteria:**
- All user interactions work identically before and after refactoring
- No visual changes to the UI
- No breaking changes to component props or store interface
- All API calls continue to work

### FR2: Reduce Code Duplication

**Priority:** High

**Description:** Eliminate duplicate code patterns identified in the codebase:
- PixiJS initialization and event handling
- API pagination logic
- Agent adding logic
- Dropdown/modal UI patterns
- Color manipulation utilities

**Acceptance Criteria:**
- No two functions/components implement the same logic
- Shared utilities are reusable and well-tested
- Duplication reduced by at least 80%

### FR3: Remove Unused Code

**Priority:** Medium

**Description:** Delete unused code to reduce noise and confusion:
- Unused type definitions (ShipTrail, Cooldown, FlightMode, CargoItem)
- Unused properties in services (e.g., waypoints in PollingService)
- Any other dead code discovered during refactoring

**Acceptance Criteria:**
- All exported types are used somewhere in the codebase
- No unused variables or functions
- TypeScript compiler shows no unused warnings

### FR4: Apply SOLID Principles

**Priority:** High

**Description:** Refactor code to follow SOLID principles:
- **Single Responsibility:** Each module has one clear purpose
- **Open/Closed:** Extensible through composition, not modification
- **Liskov Substitution:** Proper abstraction hierarchies
- **Interface Segregation:** Components don't depend on unused props
- **Dependency Inversion:** Depend on abstractions, not concrete implementations

**Acceptance Criteria:**
- No component file exceeds 300 lines
- Business logic separated from UI components
- Shared logic extracted into hooks, utilities, or services
- Components receive only the props they actually use

### FR5: Improve Code Organization

**Priority:** High

**Description:** Reorganize code with better directory structure and file locations for improved discoverability and maintainability.

**Acceptance Criteria:**
- New directory structure implemented
- Related code grouped together
- Import paths are clear and logical
- No circular dependencies

---

## Technical Requirements

### TR1: Create New Directory Structure

**Location:** web/src/

**New directories to create:**
```
web/src/
├── components/
│   ├── common/          # Reusable UI components
│   ├── space/           # Space-related components (SpaceMap, GalaxyView)
│   └── agent/           # Agent-related components
├── hooks/
│   ├── usePixiCanvas.ts
│   └── useAgentForm.ts
├── services/
│   ├── api/
│   │   ├── client.ts    # HTTP client
│   │   ├── agents.ts    # Agent endpoints
│   │   └── systems.ts   # System endpoints
│   └── pixi/
│       ├── CanvasManager.ts
│       └── ShipRenderer.ts
├── utils/
│   ├── pagination.ts
│   ├── colors.ts
│   └── shipPosition.ts
└── constants/
    ├── pixi.ts
    └── api.ts
```

**Implementation Notes:**
- Move existing files to new locations
- Update all import paths
- Maintain backward compatibility for any external imports

### TR2: Extract PixiJS Canvas Management Hook

**New File:** web/src/hooks/usePixiCanvas.ts

**Purpose:** Consolidate PixiJS initialization and event handling from SpaceMap and GalaxyView

**Interface:**
```typescript
interface PixiCanvasConfig {
  width: number;
  height: number;
  backgroundColor?: number;
  minScale?: number;
  maxScale?: number;
  onReady?: (app: PIXI.Application, container: PIXI.Container) => void;
}

function usePixiCanvas(
  canvasRef: React.RefObject<HTMLDivElement>,
  config: PixiCanvasConfig
): {
  app: PIXI.Application | null;
  container: PIXI.Container | null;
  isReady: boolean;
}
```

**Functionality:**
- PixiJS app initialization
- Canvas mounting/unmounting
- Mouse wheel zoom with configurable limits
- Mouse drag pan
- Window resize handling
- Event listener cleanup

**Files to Modify:**
- web/src/components/SpaceMap.tsx (remove lines ~1150-1250)
- web/src/components/GalaxyView.tsx (remove lines ~14-100)

**Patterns to Follow:**
- Use React refs for app and container
- Return null during async initialization
- Clean up all event listeners on unmount
- Debounce resize events if needed

### TR3: Create Reusable Pagination Utility

**New File:** web/src/utils/pagination.ts

**Purpose:** Extract pagination logic from API functions

**Interface:**
```typescript
interface PaginatedFetcher<T> {
  fetchPage: (page: number, limit: number) => Promise<ApiResponse<T[]>>;
}

async function fetchAllPaginated<T>(
  fetcher: PaginatedFetcher<T>,
  limit?: number
): Promise<T[]>
```

**Files to Modify:**
- web/src/services/api.ts:82-104 (getWaypoints)
- web/src/services/api.ts:126-148 (getAllSystems)

**Implementation Hints:**
- Handle both meta-based and length-based pagination detection
- Return all results in a single array
- Handle errors gracefully
- Add optional progress callback for large datasets

### TR4: Consolidate Agent Management

**Action:** Merge AddAgentCard and AgentManager into single component

**New File:** web/src/components/agent/AgentManager.tsx

**Purpose:** Single source of truth for agent management UI

**Features:**
- Agent list display
- Add new agent form (inline or modal)
- Delete agent with confirmation
- Toggle agent visibility
- Display agent credits and metadata

**Files to Delete:**
- web/src/components/AddAgentCard.tsx

**Files to Modify:**
- web/src/App.tsx (update import and usage)

**Design Considerations:**
- Show add form inline when no agents exist (welcome screen)
- Show add form in dropdown when agents exist
- Extract form logic to `useAgentForm` hook for reusability

### TR5: Create Reusable Dropdown Component

**New File:** web/src/components/common/Dropdown.tsx

**Purpose:** Reusable dropdown pattern for AgentManager, SystemSelector, etc.

**Interface:**
```typescript
interface DropdownProps {
  trigger: React.ReactNode;
  isOpen: boolean;
  onToggle: () => void;
  children: React.ReactNode;
  className?: string;
  position?: 'left' | 'right';
}
```

**Files to Modify:**
- web/src/components/AgentManager.tsx (use Dropdown component)
- web/src/components/SystemSelector.tsx (use Dropdown component)

**Patterns to Follow:**
- Compound component pattern with Dropdown.Item subcomponent
- Handle click outside to close
- Keyboard navigation support (arrow keys, escape)
- Proper ARIA attributes for accessibility

### TR6: Split SpaceMap Component

**Current:** web/src/components/SpaceMap.tsx (2011 lines)

**Split into:**

1. **web/src/components/space/SpaceMap.tsx** (~200 lines)
   - Main component with rendering logic
   - State management
   - Effect hooks for data fetching
   - Uses extracted utilities and hooks

2. **web/src/services/pixi/ShipRenderer.ts** (~400 lines)
   - `drawShipShape(graphics, role, color)` function
   - All ship shape drawing logic
   - Ship role constants

3. **web/src/utils/shipPosition.ts** (~100 lines)
   - `interpolateShipPosition(ship, waypoints)` function
   - `getWaypointRadius(waypoint)` function
   - Orbital mechanics calculations

4. **web/src/utils/colors.ts** (~30 lines)
   - `lightenColor(color, amount)` function
   - `darkenColor(color, amount)` function
   - Color utility helpers

5. **web/src/constants/pixi.ts** (~50 lines)
   - `WAYPOINT_COLORS` object
   - `SHIP_SCALE`, `ORBIT_PERIOD`, etc.
   - All magic numbers extracted

**Implementation Strategy:**
1. Extract utilities first (colors, shipPosition)
2. Extract ShipRenderer as pure functions
3. Extract constants
4. Refactor main component to use extracted code
5. Update imports throughout

### TR7: Split API Service

**Current:** web/src/services/api.ts (149 lines)

**Split into:**

1. **web/src/services/api/client.ts**
   - `fetchApi<T>(endpoint, options)` function
   - HTTP error handling
   - Backend connection error messages

2. **web/src/services/api/agents.ts**
   - `getAgents()`
   - `addAgent(token)`
   - `updateAgent(id, updates)`
   - `deleteAgent(id)`
   - `getAgentShips(agentId)`

3. **web/src/services/api/systems.ts**
   - `getSystem(systemSymbol)`
   - `getWaypoints(systemSymbol)` (using pagination util)
   - `getWaypoint(systemSymbol, waypointSymbol)`
   - `getMarket(...)`
   - `getAllSystems()` (using pagination util)

4. **web/src/services/api/index.ts**
   - Re-export all API functions for convenience

**Files to Modify:**
- All files importing from `'../services/api'`

**Patterns to Follow:**
- Keep client.ts independent
- Use pagination utility in systems.ts
- Maintain same function signatures for backward compatibility

### TR8: Remove Unused Types

**File:** web/src/types/spacetraders.ts

**Types to Remove:**
- `ShipTrail` (lines 182-186)
- `Cooldown` (lines 66-71)
- `FlightMode` (line 13)
- `CargoItem` (lines 38-43) - only if not used in Cargo interface

**Verification:**
- Run global search for each type name
- Confirm zero usage before deletion
- Remove from export statements

### TR9: Remove Unused Code from PollingService

**File:** web/src/services/polling.ts

**Code to Remove:**
- Line 10: `private waypoints: Map<string, Waypoint> = new Map();`
- Lines 12-14: `setWaypoints(waypoints)` method
- Line 2: Remove Waypoint import if unused elsewhere

**Files to Modify:**
- web/src/hooks/usePolling.ts (remove setWaypoints call, lines 8-11)

### TR10: Create Constants Files

**New Files:**

1. **web/src/constants/pixi.ts**
```typescript
export const PIXI_CONSTANTS = {
  SHIP_SCALE: 0.1,
  STROKE_WIDTH: 0.1,
  ORBIT_PERIOD: 5000,
  MIN_ZOOM: 0.05,
  MAX_ZOOM: 10,
} as const;

export const WAYPOINT_COLORS: Record<string, number> = {
  PLANET: 0x4ECDC4,
  GAS_GIANT: 0xF7B731,
  MOON: 0xA8DADC,
  // ... etc
};
```

2. **web/src/constants/api.ts**
```typescript
export const API_CONSTANTS = {
  BASE_URL: '/api',
  POLL_INTERVAL: 10000,
  REQUEST_DELAY: 1000,
  PAGINATION_LIMIT: 20,
} as const;
```

**Files to Modify:**
- Replace all magic numbers with constant references

### TR11: Improve Type Safety

**Files to Fix:**

1. **web/src/services/polling.ts:28**
   - Remove `as any` cast
   - Create proper `TaggedShip` type extending Ship

2. **web/src/services/api.ts:93**
   - Create `PaginationMeta` interface
   - Type the meta object properly

**New Types:**
```typescript
// In types/spacetraders.ts
export interface PaginationMeta {
  page: number;
  limit: number;
  total: number;
}

export interface TaggedShip extends Ship {
  agentId: string;
  agentColor: string;
}
```

---

## Implementation Hints

### Order of Implementation

**Phase 1: Extract Utilities (Low Risk)**
1. Create constants files
2. Extract color utilities
3. Extract pagination utility
4. Extract ship position utilities
5. Update all imports

**Phase 2: Extract Services (Medium Risk)**
1. Split API service
2. Remove unused code from PollingService
3. Test all API calls

**Phase 3: Extract Hooks (Medium Risk)**
1. Create usePixiCanvas hook
2. Update SpaceMap to use hook
3. Update GalaxyView to use hook
4. Test canvas rendering

**Phase 4: Component Refactoring (High Risk)**
1. Extract ShipRenderer
2. Simplify SpaceMap component
3. Create Dropdown component
4. Consolidate Agent components
5. Test all UI interactions

**Phase 5: Cleanup (Low Risk)**
1. Remove unused types
2. Organize directory structure
3. Update documentation

### Testing Strategy

**For Each Change:**
1. Test locally with running backend
2. Verify all visual elements render correctly
3. Test all user interactions
4. Check browser console for errors
5. Verify TypeScript compiles without errors

**Key Test Cases:**
- Add/remove agents
- Switch between system and galaxy view
- Filter ships by status and agent
- Zoom and pan canvas
- Ship movement animation
- Market data display

### Code Review Checklist

- [ ] No component exceeds 300 lines
- [ ] No duplicate code patterns
- [ ] All imports use new paths
- [ ] No TypeScript errors or warnings
- [ ] All constants extracted from components
- [ ] No `any` type casts
- [ ] All tests pass
- [ ] Application works identically to before

---

## Acceptance Criteria

### AC1: Code Duplication Eliminated
- [ ] PixiJS initialization code exists in exactly one location (usePixiCanvas)
- [ ] Pagination logic exists in exactly one location (utils/pagination.ts)
- [ ] Agent form logic consolidated into single component
- [ ] Dropdown pattern extracted to reusable component

### AC2: SOLID Principles Applied
- [ ] No component file exceeds 300 lines
- [ ] SpaceMap.tsx split into 5+ focused modules
- [ ] Each module has single, clear responsibility
- [ ] Shared logic extracted into hooks/utilities
- [ ] Components depend on abstractions (hooks) not implementations

### AC3: Code Organization Improved
- [ ] New directory structure created
- [ ] All files in appropriate directories
- [ ] Related code grouped together
- [ ] Clear separation between components, services, hooks, utils

### AC4: Unused Code Removed
- [ ] ShipTrail type removed
- [ ] Cooldown type removed
- [ ] FlightMode type removed
- [ ] CargoItem type removed (if unused)
- [ ] Unused waypoints property removed from PollingService
- [ ] No unused imports or variables

### AC5: Type Safety Improved
- [ ] No `as any` type casts in codebase
- [ ] PaginationMeta interface created and used
- [ ] TaggedShip interface created and used
- [ ] All API responses properly typed

### AC6: Application Functionality Maintained
- [ ] All features work identically to before
- [ ] No visual changes to UI
- [ ] No breaking changes to public APIs
- [ ] All existing imports continue to work
- [ ] Performance is equal or better

### AC7: Code Quality Improved
- [ ] Lines of code reduced by ~40%
- [ ] No TypeScript compiler warnings
- [ ] Consistent code style throughout
- [ ] Clear, descriptive function/variable names
- [ ] Comments explain "why" not "what"

---

## Assumptions

1. **Testing:** Manual testing is sufficient; no automated tests required
2. **Backward Compatibility:** All existing component props and store interfaces must remain unchanged
3. **Dependencies:** No new npm packages need to be installed
4. **Documentation:** Update CLAUDE.md after refactoring to reflect new structure
5. **Performance:** Refactoring should not negatively impact render performance
6. **Browser Support:** Same browser support as before (modern browsers)
7. **Backend:** Backend API remains unchanged during frontend refactoring

---

## Non-Requirements

The following are explicitly **not** part of this refactoring:

- Adding new features or functionality
- Changing UI/UX design or layout
- Writing automated tests
- Optimizing rendering performance (unless regression occurs)
- Updating dependencies or upgrading libraries
- Modifying backend code
- Adding internationalization
- Implementing error tracking/logging
- Adding analytics

---

## Success Metrics

**Code Quality Metrics:**
- Average component size: < 200 lines (currently 500+)
- Largest component: < 300 lines (currently 2011)
- Code duplication: < 5% (currently ~15%)
- Type safety: 100% (no `any` casts)

**Maintainability Metrics:**
- Time to locate specific logic: -50% (better organization)
- Time to add new canvas feature: -60% (reusable hook)
- Time to add new API endpoint: -40% (clear structure)

**Developer Experience:**
- Clear, intuitive directory structure
- Easy to find related code
- Self-documenting code organization
- Reduced cognitive load

---

## Risks and Mitigations

### Risk 1: Breaking Changes During Refactoring
**Likelihood:** Medium
**Impact:** High
**Mitigation:**
- Implement changes incrementally
- Test after each major change
- Keep git commits small and focused
- Have rollback plan

### Risk 2: PixiJS Hook Complexity
**Likelihood:** Low
**Impact:** Medium
**Mitigation:**
- Start with simple implementation
- Add features incrementally
- Test with both SpaceMap and GalaxyView
- Document edge cases

### Risk 3: Import Path Chaos
**Likelihood:** Medium
**Impact:** Low
**Mitigation:**
- Use IDE refactoring tools for renames
- Update all imports in single commit
- Use barrel exports (index.ts) for convenience
- TypeScript will catch broken imports

### Risk 4: Performance Regression
**Likelihood:** Low
**Impact:** Medium
**Mitigation:**
- Profile before and after
- Ensure hooks don't cause unnecessary re-renders
- Memoize expensive computations
- Monitor frame rate during canvas rendering

---

## Follow-up Work (Out of Scope)

After this refactoring is complete, consider these improvements:

1. **Add Unit Tests:** Test utilities, hooks, and services
2. **Add Integration Tests:** Test component interactions
3. **Performance Optimization:** Profile and optimize rendering
4. **Error Handling:** Centralized error boundary and logging
5. **Accessibility:** ARIA labels, keyboard navigation
6. **Documentation:** Component documentation with examples
7. **Storybook:** Component showcase and documentation
