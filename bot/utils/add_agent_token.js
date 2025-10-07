const fs = require('fs');

const content = fs.readFileSync('src/index.ts', 'utf8');

// Add agentToken to all tool properties (except register_agent which already doesn't have it)
const updated = content.replace(
  /(name: "(?!register_agent)[^"]+",[\s\S]*?inputSchema: {\s*type: "object",\s*properties: {)/g,
  (match) => {
    if (match.includes('agentToken')) {
      return match; // Already has agentToken
    }
    return match + '\n            agentToken: {\n              type: "string",\n              description: "Agent authentication token (optional, uses account token if not provided)",\n            },';
  }
);

fs.writeFileSync('src/index.ts', updated);
console.log('Added agentToken parameter to all tools');
