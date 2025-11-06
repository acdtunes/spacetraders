/**
 * Simple Markdown renderer for Ink
 * Handles common markdown formatting without external dependencies
 */

import React from 'react';
import { Box, Text } from 'ink';

interface MarkdownTextProps {
  children: string;
}

export const MarkdownText: React.FC<MarkdownTextProps> = ({ children }) => {
  const lines = children.split('\n');
  return (
    <Box flexDirection="column">
      {lines.map((line, index) => (
        <MarkdownLine key={index} line={line} />
      ))}
    </Box>
  );
};

const MarkdownLine: React.FC<{ line: string }> = ({ line }) => {
  // Headers
  if (line.startsWith('#### ')) {
    return <Text bold color="cyan">{line.substring(5)}</Text>;
  }
  if (line.startsWith('### ')) {
    return <Text bold color="cyan">{line.substring(4)}</Text>;
  }
  if (line.startsWith('## ')) {
    return <Text bold color="cyan">{line.substring(3)}</Text>;
  }
  if (line.startsWith('# ')) {
    return <Text bold color="green">{line.substring(2)}</Text>;
  }

  // Horizontal rules
  if (line.match(/^[═─]{3,}$/)) {
    return <Text color="cyan">{line}</Text>;
  }

  // Lists
  if (line.match(/^\s*[-*+]\s/)) {
    return <Text>{renderInlineFormatting(line)}</Text>;
  }
  if (line.match(/^\s*\d+\.\s/)) {
    return <Text>{renderInlineFormatting(line)}</Text>;
  }

  // Code blocks (simple detection)
  if (line.startsWith('```')) {
    return <Text dimColor>{line}</Text>;
  }

  // Empty lines
  if (line.trim() === '') {
    return <Text> </Text>;
  }

  // Regular text with inline formatting
  return <Text>{renderInlineFormatting(line)}</Text>;
};

const renderInlineFormatting = (text: string): (string | React.ReactElement)[] | string => {
  const parts: (string | React.ReactElement)[] = [];
  let currentIndex = 0;

  // Regex to match **bold**, *italic*, `code`, and emoji
  const regex = /(\*\*[^*]+\*\*|\*[^*]+\*|`[^`]+`|[🔔💭🔧👤📦⚠️🤖✅❌⏱️🚀])/g;
  let match: RegExpExecArray | null;

  while ((match = regex.exec(text)) !== null) {
    // Add text before the match
    if (match.index > currentIndex) {
      parts.push(text.substring(currentIndex, match.index));
    }

    const matched = match[0];

    // Bold
    if (matched.startsWith('**') && matched.endsWith('**')) {
      parts.push(<Text key={match.index} bold>{matched.slice(2, -2)}</Text>);
    }
    // Italic
    else if (matched.startsWith('*') && matched.endsWith('*')) {
      parts.push(<Text key={match.index} italic>{matched.slice(1, -1)}</Text>);
    }
    // Code
    else if (matched.startsWith('`') && matched.endsWith('`')) {
      parts.push(<Text key={match.index} color="magenta">{matched.slice(1, -1)}</Text>);
    }
    // Emoji (keep as-is)
    else {
      parts.push(matched);
    }

    currentIndex = match.index + matched.length;
  }

  // Add remaining text
  if (currentIndex < text.length) {
    parts.push(text.substring(currentIndex));
  }

  return parts.length > 0 ? parts : text;
};
