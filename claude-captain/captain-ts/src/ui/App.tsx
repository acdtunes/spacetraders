/**
 * Main Ink UI component for TARS Captain
 */

import React, { useState, useEffect } from 'react';
import { Box, Text, Newline, useStdout, useInput } from 'ink';
import Spinner from 'ink-spinner';
import TextInput from 'ink-text-input';
import type { SDKMessage } from '@anthropic-ai/claude-agent-sdk';
import { Message } from './Message.js';

interface AppProps {
  onCommand: (command: string) => Promise<void>;
  messages: SDKMessage[];
  isProcessing: boolean;
  memoryStatus: string;
  onExit: () => void;
  cancelRef: React.MutableRefObject<boolean>;
  isAfkMode?: boolean;
  userCommands?: string[];
}

export const App: React.FC<AppProps> = ({
  onCommand,
  messages,
  isProcessing,
  memoryStatus,
  onExit,
  cancelRef,
  isAfkMode = false,
  userCommands = []
}) => {
  const [input, setInput] = useState('');
  const { stdout } = useStdout();
  const [terminalWidth, setTerminalWidth] = useState(stdout?.columns || 80);

  // Update terminal width on resize
  useEffect(() => {
    const handleResize = () => {
      setTerminalWidth(stdout?.columns || 80);
    };

    stdout?.on('resize', handleResize);

    return () => {
      stdout?.off('resize', handleResize);
    };
  }, [stdout]);

  // Handle ESC key to interrupt processing
  useInput((input, key) => {
    if (key.escape && isProcessing) {
      cancelRef.current = true;
    }
  });

  const handleSubmit = async (value: string) => {
    if (!value.trim()) {
      return;
    }

    // Handle special commands
    if (value.toLowerCase() === 'exit' || value.toLowerCase() === 'quit') {
      onExit();
      return;
    }

    setInput('');
    await onCommand(value);
  };

  return (
    <Box flexDirection="column">
      {/* Header */}
      <Box flexDirection="column" marginBottom={1}>
        <Text bold color="cyan" wrap="truncate">
          {'─'.repeat(terminalWidth)}
        </Text>
        <Text bold color="cyan">
          🚀 TARS Fleet Command Console
        </Text>
        <Text color="gray">Tactical Autonomous Resource Strategist</Text>
        <Text color="gray">Model: claude-sonnet-4-5-20250929</Text>
        <Text color="gray">Memory: {memoryStatus}</Text>
        <Text bold color="cyan" wrap="truncate">
          {'─'.repeat(terminalWidth)}
        </Text>
        <Newline />
      </Box>

      {/* Messages */}
      <Box flexDirection="column" marginBottom={1}>
        {messages.map((message, index) => {
          // Show user command before each assistant response
          const userCommand = userCommands[Math.floor(index / 2)];
          const showUserCommand = message.type === 'assistant' && userCommand;

          // Determine if we're in a subagent context by checking if previous message has Task delegation
          let isInSubagent = false;
          if (message.type === 'assistant' && index > 0) {
            // Look backwards to see if we've delegated to a subagent
            for (let i = index - 1; i >= 0; i--) {
              const prevMsg = messages[i];
              if (prevMsg.type === 'assistant') {
                const content = (prevMsg as any)?.message?.content || [];
                const hasTaskCall = content.some(
                  (block: any) => block.type === 'tool_use' && block.name === 'Task'
                );
                if (hasTaskCall) {
                  isInSubagent = true;
                  break;
                }
                // If we hit text content from assistant, we've likely exited subagent context
                const hasText = content.some((block: any) => block.type === 'text');
                if (hasText) {
                  break;
                }
              }
            }
          }

          return (
            <React.Fragment key={index}>
              {showUserCommand && (
                <Box marginBottom={1}>
                  <Text bold>Admiral&gt; </Text>
                  <Text>{userCommand}</Text>
                </Box>
              )}
              <Message message={message} isInSubagent={isInSubagent} />
            </React.Fragment>
          );
        })}
      </Box>

      {/* Processing indicator */}
      {isProcessing && (
        <Box marginBottom={1}>
          <Text color="cyan">
            <Spinner type="dots" /> TARS is processing...{' '}
            <Text dimColor>(ESC to interrupt)</Text>
          </Text>
        </Box>
      )}

      {/* AFK Mode Indicator */}
      {isAfkMode && !isProcessing && (
        <Box flexDirection="column">
          <Text color="cyan" wrap="truncate">
            {'─'.repeat(terminalWidth)}
          </Text>
          <Box flexDirection="column" paddingY={1}>
            <Text bold color="green">
              🤖 AFK MODE ACTIVE - Autonomous Operation
            </Text>
            <Text dimColor>
              TARS is operating autonomously. ESC to interrupt and return to interactive mode.
            </Text>
          </Box>
          <Text color="cyan" wrap="truncate">
            {'─'.repeat(terminalWidth)}
          </Text>
        </Box>
      )}

      {/* Input prompt */}
      {!isAfkMode && !isProcessing && (
        <Box flexDirection="column">
          <Text color="cyan" wrap="truncate">
            {'─'.repeat(terminalWidth)}
          </Text>
          <Box flexDirection="row">
            <Text bold>Admiral&gt; </Text>
            <TextInput value={input} onChange={setInput} onSubmit={handleSubmit} />
          </Box>
          <Text color="cyan" wrap="truncate">
            {'─'.repeat(terminalWidth)}
          </Text>
        </Box>
      )}

      {/* Help text */}
      {!isAfkMode && (
        <Box marginTop={1}>
          <Text dimColor>
            Commands: exit/quit | /clear-memory | /afk [hours] [checkin_min] (default: /afk 4 30)
          </Text>
        </Box>
      )}
    </Box>
  );
};
