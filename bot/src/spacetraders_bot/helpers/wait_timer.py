#!/usr/bin/env python3
import time
import sys

def wait_timer(seconds):
    """Wait for a specified number of seconds"""
    print(f"⏳ Waiting for {seconds} seconds...")
    time.sleep(seconds)
    print(f"✅ Wait complete! ({seconds} seconds elapsed)")

if __name__ == "__main__":
    wait_time = int(sys.argv[1]) if len(sys.argv) > 1 else 30
    wait_timer(wait_time)
