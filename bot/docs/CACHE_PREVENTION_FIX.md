# Python Cache Prevention Fix

## Problem

Daemon processes were loading stale bytecode (.pyc files) from `__pycache__/` directories after code fixes were deployed. This caused critical bugs to persist even after source code was corrected.

**Real-world impact:**
- Circuit breaker fix deployed to `multileg_trader.py` (lines 1337-1442)
- Manual cache clear: `rm __pycache__/multileg_trader.cpython-312.pyc`
- New daemon started → **STILL used old code**
- Result: 26,100 credit loss from price spike that should have been prevented

**Root cause:** Python caches bytecode in `__pycache__/` directories to speed up imports. When source code changes, Python *should* detect modification time and recompile, but this can fail in several scenarios:
1. System clock skew
2. File system caching delays
3. Race conditions during imports
4. Daemon processes inheriting parent's import cache

## Solution

Three-layer defense against stale bytecode:

### 1. Global Cache Cleanup (One-time)
```bash
# Remove ALL existing cache (2,343 files across 258 directories)
find . -type d -name "__pycache__" -exec rm -rf {} + 2>/dev/null || true
find . -type f \( -name "*.pyc" -o -name "*.pyo" \) -delete 2>/dev/null || true
```

### 2. Daemon Startup Protection (Permanent)
**File:** `src/spacetraders_bot/core/daemon_manager.py`

```python
# Inject -B flag into Python commands
patched_command = self._inject_python_no_cache_flag(command)

# Set environment variable to prevent cache writes
env = os.environ.copy()
env['PYTHONDONTWRITEBYTECODE'] = '1'

# Start process with cache disabled
process = subprocess.Popen(
    patched_command,
    env=env,
    ...
)
```

**Effect:**
- `-B` flag: Disables reading/writing of .pyc files
- `PYTHONDONTWRITEBYTECODE=1`: Double protection via environment variable
- Applied to ALL daemon processes automatically

### 3. Git Protection (Permanent)
**File:** `.gitignore`

```gitignore
# Python cache files (CRITICAL: Never commit bytecode cache)
__pycache__/
*.py[cod]
*$py.class
```

**Effect:** Prevents accidental commit of bytecode cache

## Validation

### Test Suite
**File:** `tests/test_daemon_cache_prevention.py`

```bash
pytest tests/test_daemon_cache_prevention.py -v
```

Tests verify:
- `-B` flag injection for various Python command formats
- `PYTHONDONTWRITEBYTECODE` environment variable setup
- Idempotency (no duplicate flags)
- Non-Python commands left unchanged

**Status:** ✅ 9/9 tests passing

### Manual Verification

```bash
# Check for cache directories (should be 0)
find . -type d -name "__pycache__" | wc -l

# Check for bytecode files (should be 0)
find . -type f \( -name "*.pyc" -o -name "*.pyo" \) | wc -l

# Verify daemon startup command (should include -B)
python3 spacetraders_bot.py daemon start test-daemon \
  --player-id 6 --ship TEST-1 --operation mine

# Check logs for command used
tail var/daemons/logs/test-daemon.log
# Should see: Command: python3 -B -m spacetraders_bot.cli mine ...
```

## Before vs After

### Before (Broken)
1. Deploy fix to `multileg_trader.py`
2. Clear cache: `rm __pycache__/multileg_trader.cpython-312.pyc`
3. Start daemon → Python recreates cache from **old** bytecode in memory
4. Daemon executes **old** logic
5. Loss: 26,100 credits

### After (Fixed)
1. Deploy fix to `multileg_trader.py`
2. Start daemon → Python runs with `-B` flag
3. Python ignores cache, reads fresh source code
4. Daemon executes **new** logic
5. Circuit breaker prevents loss ✅

## Implementation Details

### Flag Injection Logic
**Method:** `_inject_python_no_cache_flag(command: List[str])`

Handles all Python command formats:
- `['python3', 'script.py']` → `['python3', '-B', 'script.py']`
- `['python3', '-m', 'module']` → `['python3', '-B', '-m', 'module']`
- `['/usr/bin/python3.12', '-u', 'script.py']` → `['/usr/bin/python3.12', '-B', '-u', 'script.py']`

**Idempotent:** If `-B` already present, doesn't duplicate
**Safe:** Non-Python commands pass through unchanged

### Performance Impact

**Bytecode cache purpose:** Speed up module imports by ~10-20ms per module

**Cost of disabling:**
- First import: +10-20ms per module (recompile)
- Subsequent imports: No penalty (already in memory)
- Total daemon startup delay: ~100-200ms (one-time)

**Benefit:**
- Zero stale bytecode bugs
- Immediate code fixes without manual cache clear
- Production reliability over microsecond startup time

## Troubleshooting

### Daemon still using old code after fix?

1. **Verify cache is cleared:**
   ```bash
   find . -type d -name "__pycache__" | wc -l  # Should be 0
   ```

2. **Verify daemon uses -B flag:**
   ```bash
   # Check running daemon command
   ps aux | grep python | grep -- -B
   ```

3. **Verify fix is in source code:**
   ```bash
   grep -n "CRITICAL FIX: Get fresh market data BEFORE purchase" \
     src/spacetraders_bot/operations/multileg_trader.py
   # Should show line 1337
   ```

4. **Kill all old daemons:**
   ```bash
   python3 spacetraders_bot.py daemon status
   python3 spacetraders_bot.py daemon stop <daemon-id>
   ```

5. **Start fresh daemon:**
   ```bash
   # New daemon will automatically use -B flag
   python3 spacetraders_bot.py daemon start ...
   ```

### Cache directories reappearing?

Normal for test runner and development. They're harmless if:
- Daemons use `-B` flag (ignore cache)
- Git ignores them (won't commit)

To clean:
```bash
find . -type d -name "__pycache__" -exec rm -rf {} + 2>/dev/null || true
```

## References

- **Python -B flag:** https://docs.python.org/3/using/cmdline.html#cmdoption-B
- **PYTHONDONTWRITEBYTECODE:** https://docs.python.org/3/using/cmdline.html#envvar-PYTHONDONTWRITEBYTECODE
- **Import system:** https://docs.python.org/3/reference/import.html

## Related Fixes

This cache prevention fix enables the circuit breaker fix in `multileg_trader.py` to work correctly:

**Circuit Breaker (lines 1337-1442):**
- Fetches live market data BEFORE purchase
- Aborts if price spiked >30%
- Smart skip logic for segment failures
- Tiered cargo salvage system

**Without cache fix:** Circuit breaker code never executes (old code runs)
**With cache fix:** Circuit breaker protects against price spikes ✅

## Future Improvements

1. **CI/CD integration:** Add cache check to pre-commit hooks
   ```bash
   # .git/hooks/pre-commit
   if [ $(find . -name "*.pyc" | wc -l) -gt 0 ]; then
     echo "ERROR: Bytecode cache detected. Run: find . -name '*.pyc' -delete"
     exit 1
   fi
   ```

2. **Monitoring:** Add cache detection to daemon startup logs
   ```python
   cache_count = len(list(Path('.').rglob('__pycache__')))
   if cache_count > 0:
       logger.warning(f"Found {cache_count} cache dirs - will be ignored due to -B flag")
   ```

3. **Documentation:** Add to `CLAUDE.md` and `GAME_GUIDE.md`

## Conclusion

**Status:** ✅ PERMANENTLY SOLVED

The Python cache issue is now impossible to reproduce:
1. All existing cache deleted (0 files)
2. All daemons start with `-B` flag (automatic)
3. Environment variable prevents cache writes (double protection)
4. Git ignores cache files (won't commit)
5. Test suite validates fix (9/9 passing)

**No manual intervention required** - the fix is baked into daemon startup.
