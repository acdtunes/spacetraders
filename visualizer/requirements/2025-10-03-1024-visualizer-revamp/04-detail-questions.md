# Detail Questions

**Date:** 2025-10-03
**Phase:** Expert Requirements

These questions clarify specific system behavior based on deep understanding of the codebase and SpaceTraders API.

---

## Q1: Should the market overlay display all trade goods with prices, or just a summary (import/export counts and best opportunities)?

**Default if unknown:** Summary only (showing all goods would clutter the UI; summary with drill-down is more scalable)

**Technical context:** Market data can include 20+ goods. The current waypoint labels are already ~3 lines. A full goods list would make the overlay very large.

**Options:**
- Summary: Show import count, export count, and top 2 profitable opportunities
- Full: Show all goods in a scrollable overlay

---

## Q2: In galaxy view, should clicking a system immediately switch to detailed system view, or show a preview popup first?

**Default if unknown:** Direct switch (consistent with current UX pattern; simpler interaction model)

**Technical context:** Current app uses SystemSelector dropdown to change systems. Gallery view would be a visual alternative. Preview popup adds complexity but provides context before switching.

**Options:**
- Direct: Click system → switch currentSystem → SpaceMap updates
- Preview: Click system → show popup with system info → "View System" button switches

---

## Q3: Should alert rules be hardcoded with reasonable defaults, or should users be able to configure thresholds via a settings panel?

**Default if unknown:** Hardcoded with reasonable defaults (simpler MVP, no need for settings storage/UI)

**Technical context:** Settings would require new UI components, state management, and potentially server-side storage. Hardcoded rules can always be made configurable later.

**Reasonable defaults:**
- Low fuel: < 20% of capacity
- Cargo full: > 90% of capacity
- Trade opportunity: > 500 credits profit per unit
- Ship arrival: Any ship changes from IN_TRANSIT to DOCKED/IN_ORBIT

---

## Q4: Should market data be polled for all discovered waypoints, or only for waypoints where ships are currently located?

**Default if unknown:** Ships only (respects API constraints, minimal API calls, most relevant data)

**Technical context:** SpaceTraders API requires a ship at the waypoint to see live market data. Polling all waypoints would be impossible without ships present. Even with cached/stale data, polling all would exceed rate limits.

**Options:**
- Ships only: Poll market for waypoint where each ship is located
- Cached + Ships: Poll ships' locations, display last-known data for other markets (requires storage)

---

## Q5: Should the galaxy view show visual lines/routes between connected systems (jump gates), or just systems as independent points?

**Default if unknown:** Just systems as points (simpler rendering, cleaner visual, can add routes later)

**Technical context:** Jump gate connections would require fetching jump gate data for all systems, determining connections, and rendering lines. Systems as points is simpler and shows fleet distribution clearly.

**Options:**
- Points only: Systems rendered as colored dots/circles
- With routes: Lines drawn between systems with jump gates connecting them
