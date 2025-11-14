# Agent-Player Auto-Sync Implementation

## Overview

This implementation automatically synchronizes the tour player filter with the selected agent filter in the visualizer. When a user selects a single agent to view, the tour visualization automatically filters to show only that agent's scout tours.

## Problem Solved

Previously, the visualizer had two separate, disconnected selection mechanisms:
1. **Agent Filter** - Controls which agents' ships are displayed on the map
2. **Tour Player Filter** - Controls which player's scout tours are displayed

Users had to manually coordinate these two filters to view a specific agent's tours, which was cumbersome and error-prone.

## Solution Architecture

### 1. Agent-to-Player Mapping (`/web/src/utils/agentHelpers.ts`)

Created utility functions to map agent symbols to player IDs using ship assignments:

```typescript
/**
 * Extract agent symbol from ship symbol
 * Example: "STORMWARDEN-1" -> "STORMWARDEN"
 */
export function getAgentSymbolFromShip(shipSymbol: string): string

/**
 * Find player_id for an agent symbol by looking up ship assignments
 */
export function getPlayerIdForAgent(
  agentSymbol: string,
  assignments: Map<string, ShipAssignment>
): number | null

/**
 * Get all unique agent symbols from assignments
 */
export function getAgentSymbolsFromAssignments(
  assignments: Map<string, ShipAssignment>
): string[]
```

**Key Insight**: Agent symbols can be derived from ship symbols by removing the trailing "-N" suffix. Ship assignments contain both `ship_symbol` and `player_id`, creating the link between agents and players.

### 2. Auto-Sync Hook (`/web/src/hooks/useAgentPlayerSync.ts`)

Created a React hook that watches the agent filter and automatically updates the player filter:

```typescript
export function useAgentPlayerSync()
```

**Sync Logic**:
- **No agents selected** (filterAgents.size === 0) → Clear player filter (show all tours)
- **Multiple agents selected** (filterAgents.size > 1) → Clear player filter (show all tours)
- **Exactly one agent selected** (filterAgents.size === 1) → Look up that agent's player_id from assignments and set it as the selected player

### 3. Agent Filter UI (`/web/src/components/ShipList.tsx`)

Added a new "Agent Filter" section to the ShipList component in the sidebar:

- Shows all agents with their color indicators
- Displays ship count for each agent in the current system
- Toggle-style buttons to show/hide each agent's ships
- Only visible when multiple agents exist
- Visual feedback shows which agents are active

**UI Features**:
- Color-coded agent indicators matching their fleet colors
- Real-time ship count per agent
- Clear active/inactive visual states
- Compact, space-efficient design

### 4. Tour Filter UI Enhancement (`/web/src/components/TourFilterPanel.tsx`)

Added visual feedback to show when the player filter is auto-synced:

```tsx
<label className="block text-xs text-gray-400 mb-1">
  Filter by Player
  {selectedPlayerId !== null && (
    <span className="ml-1 text-blue-400">(auto-synced)</span>
  )}
</label>
```

Users can see when the player filter has been automatically set based on their agent selection.

### 5. App Integration (`/web/src/App.tsx`)

Integrated the auto-sync hook into the main App component:

```typescript
import { useAgentPlayerSync } from './hooks/useAgentPlayerSync';

function App() {
  // ... existing hooks
  useAgentPlayerSync();  // ← Auto-sync magic happens here
}
```

## Data Flow

```
User clicks agent filter button
          ↓
filterAgents Set updated (via toggleAgentFilter)
          ↓
useAgentPlayerSync hook detects change
          ↓
If exactly 1 agent selected:
  1. Get agent from agents array
  2. Extract agent symbol
  3. Look up player_id from assignments Map
  4. Call setSelectedPlayerId(player_id)
          ↓
BotPollingService fetches tours with player_id filter
          ↓
Only selected player's tours are displayed
```

## User Experience

### Before
1. User opens visualizer with multiple agents
2. User wants to focus on one agent's operations
3. User manually filters agent ships in sidebar
4. User separately opens tour filter panel
5. User manually selects matching player ID
6. User must remember which player ID corresponds to which agent

### After
1. User opens visualizer with multiple agents
2. User clicks one agent in the "Agent Filter" section
3. **Tours automatically filter to that agent's player ID**
4. User sees immediate, synchronized view of agent's operations
5. User can still manually override player filter if needed

## Technical Details

### Why Ship Assignments?

The visualizer stores agents in a local JSON file with only:
- `id` (UUID)
- `symbol` (agent callsign like "STORMWARDEN")
- `token` (API token)
- `color`, `visible`, `createdAt`

There's no direct `player_id` field in the agent data. However, the bot operations database contains `ship_assignments` with both `ship_symbol` and `player_id`. By extracting the agent symbol from ship symbols, we can establish the agent → player_id relationship.

### Filter Logic Semantics

The `filterAgents` Set uses inclusion semantics:
- Empty Set → Show all agents
- Non-empty Set → Show only agents whose IDs are IN the set

This differs from typical "exclusion" filter semantics and is important for the auto-sync logic.

### Edge Cases Handled

1. **No assignments available** → Player filter stays null (show all)
2. **Agent has no ships yet** → Player filter stays null (show all)
3. **Multiple agents selected** → Player filter clears to null (avoid confusion)
4. **User manually changes player filter** → Auto-sync respects manual override until agent filter changes again
5. **Agent removed** → Auto-sync clears player filter if that agent was selected

## Files Modified

### New Files
- `/web/src/utils/agentHelpers.ts` - Agent symbol extraction and player mapping utilities
- `/web/src/hooks/useAgentPlayerSync.ts` - Auto-sync React hook

### Modified Files
- `/web/src/App.tsx` - Added useAgentPlayerSync hook
- `/web/src/components/ShipList.tsx` - Added agent filter UI section
- `/web/src/components/TourFilterPanel.tsx` - Added "(auto-synced)" indicator

## Testing Guide

### Manual Testing Workflow

1. **Setup**: Ensure visualizer has at least 2 agents with active scout operations
2. **Initial State**: Open visualizer, verify all agents' ships and tours are visible
3. **Select Single Agent**:
   - Click one agent in the "Agent Filter" section
   - Verify only that agent's ships are shown on map
   - Verify only that agent's tours are shown in tour panel
   - Verify tour filter shows "(auto-synced)" indicator
4. **Select Multiple Agents**:
   - Click a second agent to add to filter
   - Verify both agents' ships are shown
   - Verify ALL tours are shown (no player filter)
5. **Deselect All**:
   - Click agents to deselect all
   - Verify all ships and tours are shown
6. **Manual Override**:
   - Select one agent
   - Manually change player filter dropdown
   - Click a different agent
   - Verify player filter updates to new agent's player_id

### Expected Behavior Matrix

| Agent Filter State | Expected Player Filter | Expected Tours Shown |
|-------------------|------------------------|---------------------|
| None selected (empty) | null (All Players) | All tours |
| 1 agent selected | That agent's player_id | Only that agent's tours |
| 2+ agents selected | null (All Players) | All tours |

## Future Enhancements

Potential improvements for future iterations:

1. **Agent-level credits display** - Show credits per agent in filter section
2. **Quick "focus on agent" button** - Center map on agent's primary operations
3. **Agent performance stats** - Show operation summaries per agent
4. **Keyboard shortcuts** - Quick keys to select/deselect agents
5. **Agent groups** - Allow saving and loading agent filter presets
6. **Tour color coordination** - Make tour colors match agent colors

## Migration Notes

No breaking changes. This is a pure enhancement that:
- Adds new functionality without removing anything
- Existing manual player filter still works
- No database schema changes required
- No API changes required
- Backward compatible with existing data

## Performance Considerations

- **Hook re-runs**: Only when `filterAgents`, `agents`, or `assignments` change (infrequent)
- **Lookup complexity**: O(n) where n = number of assignments (typically <100)
- **UI rendering**: Agent filter section only renders when agents.length > 1
- **No network calls**: All data already in memory from existing polling

## Known Limitations

1. **Agent without ships**: If an agent has no ships yet, player_id cannot be determined
2. **Player changes**: If bot reassigns ships to different player_id, sync may briefly lag until next assignment poll
3. **Multiple players per agent**: System assumes 1:1 agent:player_id relationship (current bot architecture)

## Troubleshooting

### Tours not syncing to agent

**Symptoms**: Selecting an agent doesn't filter tours

**Possible causes**:
1. Agent has no ship assignments yet
2. Assignments haven't loaded (wait 10s for first poll)
3. Agent symbol doesn't match ship prefix (check ship naming)

**Solution**: Check browser console for warnings, verify assignments API returns data

### "(auto-synced)" not showing

**Symptoms**: Label doesn't appear when agent selected

**Possible causes**:
1. Multiple agents selected (intentional - no sync in this case)
2. Player filter manually set to null
3. React state not updating

**Solution**: Check `selectedPlayerId` in React DevTools

### Agent filter not visible

**Symptoms**: "Agent Filter" section doesn't appear

**Expected behavior**: Section only shows when `agents.length > 1`

**Solution**: Add a second agent or verify agents are loaded correctly

## Conclusion

This implementation provides a seamless, intuitive connection between agent selection and tour visualization. Users can now focus on a single agent's operations with a single click, eliminating the need to manually coordinate multiple filters.

The architecture is extensible and can easily accommodate future enhancements like agent grouping, performance tracking, or multi-system coordination.
