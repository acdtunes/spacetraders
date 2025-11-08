# PostgreSQL Support Implementation

## Summary

The Database class now transparently supports both SQLite and PostgreSQL backends, automatically selecting based on the `DATABASE_URL` environment variable.

## Backend Selection

### SQLite (Default)
- **Trigger**: No `DATABASE_URL` environment variable, or non-PostgreSQL URL
- **Use cases**: Testing, local development, in-memory databases
- **Features**:
  - WAL mode for file-based databases
  - In-memory support with persistent connections
  - `AUTOINCREMENT` for primary keys

### PostgreSQL
- **Trigger**: `DATABASE_URL` starts with `postgresql://`
- **Use cases**: Production deployments, concurrent access
- **Features**:
  - Connection pooling (new connection per query)
  - `SERIAL` for primary keys
  - `RealDictCursor` for dict-like row access

## Usage

### SQLite (Default)
```python
from adapters.secondary.persistence.database import Database

# No environment variable needed
db = Database()  # Uses var/spacetraders.db

# Or specify path
db = Database(db_path="custom/path.db")

# Or in-memory
db = Database(db_path=":memory:")
```

### PostgreSQL
```bash
export DATABASE_URL="postgresql://user:password@localhost:5432/database"
```

```python
from adapters.secondary.persistence.database import Database

# Automatically uses PostgreSQL
db = Database()
```

## Docker Compose Setup

PostgreSQL is already configured in `docker-compose.yml`:

```bash
# Start PostgreSQL
docker-compose up -d

# Wait for readiness
docker exec spacetraders-postgres pg_isready -U spacetraders

# Create database (if needed)
docker exec spacetraders-postgres psql -U spacetraders -c "CREATE DATABASE spacetraders;"
```

## Environment Configuration

```bash
# PostgreSQL (production)
export DATABASE_URL="postgresql://spacetraders:dev_password@localhost:5432/spacetraders"

# SQLite (testing) - unset DATABASE_URL or don't set it
unset DATABASE_URL
```

## SQL Compatibility

The Database class handles SQL differences transparently:

| Feature | SQLite | PostgreSQL |
|---------|--------|------------|
| Auto-increment PK | `INTEGER PRIMARY KEY AUTOINCREMENT` | `SERIAL PRIMARY KEY` |
| Parameter placeholder | `?` | `%s` |
| Column introspection | `PRAGMA table_info(table)` | `information_schema.columns` |
| Row factory | `sqlite3.Row` | `psycopg2.extras.RealDictCursor` |

## Testing

All tests use SQLite by default (no `DATABASE_URL` set).

### Run PostgreSQL-specific tests:
```bash
export PYTHONPATH=src:$PYTHONPATH
pytest tests/bdd/steps/infrastructure/test_database_backend_steps.py -v
```

### Manual verification:
```python
import os
os.environ['DATABASE_URL'] = 'postgresql://...'
from adapters.secondary.persistence.database import Database

db = Database()
assert db.backend == 'postgresql'
```

## Implementation Details

### Backend Detection (`__init__`)
1. Check `DATABASE_URL` environment variable
2. If starts with `postgresql://`: set `backend = 'postgresql'`
3. Otherwise: set `backend = 'sqlite'`

### Connection Management (`_get_connection`)
- **PostgreSQL**: Creates new `psycopg2` connection with `RealDictCursor`
- **SQLite**: Returns persistent connection for `:memory:`, new connection for file-based

### Schema Initialization (`_init_database`)
- Tables with auto-increment PKs have separate SQLite/PostgreSQL DDL
- Migrations check column existence using backend-specific queries

### Helper Methods
- `_get_placeholder()`: Returns `'?'` or `'%s'` based on backend
- `_get_sql_type()`: Maps SQLite types to PostgreSQL equivalents

## Migration Path

### Existing SQLite data → PostgreSQL

1. Export SQLite data:
```bash
sqlite3 var/spacetraders.db .dump > backup.sql
```

2. Convert SQLite → PostgreSQL syntax:
   - Replace `AUTOINCREMENT` with `SERIAL`
   - Replace `?` placeholders with `%s`
   - Convert date/time formats

3. Import to PostgreSQL:
```bash
psql -U spacetraders -d spacetraders < converted_backup.sql
```

4. Set `DATABASE_URL` and restart application

## Performance Considerations

### SQLite
- Best for: Single-process access, testing, local development
- WAL mode enables concurrent reads
- File-based persistence with automatic directory creation

### PostgreSQL
- Best for: Multi-process/multi-server deployments
- True concurrent read/write support
- Connection pooling via new connections per query
- Consider external pooling (PgBouncer) for high concurrency

## Troubleshooting

### PostgreSQL connection refused
```bash
# Check container is running
docker ps | grep postgres

# Check logs
docker logs spacetraders-postgres

# Verify network
docker exec spacetraders-postgres pg_isready -U spacetraders
```

### SQLite locked database
- Ensure WAL mode is enabled (automatic for file-based DBs)
- Check no other process has exclusive lock
- Increase timeout: `Database(db_path="path.db")` uses 30s timeout

### Test failures
- Ensure `DATABASE_URL` is not set for test runs
- Tests use in-memory SQLite for isolation
- Reset container between tests: `reset_container()`

## Dependencies

- **SQLite**: Built-in Python `sqlite3` module
- **PostgreSQL**: `psycopg2-binary>=2.9.9` (already in `pyproject.toml`)

## Database Schema

The same schema works for both backends:
- `players` (SERIAL/AUTOINCREMENT PK)
- `system_graphs`
- `routes`
- `ship_assignments`
- `containers` (composite PK)
- `container_logs` (SERIAL/AUTOINCREMENT PK)
- `market_data` (composite PK)
- `contracts` (composite PK)
- `waypoints`

All foreign keys, indexes, and constraints are backend-agnostic.
