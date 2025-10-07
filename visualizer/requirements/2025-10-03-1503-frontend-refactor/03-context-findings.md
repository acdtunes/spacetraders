# Context Findings

## Codebase Overview

**Total TypeScript Files:** 16 files in web/src
**Main Technologies:** React 18, TypeScript, PixiJS, Zustand, Vite
**Architecture Pattern:** Component-based UI with centralized state management

## Code Duplication Issues

### 1. PixiJS Initialization and Event Handling (High Priority)

**Location:**
- web/src/components/SpaceMap.tsx:1206-1240
- web/src/components/GalaxyView.tsx:55-91

**Issue:** Both SpaceMap and GalaxyView contain nearly identical code for:
- PixiJS app initialization (async init, canvas setup)
- Mouse wheel zoom handling (handleWheel)
- Mouse drag pan handling (handleMouseDown, handleMouseMove, handleMouseUp)
- Window resize handling (handleResize)
- Event listener setup/cleanup

**Duplication:** ~100 lines of duplicated logic

**SOLID Violation:** Single Responsibility Principle - components are handling both rendering concerns AND low-level canvas interaction management

### 2. API Pagination Logic (High Priority)

**Location:**
- web/src/services/api.ts:82-104 (getWaypoints)
- web/src/services/api.ts:126-148 (getAllSystems)

**Issue:** Identical pagination pattern repeated in two functions:
- While loop with hasMore flag
- Page counter increment
- Meta data checking for pagination
- Array concatenation

**Duplication:** ~20 lines per function

**SOLID Violation:** Don't Repeat Yourself (DRY) principle

### 3. Agent Adding Logic (Medium Priority)

**Location:**
- web/src/components/AddAgentCard.tsx:11-32 (handleSubmit)
- web/src/components/AgentManager.tsx:12-31 (handleAddAgent)

**Issue:** Very similar logic for:
- Token validation
- Loading state management
- Error handling
- API call to apiAddAgent
- Store update via addAgent

**Duplication:** ~20 lines of similar logic

**SOLID Violation:** DRY principle, both components have overlapping responsibilities

### 4. Dropdown/Modal Pattern (Medium Priority)

**Location:**
- web/src/components/AgentManager.tsx:44-121 (dropdown UI)
- web/src/components/SystemSelector.tsx:44-78 (dropdown UI)

**Issue:** Similar pattern for dropdown components:
- isOpen state management
- Toggle button with arrow indicator
- Absolute positioned dropdown container
- Styling with Tailwind classes
- List rendering inside dropdown

**Pattern Repetition:** Could be extracted to reusable Dropdown component

**SOLID Violation:** Open/Closed Principle - not easily extensible, must copy-paste pattern

### 5. Color Manipulation Helpers (Low Priority)

**Location:**
- web/src/components/SpaceMap.tsx:96-109 (lighten, darken functions)

**Issue:** Helper functions defined inline within component file
- Could be extracted to utility module
- May be needed in other components

**SOLID Violation:** Single Responsibility Principle

## Unused Code

### 1. Unused Type Definitions

**Location:** web/src/types/spacetraders.ts

**Unused Types:**
- `ShipTrail` (lines 182-186) - Only defined, never imported/used
- `Cooldown` (lines 66-71) - Only defined in types file
- `FlightMode` (line 13) - Type alias never used
- `CargoItem` (lines 38-43) - Only used within Cargo interface, never independently

**Impact:** Minor, but adds noise to type definitions

### 2. Unused waypoints in PollingService

**Location:** web/src/services/polling.ts:10-14

**Issue:**
- PollingService has a `waypoints` property
- Has `setWaypoints` method called from usePolling hook
- But waypoints are never actually used within the service

**Impact:** Dead code, confusing to maintainers

## SOLID Principle Violations

### 1. Single Responsibility Principle (SRP)

**SpaceMap.tsx (2011 lines)**
- Handles PixiJS canvas initialization
- Manages mouse/keyboard events
- Fetches waypoint data from API
- Implements ship position interpolation logic
- Draws ship shapes by role (complex graphics code)
- Handles market data display
- Manages ship filtering logic
- Total: ~7 distinct responsibilities

**Recommendation:** Extract to separate modules:
- `services/pixi/PixiCanvasManager.ts` - Canvas init, events
- `services/pixi/ShipRenderer.ts` - Ship drawing logic
- `utils/shipPositionCalculator.ts` - Position interpolation
- `hooks/useCanvas.ts` - Canvas lifecycle management

**GalaxyView.tsx (similar issues)**
- 200+ lines mixing canvas management with system rendering

### 2. Open/Closed Principle (OCP)

**Issue:** No abstraction for common UI patterns
- Cannot easily create new dropdown components without copy-paste
- Cannot extend canvas behavior without modifying SpaceMap/GalaxyView

**Recommendation:** Create reusable components:
- `components/common/Dropdown.tsx`
- `components/common/Modal.tsx`
- `hooks/usePixiCanvas.ts` with options for extensibility

### 3. Interface Segregation Principle (ISP)

**Store (useStore.ts) - Potential over-coupling**
- Components often import entire store but only use 2-3 properties
- 165 lines with many unrelated state slices

**Examples:**
- ShipFilters.tsx imports 14 properties from store (lines 4-22)
- Some components only need 2-3 properties

**Recommendation:** Consider splitting store or using selectors

### 4. Dependency Inversion Principle (DIP)

**Issue:** Components directly depend on concrete implementations
- Components import `pollingService` directly (concrete class)
- Components call API functions directly

**Current:**
```typescript
import { pollingService } from '../services/polling';
import { getWaypoints } from '../services/api';
```

**Better:** Inject dependencies via props or context for testability

## Separation of Concerns Issues

### 1. Business Logic in Components

**SpaceMap.tsx:**
- Ship filtering logic inline (lines 255-267)
- Position calculation inline (lines 32-88)
- Waypoint radius calculation (lines 9-29)

**Recommendation:** Extract to service/utility modules

### 2. Magic Numbers and Constants

**Throughout codebase:**
- SpaceMap.tsx has hardcoded values: scale factors, colors, sizes
- No centralized constants file
- Makes it hard to maintain consistency

**Example locations:**
- SpaceMap.tsx:94 - `const scale = 0.1;`
- SpaceMap.tsx:44 - `const orbitPeriod = 5000;`
- polling.ts:4-5 - `POLL_INTERVAL = 10000`, `REQUEST_DELAY = 1000`

**Recommendation:** Create `constants/` directory with themed files

### 3. Tight Coupling

**Issues:**
- All components directly access global Zustand store
- Services are singletons (polling.ts:85)
- Difficult to test in isolation

## Low Cohesion Issues

### 1. Mixed Concerns in api.ts

**Location:** web/src/services/api.ts

**Contains:**
- HTTP client logic (fetchApi function)
- Error handling for backend connection
- Multiple API endpoint implementations
- Pagination logic

**Recommendation:** Split into:
- `services/api/client.ts` - HTTP client only
- `services/api/agents.ts` - Agent endpoints
- `services/api/systems.ts` - System endpoints
- `utils/pagination.ts` - Reusable pagination

### 2. SpaceMap Component (Low Cohesion)

**Single file contains:**
- Helper functions (getWaypointRadius, interpolateShipPosition)
- Ship shape drawing functions (drawShipShape - 700+ lines)
- React component logic
- PixiJS rendering logic
- State management
- API calls

## Technical Debt

### 1. Type Safety Issues

**Location:** web/src/services/polling.ts:28
```typescript
const taggedShips = ships.map((ship) => ({
  ...ship,
  agentId: agent.id,
  agentColor: agent.color,
})) as any;  // Type cast to any
```

**Issue:** Using `any` to bypass TypeScript - loses type safety

### 2. API Response Casting

**Location:** web/src/services/api.ts:93-94
```typescript
const meta = (data as any).meta;
```

**Issue:** Using `any` instead of properly typing pagination metadata

## Best Practice Violations

### 1. Large Component Files

- SpaceMap.tsx: 2011 lines (should be < 300)
- Contains multiple concerns that should be separate modules

### 2. No Custom Hooks for Complex Logic

- Ship position interpolation could be `useShipPosition` hook
- Canvas management could be `usePixiCanvas` hook
- System fetching could be `useSystems` hook

### 3. Inconsistent Error Handling

- Some places use console.error
- Some places use error state
- Some places use alert()
- No centralized error handling strategy

## Files Analyzed

1. web/src/App.tsx
2. web/src/store/useStore.ts
3. web/src/services/api.ts
4. web/src/services/polling.ts
5. web/src/services/marketAnalysis.ts
6. web/src/components/SpaceMap.tsx
7. web/src/components/GalaxyView.tsx
8. web/src/components/AgentManager.tsx
9. web/src/components/AddAgentCard.tsx
10. web/src/components/SystemSelector.tsx
11. web/src/components/ShipFilters.tsx
12. web/src/components/ServerStatus.tsx
13. web/src/components/ErrorBoundary.tsx
14. web/src/hooks/usePolling.ts
15. web/src/types/spacetraders.ts

## Related Features Found

- Market analysis system (partially implemented, used in SpaceMap)
- Ship trail tracking (defined in store but minimal usage)
- Error boundary (good pattern, already follows best practices)
- Server status monitoring (good separation, follows SRP)

## Summary Statistics

- **Code Duplication Instances:** 5 major cases
- **Unused Code Items:** 5+ items
- **SOLID Violations:** All 5 principles have issues
- **Lines in Largest Component:** 2011 lines (SpaceMap.tsx)
- **Recommended New Files:** 12-15 new modules
- **Estimated Reduction:** ~40% reduction in LOC through extraction and deduplication
