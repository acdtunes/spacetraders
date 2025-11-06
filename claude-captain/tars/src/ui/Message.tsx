/**
 * Message display component for TARS
 */

import React from 'react';
import { Box, Text } from 'ink';
import type { SDKMessage } from '@anthropic-ai/claude-agent-sdk';
import { MarkdownText } from './MarkdownText.js';

interface MessageProps {
  message: SDKMessage;
  isInSubagent?: boolean;
}

export const Message: React.FC<MessageProps> = ({ message, isInSubagent = false }) => {
  if (message.type === 'assistant') {
    return <AssistantMessage message={message} isInSubagent={isInSubagent} />;
  } else if (message.type === 'user') {
    return <UserMessage message={message} />;
  } else if (message.type === 'system') {
    return <SystemMessage message={message} />;
  } else if (message.type === 'result') {
    // Don't display result messages
    return null;
  }

  return null;
};

const AssistantMessage: React.FC<{ message: any; isInSubagent?: boolean }> = ({
  message,
  isInSubagent = false
}) => {
  return (
    <Box flexDirection="column" marginBottom={1}>
      {message.message.content.map((block: any, index: number) => {
        if (block.type === 'text') {
          return (
            <Box key={index} flexDirection="column">
              <MarkdownText>{block.text}</MarkdownText>
            </Box>
          );
        } else if (block.type === 'tool_use') {
          // Only show Task delegation and MCP tools
          if (block.name === 'Task') {
            const subagent = block.input?.subagent_type || 'unknown';
            return (
              <Box key={index}>
                <Text color="cyan">👤 Delegating to {subagent}</Text>
              </Box>
            );
          } else if (block.name.startsWith('mcp__')) {
            const toolName = block.name.replace('mcp__spacetraders-bot__', '');
            const params = block.input || {};

            if (Object.keys(params).length > 0) {
              const paramStr = Object.entries(params)
                .map(([k, v]) => `${k}=${v}`)
                .join(', ');
              return (
                <Box key={index} paddingLeft={isInSubagent ? 2 : 0}>
                  <Text color="magenta">
                    🔧 {toolName}({paramStr})
                  </Text>
                </Box>
              );
            } else {
              return (
                <Box key={index} paddingLeft={isInSubagent ? 2 : 0}>
                  <Text color="magenta">🔧 {toolName}()</Text>
                </Box>
              );
            }
          }
        } else if (block.type === 'thinking') {
          return (
            <Box key={index} flexDirection="column" marginBottom={1}>
              <Text color="cyan" bold>
                💭 TARS Extended Thinking:
              </Text>
              {'thinking' in block && block.thinking && (
                <Box paddingLeft={2} paddingTop={1}>
                  <Text color="gray" dimColor>
                    {block.thinking}
                  </Text>
                </Box>
              )}
            </Box>
          );
        }

        return null;
      })}
    </Box>
  );
};

const UserMessage: React.FC<{ message: SDKMessage }> = () => {
  // User messages (tool results) are typically hidden
  return null;
};

const SystemMessage: React.FC<{ message: any }> = ({ message }) => {
  if (message.subtype === 'init') {
    // Display init message to debug MCP server connection
    const mcpServers = message.mcp_servers || [];
    const mcpStatus = mcpServers.map((s: any) => `${s.name}: ${s.status}`).join(', ');

    return (
      <Box marginBottom={1}>
        <Text dimColor>
          📦 Initialized: {message.tools?.length || 0} tools | MCP: {mcpStatus || 'none'}
        </Text>
      </Box>
    );
  } else if (message.subtype === 'compact_boundary') {
    const metadata = message.data?.compact_metadata || {};

    return (
      <Box marginBottom={1}>
        <Text dimColor>📦 Context compaction: {JSON.stringify(metadata)}</Text>
      </Box>
    );
  } else if (message.subtype === 'can_use_tool') {
    const data = message.data || {};
    const toolName = data.tool_name || 'unknown';
    const toolInput = data.input || {};

    let paramStr = "";
    if (Object.keys(toolInput).length > 0) {
      const params = Object.entries(toolInput)
        .slice(0, 3)
        .map(([k, v]) => `${k}=${String(v).substring(0, 50)}`)
        .join(", ");

      if (Object.keys(toolInput).length > 3) {
        paramStr = `(${params}, ...)`;
      } else {
        paramStr = `(${params})`;
      }
    }

    return (
      <Box flexDirection="column" marginBottom={1}>
        <Text color="yellow">⚠️  Permission Required</Text>
        <Text color="yellow">Tool: {toolName}{paramStr}</Text>
        <Text dimColor>Note: This should be pre-approved in settings.json</Text>
      </Box>
    );
  } else if (message.content && typeof message.content === 'string') {
    // Handle generic app notifications (not from SDK)
    return (
      <Box marginBottom={1}>
        <Text color="yellow">{message.content}</Text>
      </Box>
    );
  }

  return null;
};
