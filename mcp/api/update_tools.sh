#!/bin/bash
# This script adds player_id parameter to all tools in index.ts

input_file="src/index.ts"
temp_file="src/index.ts.tmp"

# Use sed to add player_id parameter to all tool definitions
# This adds it right after "properties: {" in each tool
sed 's/properties: {$/properties: {\
        player_id: {\
          type: "number",\
          description: "Player ID from database (optional, fetches token from database)",\
        },/g' "$input_file" > "$temp_file"

# Check if changes were made
if diff -q "$input_file" "$temp_file" > /dev/null 2>&1; then
    echo "No changes needed"
    rm "$temp_file"
else
    mv "$temp_file" "$input_file"
    echo "Updated tool definitions with player_id parameter"
fi
