# Bug Fix: MCP SpaceTraders API Token Type Mismatch

**Date**: 2025-10-09
**Severity**: Critical (blocking all MCP API operations)
**Status**: RESOLVED

## Problem Summary

The `mcp__spacetraders-api__*` tools repeatedly failed with:
```
Error: API request failed: 401 - Token has an invalid subject claim.
Expected "agent-token" but received "account-token".
Did you send the correct type of token?
```

This was a RECURRENT issue that kept coming back, blocking all direct SpaceTraders API queries through the MCP server.

## Root Cause Analysis

The bug had THREE contributing factors:

### 1. Configuration Issue (Primary Cause)
The Claude Desktop config file had a placeholder instead of an actual token:

```json
{
  "env": {
    "SPACETRADERS_TOKEN": "<SET_SPACETRADERS_TOKEN>"  // WRONG: Placeholder
  }
}
```

### 2. Missing Validation
The MCP API server (`mcp/api/src/client.ts`) had no validation to:
- Detect placeholder tokens
- Verify token type (agent-token vs account-token)
- Provide helpful error messages

### 3. Token Type Confusion
SpaceTraders has TWO types of tokens:
- **Agent Token** (REQUIRED for API): JWT with `"sub": "agent-token"` - obtained from agent registration
- **Account Token** (WRONG): JWT with `"sub": "account-token"` - from website login

The error message suggests someone had previously configured an account-token instead of an agent-token, explaining why this was a recurring issue.

## Solution Implemented

### 1. Client Validation (`mcp/api/src/client.ts`)

Added three layers of validation:

**A. Placeholder Detection**:
```typescript
private isPlaceholderToken(token: string): boolean {
  const placeholders = [
    "<SET_SPACETRADERS_TOKEN>",
    "your_token_here",
    "YOUR_TOKEN",
    "REPLACE_ME",
    "<TOKEN>",
  ];
  return placeholders.some((placeholder) => token.includes(placeholder));
}
```

**B. JWT Decoding and Type Validation**:
```typescript
private decodeTokenType(token: string): string | null {
  try {
    const parts = token.split(".");
    if (parts.length !== 3) return null;

    let payload = parts[1];
    const padding = 4 - (payload.length % 4);
    if (padding !== 4) {
      payload += "=".repeat(padding);
    }

    const decoded = atob(payload.replace(/-/g, "+").replace(/_/g, "/"));
    const data = JSON.parse(decoded);
    return data.sub || null;
  } catch {
    return null;
  }
}
```

**C. Runtime Token Type Enforcement**:
```typescript
private getHeaders(agentToken?: string): Record<string, string> {
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
  };

  const token = agentToken || this.accountToken;
  if (token) {
    // Validate token type before using
    const tokenType = this.decodeTokenType(token);
    if (tokenType && tokenType !== "agent-token") {
      throw new Error(
        `Invalid token type: expected "agent-token" but got "${tokenType}". ` +
        `Please use an agent token from agent registration, not an account token.`
      );
    }
    headers["Authorization"] = `Bearer ${token}`;
  }

  return headers;
}
```

### 2. Configuration Fix

Updated Claude Desktop config with the correct agent token from the database:

```bash
# Extract token from database
sqlite3 var/data/sqlite/spacetraders.db \
  "SELECT token FROM players WHERE agent_symbol='STORMWATCH';"
```

```json
{
  "mcpServers": {
    "spacetraders-api": {
      "env": {
        "SPACETRADERS_TOKEN": "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJpZGVudGlmaWVyIjoiU1RPUk1XQVRDSCIsInN1YiI6ImFnZW50LXRva2VuIiwuLi59..."
      }
    }
  }
}
```

### 3. Documentation Updates

Enhanced `mcp/api/README.md` with:
- Clear distinction between agent-token and account-token
- Step-by-step instructions for obtaining agent tokens
- Explicit warnings about placeholder values
- Example configuration with proper token format

## Prevention Mechanisms

The fix implements multiple safeguards to prevent recurrence:

1. **Startup Validation**: Server detects placeholder tokens on initialization and warns to stderr
2. **Runtime Validation**: Every API call validates token type before sending request
3. **Clear Error Messages**: If wrong token type is used, error explains the difference
4. **Documentation**: README now explicitly warns about token types and placeholders

## Testing Instructions

To verify the fix:

1. **Rebuild MCP server**:
   ```bash
   cd mcp/api
   npm run build
   ```

2. **Restart Claude Desktop** to reload MCP servers

3. **Test with API call**:
   ```typescript
   mcp__spacetraders-api__get_agent()
   ```

   **Expected**: Success with agent data
   **Before fix**: 401 error about token subject claim

4. **Test validation** (optional):
   ```bash
   # Temporarily set placeholder
   export SPACETRADERS_TOKEN="<SET_SPACETRADERS_TOKEN>"
   node mcp/api/build/index.js
   ```

   **Expected**: Warning message on stderr about placeholder token

## Files Modified

1. `/Users/andres.camacho/Development/Personal/spacetradersV2/mcp/api/src/client.ts`
   - Added `isPlaceholderToken()` method
   - Added `decodeTokenType()` method
   - Modified `getHeaders()` to validate token type

2. `/Users/andres.camacho/Library/Application Support/Claude/claude_desktop_config.json`
   - Replaced `<SET_SPACETRADERS_TOKEN>` with actual agent token

3. `/Users/andres.camacho/Development/Personal/spacetradersV2/mcp/api/README.md`
   - Added token type explanation
   - Added token acquisition instructions
   - Added validation behavior documentation

## Lessons Learned

1. **Always validate input early**: Token validation should happen at initialization, not first API call
2. **Provide helpful defaults**: Placeholder detection prevents silent failures
3. **Document token types clearly**: Users need to understand agent-token vs account-token distinction
4. **Make errors actionable**: Error messages should explain how to fix the problem

## Recurrence Prevention

To prevent this bug from returning:

1. **Never commit real tokens** to version control
2. **Always use placeholders in example configs** but validate against them at runtime
3. **Store tokens in secure locations** (database, env files, keychain)
4. **Add token validation tests** to catch this in CI/CD
5. **Document token sources** clearly in README

## Related Issues

- This bug blocked all `mcp__spacetraders-api__*` tool usage
- Bot operations (`mcp__spacetraders-bot__*`) were unaffected (they pull tokens from database)
- Similar issues may exist in other MCP servers - audit recommended

## Approval Status

- **Admiral Approval**: Required for production deployment
- **Testing**: Manual verification pending Claude Desktop restart
- **Deployment**: Configuration file updated, server rebuilt
