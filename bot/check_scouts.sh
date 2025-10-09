#!/bin/bash

cd /Users/andres.camacho/Development/Personal/spacetradersV2/bot

for scout in 2 3 4 5 6 7 8 9 A B C; do
  echo "=== SCOUT-$scout ==="
  log_file=$(ls -t var/daemons/logs/scout-$scout-*.log 2>/dev/null | head -1)
  if [ -n "$log_file" ]; then
    grep -A 20 "MARKET SCOUT TOUR" "$log_file" | grep -E "(Markets to visit|Total time|Route order:|^  [0-9])" | head -20
  else
    echo "No log file found"
  fi
  echo ""
done
