# PixiJS → Konva Migration Guide

**Status:** Phase 1 Complete ✅ (Infrastructure Ready)
**Backup:** `web-backup-20251004-002701.tar.gz` (74 KB)

---

## Table of Contents
1. [Migration Overview](#migration-overview)
2. [Completed Work](#completed-work)
3. [Architecture Strategy](#architecture-strategy)
4. [Remaining Tasks](#remaining-tasks)
5. [Phase-by-Phase Guide](#phase-by-phase-guide)
6. [Code Examples](#code-examples)
7. [Testing Checklist](#testing-checklist)
8. [Rollback Plan](#rollback-plan)

---

## Migration Overview

### Why Migrate?
- **Better React Integration**: Declarative components vs imperative PixiJS
- **Simpler DX**: ~60% less boilerplate code
- **Sufficient Performance**: 500 ships well within Konva's 3k object limit
- **Lower Learning Curve**: More intuitive API for team members

### Scope
- **2,760 lines** of PixiJS code across 5 files
- **15+ planet types** with detailed procedural rendering
- **8 ship designs** with role-specific artwork
- Real-time animations (ships, lasers, orbital physics)
- Complex interactions (pan, zoom, selection, tooltips)

### Hybrid Approach
- **react-konva**: Component structure (Stage, Layer, Group)
- **Canvas 2D API via sceneFunc**: Complex graphics (planets, ships)
- **Konva.Animation**: Real-time ship movement

---

## Completed Work

### ✅ Phase 1: Infrastructure (3/23 tasks)

1. **Dependencies Installed**
   ```json
   {
     "konva": "^10.0.2",
     "react-konva": "^19.0.10"
   }
   ```

2. **Constants Refactored**
   - Renamed: `constants/pixi.ts` → `constants/canvas.ts`
   - Updated: `PIXI_CONSTANTS` → `CANVAS_CONSTANTS`
   - Fixed 4 import references across codebase

3. **Hook Created: `hooks/useKonvaStage.ts`**
   - Drop-in replacement for `usePixiCanvas`
   - Features: pan (drag), zoom (wheel), event handling
   - API-compatible layer positioning

---

## Architecture Strategy

### File Structure (Post-Migration)

```
web/src/
├── constants/
│   └── canvas.ts              # ✅ Renamed, all imports updated
├── hooks/
│   ├── useKonvaStage.ts       # ✅ New - replaces usePixiCanvas
│   └── usePixiCanvas.ts       # 🗑️ Delete after migration
├── services/
│   ├── canvas/                # 🆕 Create this directory
│   │   ├── WaypointRenderer.ts  # Planet/waypoint rendering (Canvas 2D)
│   │   └── ShipRenderer.ts      # Ship rendering (Canvas 2D)
│   └── pixi/                  # 🗑️ Delete after migration
│       └── ShipRenderer.ts
├── components/
│   ├── GalaxyView.tsx         # 🔄 Refactor to react-konva
│   └── SpaceMap.tsx           # 🔄 Major refactor (1920 lines)
└── utils/
    └── shipPosition.ts        # ✅ Already updated
```

### Rendering Strategy

**Current (PixiJS):**
```typescript
// Imperative - manual Graphics API
const graphics = new PIXI.Graphics();
graphics.circle(x, y, radius);
graphics.fill({ color: 0xff0000 });
container.addChild(graphics);
```

**Future (Konva Hybrid):**
```tsx
// Declarative components + Canvas 2D for complex shapes
<Shape
  sceneFunc={(context, shape) => {
    drawPlanetWithCanvas2D(context, planet, x, y, radius);
  }}
  x={planet.x}
  y={planet.y}
/>
```

**Why Hybrid?**
- Preserve all 537 lines of planet rendering code
- Maintain visual quality (no rewrites)
- Get React benefits (declarative, component lifecycle)

---

## Remaining Tasks

### Phase 2: Canvas Renderers (2 tasks)
4. [ ] Create `services/canvas/WaypointRenderer.ts`
5. [ ] Create `services/canvas/ShipRenderer.ts`

### Phase 3: GalaxyView Migration (2 tasks)
6. [ ] Refactor GalaxyView to react-konva
7. [ ] Test: pan, zoom, system selection

### Phase 4: SpaceMap Core (3 tasks)
8. [ ] Refactor SpaceMap structure
9. [ ] Migrate waypoint rendering
10. [ ] Migrate ship rendering

### Phase 5: Visual Features (3 tasks)
11. [ ] Migrate trails & destination lines
12. [ ] Migrate labels & text
13. [ ] Migrate selection markers

### Phase 6: Animations (3 tasks)
14. [ ] Ship position animation
15. [ ] Mining laser pulsing
16. [ ] Orbital movement

### Phase 7: Interactions (2 tasks)
17. [ ] Pan/zoom controls
18. [ ] Ship/waypoint selection

### Phase 8: Integration (1 task)
19. [ ] Minimap integration

### Phase 9: Testing & Cleanup (4 tasks)
20. [ ] Full feature testing
21. [ ] Remove PixiJS dependency
22. [ ] Delete obsolete files
23. [ ] Update CLAUDE.md

---

## Phase-by-Phase Guide

### Phase 2: Canvas Renderers

#### Task 4: WaypointRenderer.ts

**Location:** `web/src/services/canvas/WaypointRenderer.ts`

**Purpose:** Convert planet rendering from PixiJS Graphics → Canvas 2D API

**Migration Steps:**

1. Copy `drawWaypoint()` from `SpaceMap.tsx:14-527`
2. Replace PixiJS calls with Canvas 2D:
   - `graphics.circle(x, y, r)` → `context.arc(x, y, r, 0, Math.PI * 2)`
   - `graphics.fill({ color })` → `context.fillStyle = '#...'; context.fill()`
   - `graphics.stroke({ color, width })` → `context.strokeStyle = '#...'; context.lineWidth = ...; context.stroke()`
3. Keep all 15+ planet type logic intact

**Example:**

```typescript
// services/canvas/WaypointRenderer.ts
import type { Waypoint } from '../../types/spacetraders';

export function drawWaypoint(
  context: CanvasRenderingContext2D,
  waypoint: Waypoint,
  x: number,
  y: number,
  radius: number
): void {
  const type = waypoint.type;

  switch (type) {
    case 'PLANET':
      const hash = (x * 73856093) ^ (y * 19349663);
      const planetType = Math.abs(hash) % 6;

      if (planetType === 0) {
        // Earth-like planet
        context.beginPath();
        context.arc(x, y, radius, 0, Math.PI * 2);
        context.fillStyle = '#2980b9';
        context.fill();

        // Continents
        context.beginPath();
        context.arc(x - radius * 0.3, y - radius * 0.2, radius * 0.4, 0, Math.PI * 2);
        context.fillStyle = 'rgba(39, 174, 96, 0.85)';
        context.fill();

        // ... copy all planet rendering logic
      }
      // ... other planet types
      break;

    case 'GAS_GIANT':
      // ... copy from SpaceMap
      break;

    // ... all other types
  }
}
```

#### Task 5: ShipRenderer.ts

**Location:** `web/src/services/canvas/ShipRenderer.ts`

**Purpose:** Convert ship rendering from PixiJS Graphics → Canvas 2D API

**Migration Steps:**

1. Copy from `services/pixi/ShipRenderer.ts`
2. Replace PIXI.Graphics parameter with CanvasRenderingContext2D
3. Convert all drawing commands to Canvas 2D
4. Keep all 8 ship role designs intact

**Example:**

```typescript
// services/canvas/ShipRenderer.ts
import { lightenColor, darkenColor } from '../../utils/colors';
import { CANVAS_CONSTANTS, ENGINE_GLOW_COLOR, WINDOW_LIGHT_COLOR } from '../../constants/canvas';

export function drawShipShape(
  context: CanvasRenderingContext2D,
  role: string,
  color: number
): void {
  const alpha = 0.95;
  const scale = CANVAS_CONSTANTS.SHIP_SCALE;
  const strokeWidth = CANVAS_CONSTANTS.STROKE_WIDTH;

  // Convert hex color to CSS
  const cssColor = `#${color.toString(16).padStart(6, '0')}`;

  switch (role.toUpperCase()) {
    case 'COMMAND':
      drawCommandShip(context, cssColor, alpha, scale, strokeWidth);
      break;
    // ... other cases
  }
}

function drawCommandShip(
  context: CanvasRenderingContext2D,
  color: string,
  alpha: number,
  scale: number,
  strokeWidth: number
): void {
  // Main hull
  context.beginPath();
  context.moveTo(0, -8 * scale);
  context.lineTo(-2 * scale, -6 * scale);
  context.lineTo(-4 * scale, 2 * scale);
  context.lineTo(-3 * scale, 6 * scale);
  context.lineTo(3 * scale, 6 * scale);
  context.lineTo(4 * scale, 2 * scale);
  context.lineTo(2 * scale, -6 * scale);
  context.closePath();
  context.fillStyle = darkenColor(color, 30);
  context.globalAlpha = alpha;
  context.fill();

  // ... rest of ship drawing
}
```

---

### Phase 3: GalaxyView Migration

#### Task 6: Refactor GalaxyView

**File:** `components/GalaxyView.tsx`

**Current:** Imperative PixiJS (202 lines)
**Target:** Declarative react-konva (~80 lines)

**Migration Steps:**

1. Replace imports:
   ```diff
   - import * as PIXI from 'pixi.js';
   - import { usePixiCanvas } from '../hooks/usePixiCanvas';
   + import { Stage, Layer, Circle, Text } from 'react-konva';
   ```

2. Replace hook:
   ```diff
   - const { app, container } = usePixiCanvas(canvasRef, { ... });
   + // react-konva handles this via Stage component
   ```

3. Replace imperative rendering with JSX:

**Before:**
```typescript
container.children.forEach(child => container.removeChild(child));
const graphics = new PIXI.Graphics();
systems.forEach(system => {
  graphics.circle(system.x, system.y, radius);
  graphics.fill({ color, alpha });
});
container.addChild(graphics);
```

**After:**
```tsx
<Stage width={width} height={height}>
  <Layer>
    {systems.map(system => (
      <Circle
        key={system.symbol}
        x={system.x}
        y={system.y}
        radius={radius}
        fill={color}
        opacity={alpha}
        onClick={() => handleSystemClick(system)}
      />
    ))}
  </Layer>
</Stage>
```

**Complete Example:**

```tsx
// components/GalaxyView.tsx (refactored)
import { Stage, Layer, Circle, Text, Group } from 'react-konva';
import { useStore } from '../store/useStore';

const GalaxyView = () => {
  const { systems, ships, currentSystem, setCurrentSystem, setViewMode } = useStore();
  const [scale, setScale] = useState(0.5);
  const [position, setPosition] = useState({ x: 0, y: 0 });

  // Count ships per system
  const shipCounts = new Map<string, number>();
  ships.forEach(ship => {
    const count = shipCounts.get(ship.nav.systemSymbol) || 0;
    shipCounts.set(ship.nav.systemSymbol, count + 1);
  });

  const handleWheel = (e: any) => {
    e.evt.preventDefault();
    const scaleBy = e.evt.deltaY > 0 ? 0.9 : 1.1;
    setScale(scale * scaleBy);
  };

  return (
    <div className="relative w-full h-full">
      <Stage
        width={window.innerWidth - 256}
        height={window.innerHeight - 64}
        draggable
        onWheel={handleWheel}
        scaleX={scale}
        scaleY={scale}
        x={position.x}
        y={position.y}
        onDragEnd={(e) => setPosition({ x: e.target.x(), y: e.target.y() })}
      >
        <Layer>
          {systems.map(system => {
            const shipCount = shipCounts.get(system.symbol) || 0;
            const hasShips = shipCount > 0;
            const radius = 2 + Math.min(system.waypoints.length / 20, 5);
            const color = hasShips ? '#4ECDC4' : '#666666';

            return (
              <Group key={system.symbol}>
                {/* Highlight if current */}
                {system.symbol === currentSystem && (
                  <Circle
                    x={system.x}
                    y={system.y}
                    radius={radius + 4}
                    stroke="#FFE66D"
                    strokeWidth={2}
                    opacity={0.8}
                  />
                )}

                {/* System circle */}
                <Circle
                  x={system.x}
                  y={system.y}
                  radius={radius}
                  fill={color}
                  opacity={hasShips ? 0.9 : 0.5}
                  onClick={() => {
                    setCurrentSystem(system.symbol);
                    setViewMode('system');
                  }}
                />

                {/* Ship count */}
                {hasShips && (
                  <Text
                    text={shipCount.toString()}
                    x={system.x}
                    y={system.y}
                    fontSize={12}
                    fill="white"
                    fontStyle="bold"
                    offsetX={5}
                    offsetY={6}
                  />
                )}
              </Group>
            );
          })}
        </Layer>
      </Stage>

      {/* Keep existing UI overlays */}
    </div>
  );
};
```

---

### Phase 4: SpaceMap Core

#### Task 8: Refactor SpaceMap Structure

**Challenge:** SpaceMap is the most complex component (1,920 lines)

**Strategy:** Incremental refactor in sub-phases

**Sub-Phase 4a: Basic Structure**

Replace PixiJS Application with Konva Stage:

```tsx
// components/SpaceMap.tsx
import { Stage, Layer, Group, Shape } from 'react-konva';
import { drawWaypoint } from '../services/canvas/WaypointRenderer';
import { drawShipShape } from '../services/canvas/ShipRenderer';

const SpaceMap = forwardRef<SpaceMapRef>((props, ref) => {
  const stageRef = useRef<Konva.Stage>(null);
  const layerRef = useRef<Konva.Layer>(null);

  // ... existing state and hooks

  return (
    <div className="relative w-full h-full">
      <Stage
        ref={stageRef}
        width={window.innerWidth - 256}
        height={window.innerHeight - 64}
        draggable
        onWheel={handleWheel}
      >
        <Layer ref={layerRef}>
          {/* Waypoints */}
          {Array.from(waypoints.values()).map(waypoint => (
            <WaypointShape key={waypoint.symbol} waypoint={waypoint} />
          ))}

          {/* Ships */}
          {ships.map(ship => (
            <ShipShape key={ship.symbol} ship={ship} />
          ))}
        </Layer>
      </Stage>
    </div>
  );
});
```

#### Task 9: Waypoint Rendering

**Create WaypointShape component:**

```tsx
// components/SpaceMap.tsx (or separate file)
interface WaypointShapeProps {
  waypoint: Waypoint;
}

const WaypointShape: React.FC<WaypointShapeProps> = ({ waypoint }) => {
  const radius = getWaypointRadius(waypoint);
  const { showMarkets } = useStore();

  return (
    <Group>
      {/* Main waypoint shape using sceneFunc */}
      <Shape
        sceneFunc={(context, shape) => {
          drawWaypoint(context, waypoint, 0, 0, radius);
        }}
        x={waypoint.x}
        y={waypoint.y}
        onClick={() => handleWaypointClick(waypoint)}
        onMouseEnter={() => setHoveredWaypoint(waypoint.symbol)}
        onMouseLeave={() => setHoveredWaypoint(null)}
      />

      {/* Marketplace indicator */}
      {showMarkets && hasMarketplace(waypoint) && (
        <Circle
          x={waypoint.x}
          y={waypoint.y}
          radius={radius + 4}
          stroke="#f39c12"
          strokeWidth={1}
          opacity={0.6}
        />
      )}

      {/* Label */}
      <Text
        text={waypoint.symbol.split('-').pop() || ''}
        x={waypoint.x + radius + 2}
        y={waypoint.y - 5}
        fontSize={10}
        fill="white"
        opacity={0.6}
      />
    </Group>
  );
};
```

#### Task 10: Ship Rendering

**Create ShipShape component:**

```tsx
interface ShipShapeProps {
  ship: Ship;
}

const ShipShape: React.FC<ShipShapeProps> = ({ ship }) => {
  const { waypoints } = useStore();
  const position = interpolateShipPosition(ship, waypoints);
  const shipColor = ship.agentColor ? parseInt(ship.agentColor.replace('#', ''), 16) : 0xff6b6b;

  // Calculate rotation
  const rotation = useMemo(() => {
    if (ship.nav.status === 'IN_TRANSIT' && ship.nav.route?.destination) {
      const dest = ship.nav.route.destination;
      const angle = Math.atan2(dest.y - position.y, dest.x - position.x);
      return angle + Math.PI / 2;
    }
    return 0;
  }, [ship, position]);

  return (
    <Group x={position.x} y={position.y} rotation={(rotation * 180) / Math.PI}>
      {/* Ship shape using sceneFunc */}
      <Shape
        sceneFunc={(context, shape) => {
          drawShipShape(context, ship.registration.role, shipColor);
        }}
        onClick={() => handleShipClick(ship)}
      />
    </Group>
  );
};
```

---

### Phase 5: Visual Features

#### Task 11: Trails & Lines

```tsx
// Ship trail component
const ShipTrail: React.FC<{ ship: Ship }> = ({ ship }) => {
  const { trails } = useStore();
  const trail = trails.get(ship.symbol) || [];

  if (trail.length < 2) return null;

  return (
    <Shape
      sceneFunc={(context, shape) => {
        for (let i = 0; i < trail.length - 1; i++) {
          const alpha = (i + 1) / trail.length;
          const width = 1 + (i / trail.length) * 1.5;

          context.strokeStyle = ship.agentColor;
          context.globalAlpha = alpha * 0.4;
          context.lineWidth = width;
          context.beginPath();
          context.moveTo(trail[i].x, trail[i].y);
          context.lineTo(trail[i + 1].x, trail[i + 1].y);
          context.stroke();
        }
      }}
    />
  );
};
```

#### Task 12: Labels & Text

```tsx
// Ship label component
const ShipLabel: React.FC<{ ship: Ship; position: { x: number; y: number } }> = ({ ship, position }) => {
  const { showLabels } = useStore();

  if (!showLabels) return null;

  return (
    <Group x={position.x + 8} y={position.y - 22}>
      {/* Background */}
      <Rect
        width={100}
        height={60}
        fill="#1a1a1a"
        opacity={0.85}
        stroke={ship.agentColor}
        strokeWidth={1}
      />

      {/* Ship name */}
      <Text
        text={ship.symbol}
        x={4}
        y={4}
        fontSize={8}
        fill="white"
        fontStyle="bold"
      />

      {/* Status */}
      <Text
        text={ship.nav.status}
        x={4}
        y={16}
        fontSize={7}
        fill="#88ccff"
      />

      {/* ... more labels */}
    </Group>
  );
};
```

---

### Phase 6: Animations

#### Task 14: Ship Position Animation

**Use Konva.Animation:**

```tsx
const ShipShape: React.FC<ShipShapeProps> = ({ ship }) => {
  const groupRef = useRef<Konva.Group>(null);
  const { waypoints } = useStore();

  useEffect(() => {
    if (!groupRef.current) return;

    const anim = new Konva.Animation(() => {
      const position = interpolateShipPosition(ship, waypoints);
      groupRef.current?.position(position);
    }, groupRef.current.getLayer());

    anim.start();
    return () => anim.stop();
  }, [ship, waypoints]);

  return (
    <Group ref={groupRef}>
      {/* Ship shape */}
    </Group>
  );
};
```

#### Task 15: Mining Laser Animation

```tsx
const MiningLaser: React.FC<{ ship: Ship; waypoint: Waypoint }> = ({ ship, waypoint }) => {
  const [phase, setPhase] = useState(0);

  useEffect(() => {
    const anim = new Konva.Animation((frame) => {
      setPhase((frame.time / 1000) % 1); // 1-second cycle
    });
    anim.start();
    return () => anim.stop();
  }, []);

  const alpha = 0.5 + Math.sin(phase * Math.PI * 2) * 0.4;

  return (
    <Line
      points={[ship.x, ship.y, waypoint.x, waypoint.y]}
      stroke="#ff0000"
      strokeWidth={0.3}
      opacity={alpha}
    />
  );
};
```

---

### Phase 7: Interactions

#### Task 17: Pan/Zoom

**Already handled by react-konva Stage:**

```tsx
<Stage
  draggable  // Pan via drag
  onWheel={(e) => {
    e.evt.preventDefault();
    const stage = e.target.getStage();
    const oldScale = stage.scaleX();

    const pointer = stage.getPointerPosition();
    const mousePointTo = {
      x: (pointer.x - stage.x()) / oldScale,
      y: (pointer.y - stage.y()) / oldScale,
    };

    const newScale = e.evt.deltaY > 0 ? oldScale * 0.9 : oldScale * 1.1;
    stage.scale({ x: newScale, y: newScale });

    const newPos = {
      x: pointer.x - mousePointTo.x * newScale,
      y: pointer.y - mousePointTo.y * newScale,
    };
    stage.position(newPos);
    stage.batchDraw();
  }}
/>
```

#### Task 18: Selection

```tsx
const [selectedObject, setSelectedObject] = useState<{type: string, symbol: string} | null>(null);

// In WaypointShape:
<Shape
  onClick={() => {
    setSelectedObject({ type: 'waypoint', symbol: waypoint.symbol });
    setSelectedWaypoint(waypoint);
  }}
/>

// Selection marker:
{selectedObject && (
  <Group x={selectedObject.x} y={selectedObject.y}>
    <Circle
      radius={15}
      stroke="#00ff00"
      strokeWidth={2}
    />
    {/* Corner brackets */}
    <Line points={[-19, -19, -13, -19, -19, -13]} stroke="#00ff00" strokeWidth={2} />
    {/* ... other corners */}
  </Group>
)}
```

---

## Code Examples

### Color Conversion Utility

PixiJS uses hex numbers (0xff0000), Canvas 2D uses CSS strings ('#ff0000'):

```typescript
// utils/colors.ts - add this function
export function hexToCSS(hex: number): string {
  return `#${hex.toString(16).padStart(6, '0')}`;
}

export function cssToHex(css: string): number {
  return parseInt(css.replace('#', ''), 16);
}
```

### Performance Optimization

```tsx
// Disable events on static elements (huge performance boost)
<Layer listening={false}>
  {waypoints.map(waypoint => (
    <WaypointShape key={waypoint.symbol} waypoint={waypoint} listening={false} />
  ))}
</Layer>

// Use shape caching for complex static shapes
const shapeRef = useRef<Konva.Shape>(null);
useEffect(() => {
  shapeRef.current?.cache();
}, []);
```

### Memory Management

```tsx
// Clean up animations
useEffect(() => {
  const anim = new Konva.Animation(callback);
  anim.start();
  return () => {
    anim.stop();
    anim = null; // GC cleanup
  };
}, [dependencies]);
```

---

## Testing Checklist

### Visual Regression Testing

- [ ] All 15+ planet types render correctly
- [ ] All 8 ship designs render correctly
- [ ] Colors match original (compare screenshots)
- [ ] Marketplace indicators visible
- [ ] Labels positioned correctly

### Interaction Testing

- [ ] Pan via mouse drag works
- [ ] Zoom via scroll wheel works
- [ ] Zoom centers on mouse position
- [ ] Click selection works (ships & waypoints)
- [ ] Hover tooltips appear
- [ ] Minimap syncs with main view

### Animation Testing

- [ ] Ships move smoothly IN_TRANSIT
- [ ] Ships orbit correctly IN_ORBIT
- [ ] Mining lasers pulse
- [ ] ETA countdown updates every second
- [ ] Trails render behind ships
- [ ] 60fps maintained with 500 ships

### Filter Testing

- [ ] Status filters work (IN_TRANSIT, DOCKED, IN_ORBIT)
- [ ] Agent filters work
- [ ] Waypoint type filters work
- [ ] Show/hide labels toggle works
- [ ] Show/hide markets toggle works

### Performance Testing

```bash
# Chrome DevTools Performance tab
# Record 30 seconds with 500 ships
# Check:
# - Frame rate: >55fps average
# - Main thread: <50% usage
# - Memory: No leaks (stable heap after 5min)
```

---

## Rollback Plan

### If Migration Fails

**Option 1: Restore from Backup**
```bash
cd /Users/andres.camacho/Development/Personal/spacetradersV2/visualizer
tar -xzf web-backup-20251004-002701.tar.gz
cd web && npm install
```

**Option 2: Git Revert**
```bash
git checkout web/src/  # Revert all changes
npm install pixi.js@^8.0.0  # Reinstall PixiJS
```

### Hybrid Approach (Keep Both)

Run PixiJS and Konva side-by-side:
1. Keep `pixi.js` dependency
2. Add feature flag: `USE_KONVA=true`
3. Conditionally render:
   ```tsx
   {USE_KONVA ? <SpaceMapKonva /> : <SpaceMapPixi />}
   ```
4. Test both, gradually migrate users

---

## Next Steps

### Immediate Actions

1. **Review this guide** thoroughly
2. **Test infrastructure** (run `npm run dev`, verify no errors)
3. **Plan timeline** (estimate 2-3 hours per phase)

### Start Migration

When ready to proceed:

```bash
# Phase 2: Canvas Renderers
# Start with WaypointRenderer.ts
# Copy planet rendering logic, convert to Canvas 2D
# Test: Render one planet type at a time

# Phase 3: GalaxyView (easier, good starting point)
# Convert to react-konva
# Test: System selection, pan, zoom

# Phase 4-8: SpaceMap (complex, do last)
# Incremental refactor
# Test after each sub-phase
```

### Success Criteria

✅ All visual features working
✅ 60fps with 500 ships
✅ Zero TypeScript errors
✅ All tests passing
✅ Bundle size <500KB (down from PixiJS 420KB)

---

## Resources

- **Konva Docs**: https://konvajs.org/docs/
- **react-konva Docs**: https://konvajs.org/docs/react/
- **Canvas 2D API**: https://developer.mozilla.org/en-US/docs/Web/API/CanvasRenderingContext2D
- **Migration Examples**: https://konvajs.org/docs/sandbox/

---

**Last Updated:** 2025-10-04
**Migration Status:** Infrastructure Complete (3/23 tasks)
**Next Phase:** Canvas Renderers
