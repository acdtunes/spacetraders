# SpaceTraders Fleet Visualization

A real-time web application for visualizing SpaceTraders agent ship movements across star systems.

## Features

✨ **Real-time ship tracking** - See ship positions update every 5 seconds
🗺️ **Interactive 2D space map** - Zoom and pan across star systems
🚀 **Multi-agent support** - Track multiple agents simultaneously with color-coded ships
⛽ **Ship details** - View fuel, cargo, and navigation status
🎯 **Smart filtering** - Filter by ship status or agent
🌌 **System switching** - Easily navigate between different star systems

## Project Structure

```
spacetradersV2/
├── src/                    # Existing MCP server
│   ├── client.ts          # Reusable SpaceTraders API client
│   └── index.ts           # MCP server
├── server/                # Backend proxy (NEW)
│   ├── index.ts          # Express server
│   ├── routes/
│   │   ├── agents.ts     # Agent management endpoints
│   │   └── systems.ts    # System/waypoint endpoints
│   └── db/
│       └── storage.ts    # Agent token storage
├── web/                   # Frontend app (NEW)
│   ├── src/
│   │   ├── components/   # React components
│   │   ├── services/     # API client & polling
│   │   ├── store/        # Zustand state management
│   │   ├── types/        # TypeScript interfaces
│   │   └── hooks/        # Custom React hooks
│   └── index.html
└── CLAUDE.md             # Game guide & operational manual
```

## Installation

### Prerequisites

- Node.js 18+ and npm
- SpaceTraders agent token(s) - Get one at https://spacetraders.io

### 1. Install Dependencies

**Backend:**
```bash
cd server
npm install
```

**Frontend:**
```bash
cd web
npm install
```

### 2. Build and Run

**Backend Server (Terminal 1):**
```bash
cd server
npm run build
npm start
```

The backend will start on http://localhost:3001

**Frontend Dev Server (Terminal 2):**
```bash
cd web
npm run dev
```

The frontend will start on http://localhost:3000

## Usage

### Adding an Agent

1. Click the "Agents" button in the top-right
2. Paste your SpaceTraders agent token
3. Click "Add Agent"
4. The system will validate the token and add the agent

### Viewing Ships

1. Once agents are added, they'll appear in the system selector
2. Select a system from the dropdown
3. Ships will automatically appear on the map
4. Use the sidebar to filter by status or agent

### Map Controls

- **Pan**: Click and drag (planned feature)
- **Zoom**: Mouse wheel (planned feature)
- **Ship Info**: Hover over ships to see details (planned feature)
- **Toggle Labels**: Use the checkbox in the sidebar

## API Endpoints

### Backend Proxy

**Agent Management:**
- `GET /api/agents` - List all tracked agents
- `POST /api/agents` - Add new agent (requires token in body)
- `PATCH /api/agents/:id` - Update agent settings
- `DELETE /api/agents/:id` - Remove agent
- `GET /api/agents/:id/ships` - Get all ships for an agent

**System Data:**
- `GET /api/systems/:systemSymbol` - Get system details
- `GET /api/systems/:systemSymbol/waypoints` - Get waypoints in system

## Configuration

### Rate Limits

The app respects SpaceTraders API rate limits:
- 2 requests/second sustained
- 10 request burst over 10 seconds

The polling service automatically staggers requests between agents with a 600ms delay.

### Polling Interval

Ships are updated every 5 seconds. To change this, edit:

```typescript
// web/src/services/polling.ts
const POLL_INTERVAL = 5000; // milliseconds
```

## Development

### Backend

```bash
cd server
npm run dev  # Watch mode with auto-rebuild
```

### Frontend

```bash
cd web
npm run dev  # Vite dev server with HMR
```

### Type Checking

Both frontend and backend use TypeScript. Run type checking:

```bash
# Backend
cd server && npm run build

# Frontend
cd web && npm run build
```

## Troubleshooting

### "Failed to add agent" error

- Verify the token is correct (copy-paste from SpaceTraders)
- Check backend server is running on port 3001
- Check browser console for detailed error messages

### Ships not appearing

- Ensure at least one agent is added
- Select a system from the dropdown
- Check that ships exist in that system
- Verify backend can reach SpaceTraders API

### Polling stopped

- Check browser console for errors
- Verify agents have `visible: true`
- Restart the frontend dev server

## Future Enhancements

- [ ] Ship trails showing recent movement paths
- [ ] Interactive ship tooltips with detailed info
- [ ] Zoom and pan controls
- [ ] Ship status animations (pulse for in-transit)
- [ ] Market data overlays
- [ ] Contract delivery route planning
- [ ] Historical playback
- [ ] WebSocket support (when API adds it)

## Architecture

### Technology Stack

**Frontend:**
- React 18
- TypeScript
- PixiJS (Canvas rendering)
- Zustand (State management)
- Tailwind CSS
- Vite (Build tool)

**Backend:**
- Express.js
- TypeScript
- Node.js
- JSON file storage (agents.json)

### Data Flow

```
User Browser → React App (Vite dev server :3000)
                    ↓
        Zustand Store (client state)
                    ↓
        Polling Service (every 5s)
                    ↓
        Backend Proxy (:3001/api/*)
                    ↓
    SpaceTraders API (api.spacetraders.io/v2)
```

### Security

- Agent tokens are never exposed to the browser
- Tokens stored in `server/db/agents.json` (server-side only)
- API requests proxied through backend
- CORS enabled for local development

**Production Deployment:**
- Encrypt `agents.json` or use a proper database
- Use environment variables for sensitive config
- Enable HTTPS
- Add authentication for multi-user scenarios

## Contributing

This project was built with requirements gathered through an interactive process documented in `requirements/2025-10-03-0046-agent-visualization/`.

To add features:
1. Review the requirements spec in that folder
2. Create a new branch
3. Implement the feature
4. Test with real SpaceTraders agents
5. Submit a pull request

## License

MIT

## Resources

- **SpaceTraders**: https://spacetraders.io
- **API Docs**: https://docs.spacetraders.io
- **Game Guide**: See `CLAUDE.md` in the root directory
- **Requirements**: See `requirements/2025-10-03-0046-agent-visualization/06-requirements-spec.md`

---

**Built with ❤️ for the SpaceTraders community**
