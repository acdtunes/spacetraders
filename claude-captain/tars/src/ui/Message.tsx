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
  showSubagentOutput?: boolean;
  allMessages?: SDKMessage[];
  messageIndex?: number;
}

export const Message: React.FC<MessageProps> = ({
  message,
  isInSubagent = false,
  showSubagentOutput = false,
  allMessages = [],
  messageIndex = 0
}) => {
  if (message.type === 'assistant') {
    return <AssistantMessage message={message} isInSubagent={isInSubagent} allMessages={allMessages} messageIndex={messageIndex} showSubagentOutput={showSubagentOutput} />;
  } else if (message.type === 'user') {
    return <UserMessage message={message} showSubagentOutput={showSubagentOutput} allMessages={allMessages} messageIndex={messageIndex} />;
  } else if (message.type === 'system') {
    return <SystemMessage message={message} />;
  } else if (message.type === 'result') {
    // Don't display result messages
    return null;
  } else if (message.type === 'stream_event') {
    // Handle token-by-token streaming
    return <StreamEventMessage message={message} />;
  }

  return null;
};

const AssistantMessage: React.FC<{
  message: any;
  isInSubagent?: boolean;
  allMessages?: SDKMessage[];
  messageIndex?: number;
  showSubagentOutput?: boolean;
}> = ({
  message,
  isInSubagent = false,
  allMessages = [],
  messageIndex = 0,
  showSubagentOutput = false
}) => {
  // Helper to find the next subagent user message for a given tool_use_id
  const findSubagentMessage = (toolUseId: string): any | null => {
    for (let i = messageIndex + 1; i < allMessages.length; i++) {
      const msg = allMessages[i];
      if (msg.type === 'user' && 'parent_tool_use_id' in msg && msg.parent_tool_use_id === toolUseId) {
        return msg;
      }
      // Stop searching if we hit another assistant message (moved to next response)
      if (msg.type === 'assistant') {
        break;
      }
    }
    return null;
  };

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
            const subagentMsg = findSubagentMessage(block.id);

            return (
              <Box key={index} flexDirection="column">
                <Text color="cyan">👤 Delegating to {subagent}</Text>
                {showSubagentOutput && subagentMsg && (
                  <Box flexDirection="column" paddingLeft={2} marginTop={1} marginBottom={1}>
                    <Text color="gray" dimColor>🔍 Subagent Internal:</Text>
                    {((subagentMsg as any)?.message?.content || []).map((contentBlock: any, idx: number) => {
                      if (contentBlock.type === 'text') {
                        return <MarkdownText key={idx}>{contentBlock.text}</MarkdownText>;
                      }
                      return null;
                    })}
                  </Box>
                )}
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

const UserMessage: React.FC<{
  message: SDKMessage;
  showSubagentOutput: boolean;
  allMessages?: SDKMessage[];
  messageIndex?: number;
}> = ({ message, showSubagentOutput, allMessages = [], messageIndex = 0 }) => {
  // Check if this is a subagent internal message
  const isSubagentMessage = 'parent_tool_use_id' in message && message.parent_tool_use_id !== null;

  // If it's a subagent message, check if it was already rendered inline with a Task delegation
  if (isSubagentMessage && showSubagentOutput) {
    const parentToolUseId = (message as any).parent_tool_use_id;

    // Look back in recent messages to see if there's an assistant message with a Task tool_use matching this parent_tool_use_id
    for (let i = messageIndex - 1; i >= Math.max(0, messageIndex - 5); i--) {
      const prevMsg = allMessages[i];
      if (prevMsg?.type === 'assistant') {
        const content = (prevMsg as any)?.message?.content || [];
        const hasMatchingTask = content.some(
          (block: any) => block.type === 'tool_use' && block.name === 'Task' && block.id === parentToolUseId
        );
        if (hasMatchingTask) {
          // This message was already rendered inline, skip it
          return null;
        }
      }
    }
  }

  // Only show if toggle is ON and it's a subagent message
  if (!isSubagentMessage || !showSubagentOutput) {
    return null;
  }

  // Display subagent internal message (for cases not rendered inline, like nested subagents)
  const content = (message as any)?.message?.content || [];

  return (
    <Box flexDirection="column" paddingLeft={2} marginBottom={1}>
      <Text color="gray" dimColor>🔍 Subagent Internal:</Text>
      {content.map((block: any, idx: number) => {
        if (block.type === 'text') {
          return <MarkdownText key={idx}>{block.text}</MarkdownText>;
        } else if (block.type === 'tool_result') {
          return (
            <Box key={idx} flexDirection="column" marginTop={1}>
              <Text color="yellow">📊 Tool Result: {block.tool_use_id}</Text>
              <MarkdownText>{typeof block.content === 'string' ? block.content : JSON.stringify(block.content, null, 2)}</MarkdownText>
            </Box>
          );
        } else if (block.type === 'tool_use') {
          return (
            <Box key={idx} flexDirection="column" marginTop={1}>
              <Text color="cyan">🔧 {block.name}({Object.keys(block.input || {}).map(k => `${k}=${JSON.stringify(block.input[k])}`).join(', ')})</Text>
            </Box>
          );
        }
        return null;
      })}
    </Box>
  );
};

const StreamEventMessage: React.FC<{ message: any }> = ({ message }) => {
  const event = message.event;

  // Only display text tokens - ignore structural events
  if (event?.type === 'content_block_delta' &&
      event.delta?.type === 'text_delta' &&
      event.delta?.text) {
    // Display token without newline to accumulate text
    return <Text>{event.delta.text}</Text>;
  }

  // Other event types (message_start, content_block_start, etc.) → don't display
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
