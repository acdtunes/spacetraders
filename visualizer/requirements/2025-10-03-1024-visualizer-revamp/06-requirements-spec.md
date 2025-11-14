# Requirements Specification: SpaceTraders Visualizer Revamp

**Project:** SpaceTraders Fleet Visualization Enhancement
**Date:** 2025-10-03
**Status:** Ready for Implementation

---

## 1. Problem Statement

The current SpaceTraders visualizer provides real-time ship tracking within a single system, but lacks economic intelligence and strategic overview capabilities. Players managing fleets across multiple systems need:

1. **Market visibility** - Understanding trade opportunities at waypoints where ships are located
2. **Strategic overview** - Viewing fleet distribution across the entire galaxy/sector
3. **Better UX** - More intuitive navigation between systems

The revamp will add market data visualization and multi-system galaxy view to help players make better trading decisions and manage distributed fleets more effectively.

---

## 2. Solution Overview

### Feature 1: Market Data Visualization
Add real-time market data overlays to waypoints showing import/export information and profitable trade opportunities.

### Feature 2: Galaxy View
Add a zoomed-out view showing all systems simultaneously with fleet distribution, allowing visual navigation between systems.

---

## 3. Functional Requirements

### 3.1 Market Data Visualization

**FR-1.1:** System shall poll market data for waypoints where ships are currently located
- Poll during the existing 5-second ship polling cycle
- Use agent tokens to access market data via SpaceTraders API
- Only request market data for waypoints with ships present (API constraint)

**FR-1.2:** System shall display market summary overlays on waypoints with markets
- Show count of import goods
- Show count of export goods
- Show top 2 most profitable trade opportunities (if any exist)
- Visual indicator (icon or colored ring) distinguishing market waypoints

**FR-1.3:** Market overlays shall be toggleable
- Similar to existing "Show Labels" checkbox
- Default: enabled
- Persists in UI state only (no server storage)

**FR-1.4:** Market data shall respect rate limits
- Maximum 1 market request per ship per poll cycle
- Stagger market requests with 600ms delay (same as agent polling)
- No market polling if no ships present

**FR-1.5:** Trade opportunities shall be calculated as profit margin
- Formula: `import_sell_price - export_buy_price`
- Only show opportunities with profit > 100 credits per unit
- Display format: "GOOD_NAME: +XXX credits/unit"

### 3.2 Galaxy View

**FR-2.1:** System shall fetch and display all systems in the galaxy/sector
- Use SpaceTraders API `GET /systems` endpoint
- Handle pagination to fetch all systems
- Cache systems data (refresh only on manual request or page load)

**FR-2.2:** Galaxy view shall render systems as visual points
- Each system rendered as colored circle/dot
- Size indicates number of waypoints (optional scaling)
- Color indicates presence of agent ships (has ships vs. empty)
- Systems positioned using actual x/y coordinates from API

**FR-2.3:** Galaxy view shall show ship distribution
- Display ship count per system as text label
- Color-code by agent if multiple agents active
- Show system symbol on hover

**FR-2.4:** User shall be able to toggle between system view and galaxy view
- Button/toggle in header or toolbar
- State: `viewMode: 'system' | 'galaxy'`
- Clicking a system in galaxy view switches to system detail view

**FR-2.5:** Galaxy view shall support pan and zoom controls
- Same mouse controls as system view (drag to pan, wheel to zoom)
- Center view on systems with ships initially
- Maintain zoom/pan state when switching views

**FR-2.6:** Clicking a system in galaxy view shall navigate to that system
- Updates `currentSystem` state
- Switches viewMode to 'system'
- Loads waypoints and ships for selected system
- Smooth transition (no preview popup)

### 3.3 UI/UX Enhancements

**FR-3.1:** System selector dropdown shall remain functional
- Works in both galaxy and system view
- Provides alternative navigation method

**FR-3.2:** Current system shall be visually highlighted in galaxy view
- Different color or ring indicator
- Helps orient user when switching between views

**FR-3.3:** Loading states shall be shown during data fetching
- Galaxy view: "Loading systems..."
- Market data: Spinner or indicator on waypoints

---

## 4. Technical Requirements

### 4.1 Backend Changes

**TR-1.1:** Add market data endpoint in `server/routes/systems.ts`
```typescript
// GET /api/systems/:systemSymbol/waypoints/:waypointSymbol/market
router.get('/:systemSymbol/waypoints/:waypointSymbol/market', async (req, res) => {
  // Requires agent token (pass via query param or header)
  // Fetch from SpaceTraders API: /systems/{sys}/waypoints/{wp}/market
  // Return market data
});
```

**TR-1.2:** Add galaxy systems endpoint in `server/routes/systems.ts`
```typescript
// GET /api/systems (list all systems with pagination)
router.get('/', async (req, res) => {
  // Fetch all systems from SpaceTraders API with pagination
  // Return array of systems with coordinates
});
```

**TR-1.3:** Market endpoint must use agent authentication
- Accept agentId in request (query param or body)
- Look up agent token from storage
- Use token to call SpaceTraders API
- Return market data or error

### 4.2 Frontend State Changes

**TR-2.1:** Extend `web/src/store/useStore.ts` with market state
```typescript
// Add to store
markets: Map<string, Market>, // key: waypointSymbol
setMarkets: (markets: Map<string, Market>) => void,
showMarkets: boolean,
toggleMarkets: () => void,
```

**TR-2.2:** Extend `web/src/store/useStore.ts` with galaxy state
```typescript
// Add to store
systems: System[], // all systems in galaxy
setSystems: (systems: System[]) => void,
viewMode: 'system' | 'galaxy',
setViewMode: (mode: 'system' | 'galaxy') => void,
```

**TR-2.3:** Update ship data to include current market info
- Tag ships with market data during polling if available
- Store as `ship.currentMarket?: Market`

### 4.3 API Client Changes

**TR-3.1:** Add market API functions in `web/src/services/api.ts`
```typescript
export async function getMarket(
  systemSymbol: string,
  waypointSymbol: string,
  agentId: string
): Promise<Market> {
  // Call /api/systems/:sys/waypoints/:wp/market?agentId=:id
}
```

**TR-3.2:** Add galaxy API functions in `web/src/services/api.ts`
```typescript
export async function getAllSystems(): Promise<System[]> {
  // Call /api/systems with pagination
  // Return all systems
}
```

### 4.4 Polling Service Changes

**TR-4.1:** Extend `web/src/services/polling.ts` to poll markets
```typescript
// In fetchAllShips, after fetching ships:
for (const ship of ships) {
  if (waypointHasMarket(ship.nav.waypointSymbol)) {
    const market = await getMarket(
      ship.nav.systemSymbol,
      ship.nav.waypointSymbol,
      agent.id
    );
    // Store market data
  }
  // Rate limit delay
}
```

**TR-4.2:** Market polling must respect rate limits
- Use same 600ms delay between market requests
- Limit to 1 market per ship per cycle
- Skip if already fetched in current cycle

### 4.5 New Components

**TR-5.1:** Create `web/src/components/GalaxyView.tsx`
- Similar structure to SpaceMap.tsx
- Initialize PixiJS Application
- Render systems as circles using coordinates
- Implement pan/zoom controls (reuse SpaceMap pattern)
- Handle click events to switch systems

**TR-5.2:** Create `web/src/components/MarketOverlay.tsx` (optional)
- Reusable component for market info display
- Can be used in SpaceMap for hover/click overlays
- Shows imports, exports, opportunities

**TR-5.3:** Add view mode toggle in `web/src/App.tsx`
- Button in header: "System View" / "Galaxy View"
- Conditionally render SpaceMap or GalaxyView
- Preserve state when switching

### 4.6 Type Definitions

**TR-6.1:** Add market types to `web/src/types/spacetraders.ts`
```typescript
export interface MarketTradeGood {
  symbol: string;
  name?: string;
  tradeVolume: number;
  supply: 'SCARCE' | 'LIMITED' | 'MODERATE' | 'HIGH' | 'ABUNDANT';
  purchasePrice: number;
  sellPrice: number;
}

export interface Market {
  symbol: string; // waypoint symbol
  exports: MarketTradeGood[];
  imports: MarketTradeGood[];
  exchange: MarketTradeGood[];
  transactions?: any[]; // optional
  tradeGoods?: MarketTradeGood[]; // optional full list
}

export interface TradeOpportunity {
  good: string;
  profitPerUnit: number;
  buyLocation: string;
  sellLocation: string;
}
```

**TR-6.2:** Extend Waypoint interface with market indicator
```typescript
export interface Waypoint {
  // ... existing fields
  hasMarketplace?: boolean; // derived from traits
}
```

### 4.7 Rendering Changes

**TR-7.1:** Update `web/src/components/SpaceMap.tsx` to render market indicators
- Check waypoint traits for "MARKETPLACE"
- Add visual indicator ($ icon or colored ring around waypoint)
- Show market overlay on hover or click (if showMarkets enabled)
- Overlay shows import/export counts and opportunities

**TR-7.2:** Market overlay design
```
┌─────────────────────────┐
│ WAYPOINT-SYMBOL         │
│ 🏪 Market               │
│ ↓ 5 Imports             │
│ ↑ 3 Exports             │
│                         │
│ Opportunities:          │
│ • FUEL: +120 cr/unit    │
│ • IRON_ORE: +85 cr/unit │
└─────────────────────────┘
```

**TR-7.3:** Galaxy view rendering pattern
```typescript
// For each system:
const systemGraphic = new PIXI.Graphics();
const radius = 3 + (system.waypoints.length / 10); // scale by waypoints
const color = hasShips(system) ? agentColor : 0x666666;
systemGraphic.circle(system.x, system.y, radius);
systemGraphic.fill({ color, alpha: 0.8 });
systemGraphic.eventMode = 'static';
systemGraphic.on('click', () => selectSystem(system.symbol));
```

---

## 5. Implementation Hints

### 5.1 Market Data Flow
```
Ship polling service
  → For each ship, check if waypoint has market
  → If yes, call getMarket(system, waypoint, agentId)
  → Backend proxies to SpaceTraders API with agent token
  → Market data returned, stored in Map<waypointSymbol, Market>
  → SpaceMap reads markets from store
  → Renders market indicators on waypoints
```

### 5.2 Galaxy View Flow
```
User clicks "Galaxy View" button
  → viewMode set to 'galaxy'
  → App renders GalaxyView component
  → GalaxyView fetches all systems (if not cached)
  → Renders each system as circle at (x, y)
  → Shows ship counts per system
  → User clicks system
  → Updates currentSystem state
  → viewMode set to 'system'
  → App renders SpaceMap with selected system
```

### 5.3 Rate Limit Management
```typescript
// In polling service:
const SHIP_POLL_DELAY = 600; // ms between ships
const MARKET_POLL_DELAY = 600; // ms between markets

for (const agent of agents) {
  const ships = await getAgentShips(agent.id);

  for (const ship of ships) {
    // Fetch market if applicable
    if (needsMarketData(ship)) {
      const market = await getMarket(ship.nav.systemSymbol, ship.nav.waypointSymbol, agent.id);
      // Store market
    }
    await delay(MARKET_POLL_DELAY);
  }

  await delay(SHIP_POLL_DELAY);
}
```

### 5.4 Trade Opportunity Calculation
```typescript
function calculateOpportunities(markets: Map<string, Market>): TradeOpportunity[] {
  const opportunities: TradeOpportunity[] = [];

  // For each export market
  for (const [exportWp, exportMarket] of markets) {
    for (const exportGood of exportMarket.exports) {

      // Find import markets for same good
      for (const [importWp, importMarket] of markets) {
        const importGood = importMarket.imports.find(g => g.symbol === exportGood.symbol);

        if (importGood) {
          const profit = importGood.sellPrice - exportGood.purchasePrice;

          if (profit > 100) { // threshold
            opportunities.push({
              good: exportGood.symbol,
              profitPerUnit: profit,
              buyLocation: exportWp,
              sellLocation: importWp,
            });
          }
        }
      }
    }
  }

  // Sort by profit descending
  return opportunities.sort((a, b) => b.profitPerUnit - a.profitPerUnit);
}
```

### 5.5 Galaxy View Performance
- Systems list can be large (hundreds), use efficient rendering
- Consider PixiJS ParticleContainer for system dots if > 1000 systems
- Implement viewport culling if performance issues arise
- Cache system coordinates, only re-render on zoom/pan

---

## 6. Acceptance Criteria

### 6.1 Market Data Visualization
- [ ] Market data is fetched for waypoints where ships are located
- [ ] Waypoints with markets show visual indicator (icon or colored ring)
- [ ] Hovering/clicking market waypoint shows overlay with:
  - [ ] Import count
  - [ ] Export count
  - [ ] Top 2 trade opportunities (if any)
- [ ] Market overlays can be toggled on/off
- [ ] Market polling respects rate limits (no 429 errors)
- [ ] Trade opportunities show profit per unit correctly

### 6.2 Galaxy View
- [ ] User can toggle between System View and Galaxy View
- [ ] Galaxy View shows all systems as colored dots
- [ ] System positions match SpaceTraders API coordinates
- [ ] Systems with agent ships are highlighted with different color
- [ ] Ship counts displayed on systems (if ships present)
- [ ] User can pan and zoom galaxy view
- [ ] Clicking a system switches to detailed system view
- [ ] Current system is highlighted in galaxy view

### 6.3 UX/Performance
- [ ] Switching between views is smooth (< 500ms)
- [ ] Loading states shown during data fetching
- [ ] No console errors or warnings
- [ ] Rate limits respected (no API throttling)
- [ ] UI remains responsive with 10+ ships and 100+ systems

---

## 7. Out of Scope

The following features were considered but explicitly excluded:

- **Historical data tracking** - No persistence of past ship positions, market prices, or profits
- **Ship command controls** - No ability to send navigate/dock/trade commands
- **Alerts and notifications system** - No low fuel warnings, cargo alerts, or market opportunity notifications
- **User-configurable alert rules** - N/A (alerts removed)
- **Jump gate route visualization** - Galaxy view shows systems as points only, no connection lines
- **Full market data display** - Only summary shown, not all 20+ goods
- **Market preview popups** - Direct navigation, no intermediate popups
- **Cached market data** - Only real-time data for ships' locations shown

---

## 8. Assumptions

1. **SpaceTraders API access** - Market data endpoint works as documented
2. **Galaxy size** - Fewer than 1000 systems (if more, may need optimization)
3. **Market availability** - Waypoints with MARKETPLACE trait have accessible market data
4. **Rate limits** - Current stagger strategy (600ms) sufficient for markets
5. **Browser support** - Modern browsers with PixiJS WebGL support
6. **No authentication changes** - Existing agent token storage pattern sufficient
7. **Market data structure** - SpaceTraders API returns exports/imports/exchange arrays
8. **System coordinates** - All systems have valid x/y coordinates
9. **No mobile optimization required** - Desktop-first (existing assumption)

---

## 9. Future Enhancements (Not in Scope)

Features that could be added later:

- Historical market price tracking and trend charts
- Optimal trade route calculation and visualization
- Multi-hop route planning (system A → B → C)
- Market data caching with staleness indicators
- Jump gate route visualization in galaxy view
- System-to-system distance calculations
- Profitability analytics per agent
- Export market data to CSV
- Configurable alert thresholds
- Browser notifications for events

---

## 10. File Change Summary

### New Files (8)
1. `server/routes/markets.ts` (optional, can extend systems.ts)
2. `web/src/types/markets.ts` (optional, can extend spacetraders.ts)
3. `web/src/components/GalaxyView.tsx`
4. `web/src/components/MarketOverlay.tsx` (optional, can inline in SpaceMap)
5. `web/src/services/marketAnalysis.ts`

### Modified Files (8)
1. `server/index.ts` - Add market routes (if new file created)
2. `server/routes/systems.ts` - Add market and all-systems endpoints
3. `web/src/store/useStore.ts` - Add market and galaxy state
4. `web/src/services/api.ts` - Add market and galaxy API functions
5. `web/src/services/polling.ts` - Add market polling logic
6. `web/src/components/SpaceMap.tsx` - Add market visualization
7. `web/src/App.tsx` - Add view mode toggle and GalaxyView
8. `web/src/types/spacetraders.ts` - Add Market interfaces

### Total Estimated Changes
- **New components:** 2 (GalaxyView, optional MarketOverlay)
- **Backend endpoints:** 2-3 (market data, all systems)
- **State additions:** ~10 new properties/actions
- **API functions:** ~3 new functions
- **Lines of code:** ~800-1000 new, ~200-300 modified

---

## 11. Implementation Order

Recommended implementation sequence:

### Phase 1: Backend Market Support
1. Add market endpoint to `server/routes/systems.ts`
2. Add all-systems endpoint
3. Test endpoints with Postman/curl

### Phase 2: Frontend Market State
1. Add market types to `types/spacetraders.ts`
2. Add market state to `useStore.ts`
3. Add market API functions to `api.ts`
4. Extend polling service to fetch markets

### Phase 3: Market Visualization
1. Update SpaceMap to detect marketplace waypoints
2. Add market indicators to waypoints
3. Add market overlay UI (hover/click)
4. Add toggle for showing markets
5. Implement trade opportunity calculation

### Phase 4: Galaxy View
1. Add galaxy state to `useStore.ts`
2. Add galaxy API functions to `api.ts`
3. Create GalaxyView component with PixiJS
4. Render systems as points
5. Implement pan/zoom controls
6. Add click-to-navigate functionality

### Phase 5: Integration
1. Add view mode toggle to App
2. Connect GalaxyView to state
3. Test switching between views
4. Polish transitions and loading states

### Phase 6: Testing & Polish
1. Test with multiple agents
2. Verify rate limit compliance
3. Test with large system counts
4. Fix any bugs or performance issues
5. Update CLAUDE.md documentation

---

**END OF REQUIREMENTS SPECIFICATION**
