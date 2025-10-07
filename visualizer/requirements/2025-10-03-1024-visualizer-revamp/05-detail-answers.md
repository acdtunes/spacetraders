# Detail Answers

**Date:** 2025-10-03
**Phase:** Expert Requirements

## Q1: Should the market overlay display all trade goods with prices, or just a summary (import/export counts and best opportunities)?
**Answer:** Summary

Market overlays will display:
- Count of imports and exports
- Top 2 most profitable trade opportunities (if applicable)
- Keep UI clean and scannable

---

## Q2: In galaxy view, should clicking a system immediately switch to detailed system view, or show a preview popup first?
**Answer:** Direct switch

Clicking a system in galaxy view will:
- Immediately update currentSystem state
- Switch to system detail view (SpaceMap)
- Consistent with existing SystemSelector behavior

---

## Q3: Should alert rules be hardcoded with reasonable defaults, or should users be able to configure thresholds via a settings panel?
**Answer:** Skip alerts entirely

The alerts/notifications feature is being removed from scope:
- No alert system
- No notification center
- No alertService
- Focus on market data and galaxy view only

---

## Q4: Should market data be polled for all discovered waypoints, or only for waypoints where ships are currently located?
**Answer:** Ships only

Market data polling strategy:
- Only poll markets at waypoints where ships are currently located
- Respects SpaceTraders API constraint (requires ship presence)
- Minimizes API calls and respects rate limits
- Shows most relevant, real-time market data

---

## Q5: Should the galaxy view show visual lines/routes between connected systems (jump gates), or just systems as independent points?
**Answer:** Points only

Galaxy view will render:
- Systems as colored dots/circles
- No jump gate connection lines
- Cleaner visual
- Simpler rendering
- Shows fleet distribution clearly

---

## Summary

**Features to Implement:**
1. **Market Data Visualization**
   - Summary overlays on waypoints
   - Shows import/export counts
   - Displays top 2 trade opportunities
   - Polls only for ships' current locations

2. **Galaxy View**
   - Multi-system visualization
   - Systems as independent points
   - Direct click-to-switch navigation
   - Shows fleet distribution across galaxy

**Features Removed from Scope:**
- Alerts and notifications system (entire feature cut)
