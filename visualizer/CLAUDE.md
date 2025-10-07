# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

SpaceTraders Fleet Visualization is a real-time web application for visualizing agent ship movements across star systems in the SpaceTraders game. It consists of a React frontend with Konva.js + Canvas 2D API for canvas rendering and an Express backend that proxies requests to the SpaceTraders API.

## Commands

### Backend (server/)

```bash
cd server
npm install            # Install dependencies
npm run build          # Compile TypeScript to build/ directory
npm start              # Run compiled server on port 4000 (or PORT env var)
npm run dev            # Watch mode: recompile on changes + auto-restart
```

### Frontend (web/)

```bash
cd web
npm install            # Install dependencies
npm run dev            # Start Vite dev server on port 3000 with HMR
npm run build          # Type-check with tsc and build for production
npm run preview        # Preview production build
```

### Development Workflow

1. Start backend in one terminal: `cd server && npm run dev`
2. Start frontend in another terminal: `cd web && npm run dev`
3. Backend runs on http://localhost:4000, frontend on http://localhost:3000

## Architecture

### Technology Stack

**Frontend:**
- React 18 with TypeScript
- Konva.js + Canvas 2D API for 2D canvas rendering (star maps, ships, waypoints)
- Zustand for state management (global store pattern)
- Tailwind CSS for styling
- Vite as build tool

**Backend:**
- Express.js with TypeScript
- JSON file storage (server/db/agents.json) for agent tokens
- Proxies all SpaceTraders API requests to keep tokens server-side

### Code Structure

```
server/
├── index.ts              # Express app entry point, CORS, routes
├── routes/
│   ├── agents.ts        # CRUD for agents, GET /api/agents/:id/ships
│   └── systems.ts       # GET system details, waypoints
└── db/
    └── storage.ts       # JSON file DB (agents.json), CRUD operations

web/src/
├── App.tsx              # Main component, layout
├── main.tsx             # React entry point
├── types/
│   └── spacetraders.ts  # TypeScript interfaces for API types
├── constants/
│   ├── canvas.ts        # Canvas constants (colors, zoom levels, etc.)
│   └── api.ts           # API configuration (intervals, limits)
├── store/
│   └── useStore.ts      # Zustand store (agents, ships, waypoints, UI state)
├── services/
│   ├── api/             # Modular API client
│   │   ├── client.ts    # HTTP client with error handling
│   │   ├── agents.ts    # Agent endpoints
│   │   ├── systems.ts   # System/waypoint endpoints
│   │   └── index.ts     # Barrel exports
│   ├── canvas/
│   │   ├── WaypointRenderer.ts  # Waypoint rendering using Canvas 2D API
│   │   └── ShipRenderer.ts      # Ship rendering logic by role using Canvas 2D API
│   ├── polling.ts       # ShipPollingService class
│   └── marketAnalysis.ts
├── utils/
│   ├── colors.ts        # Color manipulation functions
│   ├── pagination.ts    # Reusable pagination logic
│   └── shipPosition.ts  # Ship position calculations
├── hooks/
│   ├── usePolling.ts    # Polling lifecycle management
│   └── useKonvaStage.ts # Reusable Konva stage setup with pan/zoom
└── components/
    ├── SpaceMap.tsx     # Konva canvas, renders ships & waypoints
    ├── GalaxyView.tsx   # Galaxy-wide visualization
    ├── SystemSelector.tsx
    ├── ShipFilters.tsx
    ├── AgentManager.tsx
    └── AddAgentCard.tsx
```

### Key Architectural Patterns

**State Management:**
- Zustand store (web/src/store/useStore.ts) is the single source of truth
- Store contains: agents, ships, waypoints, currentSystem, UI filters, polling status
- Components consume state via `useStore()` hook
- State updates trigger React re-renders automatically

**Polling Architecture:**
- `ShipPollingService` (web/src/services/polling.ts) fetches ship data every 5 seconds
- Staggers requests between agents with 600ms delay to respect rate limits
- `usePolling` hook manages service lifecycle (start/stop on mount/unmount)
- Updates flow: API → polling service → Zustand store → React components

**Backend Proxy Pattern:**
- All SpaceTraders API calls go through the backend
- Tokens stored server-side in agents.json, never exposed to browser
- Backend validates tokens on agent creation by calling `/my/agent`
- Client code in server/src/client.ts is reused from parent MCP server

**Konva.js Rendering (Hybrid Approach):**
- react-konva provides declarative React components for Stage/Layer management
- `useKonvaStage` hook provides reusable Konva stage setup with pan/zoom (optional)
- Used by both SpaceMap (system view) and GalaxyView (galaxy view)
- GalaxyView uses declarative Konva components (Circle, Text) for simple rendering
- SpaceMap uses hybrid approach: Konva Stage/Layer + Canvas 2D API via sceneFunc
- Waypoints rendered using Canvas 2D API (`services/canvas/WaypointRenderer.ts`)
- Ships rendered using Canvas 2D API (`services/canvas/ShipRenderer.ts`) with role-specific designs
- Real-time ship interpolation: Konva.Animation triggers position recalculation each frame
- Pan via `draggable` prop on Stage, zoom via `onWheel` handler with scale/position math

**Ship Position Interpolation:**
- Logic centralized in `utils/shipPosition.ts` - `interpolateShipPosition()` function
- DOCKED ships: position = waypoint coordinates
- IN_ORBIT ships: circular orbit animation around waypoint using orbital physics
- IN_TRANSIT ships: linear interpolation between origin and destination
- Progress = (now - departureTime) / (arrivalTime - departureTime)
- Position recalculated every frame via Konva.Animation for smooth movement

**Agent Management:**
- Each agent has: id, token (server-only), symbol, color, visible flag
- Colors assigned from predefined palette on creation
- Visible agents are polled for ship data
- Frontend receives sanitized agent data (tokens stripped)

### SpaceTraders API Integration

**Rate Limits:**
- 2 requests/second sustained
- 10 request burst over 10 seconds
- Polling service staggers agent requests with 600ms delay

**Key Endpoints Used:**
- `GET /my/agent` - Validate token, get agent symbol
- `GET /my/ships` - Fetch all ships for an agent
- `GET /systems/:symbol` - System details
- `GET /systems/:symbol/waypoints` - All waypoints in system

**Ship Navigation States:**
- `IN_TRANSIT`: Ship moving between waypoints (has route with origin, destination, times)
- `IN_ORBIT`: Ship orbiting a waypoint
- `DOCKED`: Ship docked at a waypoint

### Data Flow

```
User adds agent token
  → Backend validates via SpaceTraders API
  → Token stored in agents.json, agent returned (sanitized)
  → Frontend stores agent in Zustand
  → Polling service starts fetching ships
  → Ships tagged with agentId & agentColor
  → Ships stored in Zustand
  → SpaceMap renders ships via Konva + Canvas 2D API
  → Konva.Animation animates IN_TRANSIT ships in real-time
```

## Common Development Tasks

**Adding a new API endpoint:**
1. Add route handler in `server/routes/agents.ts` or `server/routes/systems.ts`
2. Add API client function in `web/src/services/api/agents.ts` or `systems.ts`
3. Export from `web/src/services/api/index.ts` barrel
4. Use in React component or hook via `import { functionName } from '../services/api'`

**Adding new ship rendering:**
1. Update Ship interface in `web/src/types/spacetraders.ts` if needed
2. Add ship role rendering function to `services/canvas/ShipRenderer.ts`
3. Update `drawShipShape()` switch statement to handle new role
4. Use color utilities from `utils/colors.ts` for consistent styling (supports both hex numbers and CSS strings)

**Modifying polling behavior:**
- Polling interval: Change `API_CONSTANTS.POLL_INTERVAL` in `constants/api.ts`
- Rate limiting: Change `API_CONSTANTS.REQUEST_DELAY` in `constants/api.ts`
- Never reduce below 500ms to avoid SpaceTraders rate limits

**Changing Konva rendering:**
- Waypoint rendering: `services/canvas/WaypointRenderer.ts` - `drawWaypoint()` function using Canvas 2D API
- Ship rendering: `services/canvas/ShipRenderer.ts` - `drawShipShape()` and role-specific functions using Canvas 2D API
- Colors: `constants/canvas.ts` - `WAYPOINT_COLORS` object
- Zoom/scale: `constants/canvas.ts` - `MIN_ZOOM_*`, `MAX_ZOOM_*` constants
- Canvas setup: SpaceMap and GalaxyView use react-konva Stage/Layer with pan/zoom handlers

**Adding new utilities:**
- Colors: `utils/colors.ts` - `lightenColor()`, `darkenColor()`
- Pagination: `utils/pagination.ts` - `fetchAllPaginated()` for API calls
- Ship positions: `utils/shipPosition.ts` - position calculations

**Modifying filters:**
- Filter state: `store/useStore.ts` - filter state and toggle functions
- Filter UI: `components/ShipFilters.tsx`
- Filter logic: `components/SpaceMap.tsx` - ships.filter() logic in render effect

## Important Notes

**Security:**
- Agent tokens must NEVER be sent to the frontend
- Always destructure `{ token, ...sanitized }` before sending agent data
- agents.json should be encrypted in production or replaced with a database

**PixiJS Gotchas:**
- Graphics are not automatically garbage collected, manually call `container.removeChild()`
- Use labels on graphics objects to identify and remove specific types (e.g., `label: 'ship'`)
- `container.pivot` sets the point that the container rotates/scales around
- Call `app.destroy(true)` in cleanup to prevent memory leaks

**Type Safety:**
- Ship interface (`types/spacetraders.ts`) matches SpaceTraders API v2 schema
- Ships are tagged with `agentId` and `agentColor` at runtime (not in API response)
- `TaggedShip` interface extends `Ship` for proper typing of runtime-tagged ships
- API responses use generic `ApiResponse<T>` type with optional pagination metadata

**Zustand Store:**
- `waypoints` is a Map for O(1) lookup by symbol
- `trails` is a Map that stores last 5 positions per ship
- Filter sets (filterStatus, filterAgents) use Set for efficient membership checks
- Store updates are shallow merges, use spread operators to update nested data

**Rate Limiting:**
- Never reduce AGENT_DELAY below 500ms to avoid hitting SpaceTraders rate limits
- Consider implementing exponential backoff if API returns 429 errors

**Testing with Real Data:**
- Requires valid SpaceTraders agent token(s) from https://spacetraders.io
- Backend must be able to reach api.spacetraders.io
- Ships will only appear if they exist in the selected system

## Code Organization Principles

The codebase follows SOLID principles with these key patterns:

**Single Responsibility:**
- Each service has one purpose: `ShipRenderer` only renders ships, `pagination` only handles pagination
- Utilities are pure functions without side effects
- Components focus on UI, services handle business logic

**Separation of Concerns:**
- Constants extracted to dedicated files (`constants/pixi.ts`, `constants/api.ts`)
- API logic split by domain (`api/agents.ts`, `api/systems.ts`)
- Rendering logic isolated in service layer (`services/pixi/`)
- Reusable logic in `utils/` (colors, pagination, ship positions)

**DRY (Don't Repeat Yourself):**
- `usePixiCanvas` hook eliminates duplicate canvas setup (~100 lines saved)
- `fetchAllPaginated` utility handles all paginated API calls
- `ShipRenderer` service centralizes ship rendering logic (~500 lines extracted)
- Color manipulation functions in `utils/colors.ts` used across codebase

**Dependency Inversion:**
- Components depend on abstractions (hooks, services) not implementations
- `usePolling` hook abstracts polling service lifecycle
- `usePixiCanvas` hook abstracts PixiJS setup details
