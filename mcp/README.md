# MCP Servers

This directory houses the Model Context Protocol servers used by the SpaceTraders
project.

- `api/` – Node.js/TypeScript server that exposes the public SpaceTraders API.
  Build with `npm run build` and launch via `node mcp/api/build/index.js` (or
  `npm start`).
- `bot/` – Node.js/TypeScript server that surfaces the SpaceTraders bot
  automation. It shells out to the Python bot code when tools are invoked. Build
  with `npm run build` and launch via `node mcp/bot/build/index.js` (or
  `npm start`).

See the individual READMEs in each directory for installation and configuration
details.
