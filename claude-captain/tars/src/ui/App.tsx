/**
 * Main Ink UI component for TARS Captain
 */

import React, { useState, useEffect, useCallback } from 'react';
import { Box, Text, Newline, useStdout, useInput, Static } from 'ink';
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
  showSubagentOutput: boolean;
  onToggleSubagentOutput: () => void;
  streamingText: string;
}

// Isolated input section - manages its own state to avoid parent re-renders
const InputSection = React.memo(({
  isProcessing,
  isAfkMode,
  handleSubmit,
  terminalWidth
}: {
  isProcessing: boolean;
  isAfkMode: boolean;
  handleSubmit: (val: string) => Promise<void>;
  terminalWidth: number;
}) => {
  const [input, setInput] = useState('');

  if (isAfkMode || isProcessing) {
    return null;
  }

  const onSubmit = async (value: string) => {
    setInput('');  // Clear input immediately
    await handleSubmit(value);
  };

  return (
    <Box flexDirection="column">
      <Text color="cyan" wrap="truncate">
        {'─'.repeat(terminalWidth)}
      </Text>
      <Box flexDirection="row">
        <Text bold>Admiral&gt; </Text>
        <TextInput value={input} onChange={setInput} onSubmit={onSubmit} />
      </Box>
      <Text color="cyan" wrap="truncate">
        {'─'.repeat(terminalWidth)}
      </Text>
    </Box>
  );
}, (prevProps, nextProps) => {
  // Only re-render if these specific props change (NOT input!)
  return (
    prevProps.isProcessing === nextProps.isProcessing &&
    prevProps.isAfkMode === nextProps.isAfkMode &&
    prevProps.terminalWidth === nextProps.terminalWidth
  );
});

InputSection.displayName = 'InputSection';

// Component to handle ESC key input - only rendered when stdin supports raw mode
const EscapeKeyHandler: React.FC<{
  isProcessing: boolean;
  cancelRef: React.MutableRefObject<boolean>;
}> = ({ isProcessing, cancelRef }) => {
  // Only try to use useInput if stdin is a TTY
  if (!process.stdin.isTTY) {
    return null;
  }

  // eslint-disable-next-line react-hooks/rules-of-hooks
  useInput((_input, key) => {
    if (key.escape && isProcessing) {
      cancelRef.current = true;
    }
  });

  return null;
};

// Component to handle Ctrl-O toggle for subagent output visibility
const SubagentToggleHandler: React.FC<{
  onToggle: () => void;
}> = ({ onToggle }) => {
  // Only try to use useInput if stdin is a TTY
  if (!process.stdin.isTTY) {
    return null;
  }

  // eslint-disable-next-line react-hooks/rules-of-hooks
  useInput((_input, key) => {
    if (key.ctrl && _input === 'o') {
      onToggle();
    }
  });

  return null;
};

// Memoized message display using Static to prevent re-rendering old messages
const MessageDisplay = React.memo(({
  messages,
  userCommands,
  showSubagentOutput
}: {
  messages: SDKMessage[];
  userCommands: string[];
  showSubagentOutput: boolean;
}) => {
  // Build combined array of user commands and messages for Static
  const displayItems: Array<{type: 'command' | 'message', content: any, messageIndex: number, commandIndex: number}> = [];
  let commandIndex = 0;

  messages.forEach((message, index) => {
    // Show user command at the START of each new assistant response sequence
    // (i.e., when we see an assistant message that's NOT preceded by another assistant message)
    if (message.type === 'assistant') {
      const isStartOfResponse = index === 0 || messages[index - 1]?.type !== 'assistant';

      if (isStartOfResponse && commandIndex < userCommands.length) {
        const userCommand = userCommands[commandIndex];
        displayItems.push({
          type: 'command',
          content: userCommand,
          messageIndex: index,
          commandIndex
        });
        commandIndex++;
      }
    }

    displayItems.push({
      type: 'message',
      content: message,
      messageIndex: index,
      commandIndex
    });
  });

  return (
    <Box flexDirection="column" marginBottom={1}>
      <Static items={displayItems}>
        {(item) => {
          if (item.type === 'command') {
            return (
              <Box marginBottom={1} key={`cmd-${item.commandIndex}`}>
                <Text bold>Admiral&gt; </Text>
                <Text>{item.content}</Text>
              </Box>
            );
          }

          // Determine if we're in a subagent context
          const message = item.content;
          const index = item.messageIndex;
          let isInSubagent = false;

          if (message.type === 'assistant' && index > 0) {
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
                const hasText = content.some((block: any) => block.type === 'text');
                if (hasText) {
                  break;
                }
              }
            }
          }

          return <Message key={`msg-${index}`} message={message} isInSubagent={isInSubagent} showSubagentOutput={showSubagentOutput} allMessages={messages} messageIndex={index} />;
        }}
      </Static>
    </Box>
  );
}, (prevProps, nextProps) => {
  return (
    prevProps.messages === nextProps.messages &&
    prevProps.userCommands === nextProps.userCommands &&
    prevProps.showSubagentOutput === nextProps.showSubagentOutput
  );
});

MessageDisplay.displayName = 'MessageDisplay';

// Memoized header to prevent re-renders
const Header = React.memo(({
  terminalWidth,
  memoryStatus
}: {
  terminalWidth: number;
  memoryStatus: string;
}) => (
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
));

Header.displayName = 'Header';

// Memoized processing indicator
const ProcessingIndicator = React.memo(({
  isProcessing
}: {
  isProcessing: boolean;
}) => {
  if (!isProcessing) {
    return null;
  }

  return (
    <Box marginBottom={1}>
      <Text color="cyan">
        <Spinner type="dots" /> TARS is processing...{' '}
        <Text dimColor>(ESC to interrupt)</Text>
      </Text>
    </Box>
  );
});

ProcessingIndicator.displayName = 'ProcessingIndicator';

// Memoized streaming text display
const StreamingTextDisplay = React.memo((({
  streamingText
}: {
  streamingText: string;
}) => {
  if (!streamingText) {
    return null;
  }

  return (
    <Box marginBottom={1}>
      <Text>{streamingText}</Text>
    </Box>
  );
}));

StreamingTextDisplay.displayName = 'StreamingTextDisplay';

// Memoized AFK indicator
const AfkIndicator = React.memo(({
  isAfkMode,
  isProcessing,
  terminalWidth
}: {
  isAfkMode: boolean;
  isProcessing: boolean;
  terminalWidth: number;
}) => {
  if (!isAfkMode || isProcessing) {
    return null;
  }

  return (
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
  );
});

AfkIndicator.displayName = 'AfkIndicator';

// Memoized help text
const HelpText = React.memo(({
  isAfkMode,
  showSubagentOutput
}: {
  isAfkMode: boolean;
  showSubagentOutput: boolean;
}) => {
  if (isAfkMode) {
    return null;
  }

  return (
    <Box flexDirection="column" marginTop={1}>
      <Text dimColor>
        Commands: exit/quit | /clear-memory | /afk [hours] [checkin_min] (default: /afk 4 30)
      </Text>
      <Text dimColor>
        Shortcuts: Ctrl-O (subagent details: {showSubagentOutput ? '👁️ VISIBLE' : '🙈 HIDDEN'})
      </Text>
    </Box>
  );
});

HelpText.displayName = 'HelpText';

export const App: React.FC<AppProps> = ({
  onCommand,
  messages,
  isProcessing,
  memoryStatus,
  onExit,
  cancelRef,
  isAfkMode = false,
  userCommands = [],
  showSubagentOutput,
  onToggleSubagentOutput,
  streamingText
}) => {
  const { stdout } = useStdout();
  const [terminalWidth, setTerminalWidth] = useState(stdout?.columns || 80);

  // Update terminal width on resize (debounced to reduce flicker)
  useEffect(() => {
    let resizeTimeout: NodeJS.Timeout;

    const handleResize = () => {
      clearTimeout(resizeTimeout);
      resizeTimeout = setTimeout(() => {
        setTerminalWidth(stdout?.columns || 80);
      }, 300);  // Debounce resize for 300ms
    };

    stdout?.on('resize', handleResize);

    return () => {
      stdout?.off('resize', handleResize);
      clearTimeout(resizeTimeout);
    };
  }, [stdout]);

  // Memoize handleSubmit to prevent InputSection from re-rendering
  const handleSubmit = useCallback(async (value: string) => {
    if (!value.trim()) {
      return;
    }

    // Handle special commands
    if (value.toLowerCase() === 'exit' || value.toLowerCase() === 'quit') {
      onExit();
      return;
    }

    await onCommand(value);
  }, [onCommand, onExit]);

  return (
    <Box flexDirection="column">
      <EscapeKeyHandler isProcessing={isProcessing} cancelRef={cancelRef} />
      <SubagentToggleHandler onToggle={onToggleSubagentOutput} />
      <Header terminalWidth={terminalWidth} memoryStatus={memoryStatus} />
      <MessageDisplay messages={messages} userCommands={userCommands} showSubagentOutput={showSubagentOutput} />
      <StreamingTextDisplay streamingText={streamingText} />
      <ProcessingIndicator isProcessing={isProcessing} />
      <AfkIndicator isAfkMode={isAfkMode} isProcessing={isProcessing} terminalWidth={terminalWidth} />
      <InputSection
        isProcessing={isProcessing}
        isAfkMode={isAfkMode}
        handleSubmit={handleSubmit}
        terminalWidth={terminalWidth}
      />
      <HelpText isAfkMode={isAfkMode} showSubagentOutput={showSubagentOutput} />
    </Box>
  );
};
