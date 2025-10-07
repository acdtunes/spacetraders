# Context Findings

**Date:** 2025-10-03
**Phase:** Context Gathering

## Overview

Analysis of codebase to support three new features:
1. Market data and trade opportunity visualization
2. Multi-system galaxy/sector view
3. Notifications and alerts system

---

## Current Architecture Analysis

### Frontend State Management (web/src/store/useStore.ts)
- Uses Zustand for global state
- Current state includes: agents, ships, waypoints, currentSystem, UI filters, polling status
- Pattern: State setters are simple actions that components call
- Ship trails already partially implemented (Map<string, ShipPosition[]>)

### API Client Pattern (web/src/services/api.ts)
- All backend calls go through `/api` prefix
- Uses generic `fetchApi<T>()` wrapper with error handling
- Current system APIs: `getSystem()`, `getWaypoints()`, `getWaypoint()`
- Pagination handled client-side for waypoints (loops until all fetched)

### Backend Proxy (server/routes/)
- Express router pattern with `/api/agents` and `/api/systems`
- Uses SpaceTradersClient from server/src/client.ts
- Public endpoints (systems) don't require auth, agent endpoints do
- Tokens stored in server/db/agents.json, never sent to frontend

### PixiJS Rendering (web/src/components/SpaceMap.tsx)
- Canvas-based rendering with zoom/pan controls
- Waypoints rendered as static circles with colors by type (WAYPOINT_COLORS object)
- Ships rendered as triangles that rotate toward destination
- Ticker animation for real-time ship position interpolation
- Graphics cleanup pattern: label children, filter, remove old ones

### Polling Service (web/src/services/polling.ts)
- ShipPollingService class with 5-second interval
- Staggers agent requests with 600ms delay for rate limiting
- Tags ships with agentId and agentColor before storing

---

## SpaceTraders API Research

### Market Data Endpoints

**Endpoint:** `GET /systems/{systemSymbol}/waypoints/{waypointSymbol}/market`

**Key Findings:**
- Markets exist at waypoints with the "Marketplace" trait
- Market data includes:
  - **Exports**: Goods produced locally (lower purchase prices)
  - **Imports**: Goods consumed locally (higher sell prices)
  - **Exchange**: Goods traded without local production/consumption
- Prices fluctuate based on supply/demand
- Market visibility: Requires a ship at the waypoint to see live prices (API restriction)
- Markets grow over time as agents trade more

**Trade Strategy:**
- Buy exports, sell at import locations = most profitable
- More advanced systems export higher-tech goods

### Systems Endpoint

**Endpoint:** `GET /systems`

**Purpose:** List all systems for galaxy view
- Includes system coordinates (x, y)
- System type information
- Waypoint count
- Pagination required for full galaxy

**Endpoint:** `GET /systems/{systemSymbol}`

**Current Usage:** Already implemented in server/routes/systems.ts
- Returns system details including waypoints list

---

## Files Requiring Modification

### New Files to Create

**Backend:**
1. `server/routes/markets.ts` - Market data proxy endpoints
2. `server/routes/galaxy.ts` - Multi-system listing endpoint

**Frontend:**
3. `web/src/types/markets.ts` - Market data type definitions
4. `web/src/components/GalaxyView.tsx` - Multi-system visualization component
5. `web/src/components/MarketOverlay.tsx` - Market data overlay for waypoints
6. `web/src/components/NotificationCenter.tsx` - Alert/notification UI
7. `web/src/services/alertService.ts` - Alert rule engine
8. `web/src/services/marketAnalysis.ts` - Trade opportunity calculations

### Files to Modify

**Backend:**
1. `server/index.ts` - Add new route imports
2. `server/routes/systems.ts` - Add market data endpoint

**Frontend:**
3. `web/src/store/useStore.ts` - Add market data, galaxy data, notifications state
4. `web/src/services/api.ts` - Add market and galaxy API functions
5. `web/src/services/polling.ts` - Extend to poll market data
6. `web/src/components/SpaceMap.tsx` - Add market visualization, integrate alerts
7. `web/src/App.tsx` - Add galaxy view toggle, notification center
8. `web/src/types/spacetraders.ts` - Add Market interfaces

---

## Technical Constraints

### SpaceTraders API Rate Limits
- 2 requests/second sustained
- 10 request burst over 10 seconds
- **Implication:** Market data polling must be careful not to exceed limits
  - Can't poll all markets simultaneously
  - Need to prioritize markets where ships are located
  - Consider caching market data with staleness tolerance

### Market Visibility Restriction
- API only returns market data if an agent's ship is at that waypoint
- **Implication:**
  - Can only show market data for locations with ships present
  - Could show "last known" data with timestamp
  - Trade opportunity detection limited to discovered markets

### Galaxy Scale
- Unknown number of systems (likely hundreds or thousands)
- **Implication:**
  - Full galaxy view needs efficient rendering
  - Likely need PixiJS sprites or instancing
  - Pagination required for systems list
  - May need viewport culling for performance

---

## Similar Patterns in Codebase

### Adding New Data Type to Store
**Pattern from ship tracking:**
```typescript
// In useStore.ts
ships: Ship[],
setShips: (ships) => set({ ships }),

// In polling.ts
const ships = await fetchAllShips(agents);
onUpdate(ships);

// In component
const ships = useStore((state) => state.ships);
```

**Apply to markets:**
- Add `markets: Map<string, Market>` to store
- Add `setMarkets()` action
- Poll market data alongside ships
- Components read from store

### Adding New Route
**Pattern from systems routes:**
```typescript
// server/index.ts
import systemsRouter from './routes/systems.js';
app.use('/api/systems', systemsRouter);

// server/routes/systems.ts
const router = Router();
router.get('/:systemSymbol', async (req, res) => {
  const client = new SpaceTradersClient(API_BASE_URL);
  const system = await client.get(`/systems/${req.params.systemSymbol}`);
  res.json(system);
});
```

**Apply to markets:**
- Create `server/routes/markets.ts`
- Import in server/index.ts
- Add endpoint for market data
- Requires agent token to access

### Adding New Visualization
**Pattern from waypoint rendering:**
```typescript
// Define colors by type
const WAYPOINT_COLORS: Record<string, number> = {
  PLANET: 0x4a90e2,
  // ...
};

// Render in useEffect
waypoints.forEach((waypoint) => {
  const color = WAYPOINT_COLORS[waypoint.type];
  graphics.circle(x, y, radius);
  graphics.fill({ color });
});
```

**Apply to markets:**
- Define colors for market types (import/export)
- Add market indicators to waypoints (e.g., $ icon, colored ring)
- Hover/click to show market details

---

## Integration Points

### 1. Market Data Integration
- **Backend:** New endpoint at `/api/markets/:systemSymbol/:waypointSymbol`
- **Frontend:** API function `getMarket(system, waypoint)`
- **State:** Add to useStore as `Map<waypointSymbol, Market>`
- **Polling:** Extend polling service to fetch markets for waypoints with ships
- **Rendering:** Add market indicators to waypoints in SpaceMap

### 2. Galaxy View Integration
- **Backend:** New endpoint at `/api/galaxy` or `/api/systems` (list all)
- **Frontend:** New component GalaxyView alongside SpaceMap
- **State:** Add `systems: System[]` and `viewMode: 'system' | 'galaxy'`
- **Rendering:** New PixiJS scene showing all systems as dots, ships as counts
- **Navigation:** Click system to zoom into system view

### 3. Alert System Integration
- **No backend needed** - Client-side rule evaluation
- **State:** Add `alerts: Alert[]` and `alertRules: AlertRule[]`
- **Service:** alertService checks conditions every poll cycle
- **UI:** NotificationCenter component shows active alerts
- **Triggers:** Low fuel (<20%), cargo full (>90%), ship arrived, market arbitrage (>20% profit)

---

## Best Practices Research

### Notification/Alert Patterns
- Use browser Notification API for background alerts (requires permission)
- In-app toast/banner for non-intrusive notifications
- Persistent notification center for history
- Color coding: red (critical), yellow (warning), green (opportunity)

### Trade Opportunity Calculation
```typescript
// Simple arbitrage detection
for each export waypoint:
  for each import waypoint:
    if import.sell_price - export.buy_price > threshold:
      calculate profit per unit
      calculate distance
      yield trade opportunity with profit/distance ratio
```

### Multi-Scale Visualization
- Use PixiJS Container hierarchy: galaxy > system > waypoint
- Transform between coordinate spaces
- Smooth camera transitions between zoom levels
- Level-of-detail: show different info at different zoom levels

---

## Assumptions

1. **Market Data Access:** Assume we can only show markets where ships are present (API constraint)
2. **Galaxy Size:** Assume < 1000 systems (if more, need virtualization)
3. **Alert Rules:** Assume simple threshold-based rules (fuel %, cargo %, profit %)
4. **Market Updates:** Assume market data can refresh on same 5s cycle as ships
5. **Historical Data:** No server-side persistence needed (discovery phase confirmed)
6. **Read-Only:** No command interface needed (discovery phase confirmed)

---

## Next Phase Preparation

Ready to ask expert questions about:
- Specific market data to display (all goods vs summary?)
- Galaxy view interaction model (click to drill down?)
- Alert rule configuration (hardcoded vs user configurable?)
- Market polling strategy (all markets vs ships only?)
- Trade route visualization details (lines between waypoints?)
