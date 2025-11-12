# Configuration Management

The SpaceTraders Go bot uses a flexible configuration system based on [Viper](https://github.com/spf13/viper) that supports multiple configuration sources with clear priority ordering.

## Configuration Priority

Configuration is loaded from multiple sources with the following priority (highest to lowest):

1. **Environment variables** (with `ST_` prefix)
2. **Config file** (`config.yaml`)
3. **Default values**

This allows you to:
- Use config files for base configuration
- Override specific settings with environment variables
- Deploy to different environments without changing code

## Configuration File

### Location

The config file is searched in the following locations (in order):

1. `./config.yaml` (current directory)
2. `./configs/config.yaml`
3. `/etc/spacetraders/config.yaml`

You can also specify a custom path:
```bash
./spacetraders-daemon --config /path/to/config.yaml
```

### Format

Create a `config.yaml` file in your project root. See `config.yaml.example` for a complete template:

```yaml
database:
  type: postgres
  url: postgresql://spacetraders:dev_password@localhost:5432/spacetraders
  # Or use individual fields:
  # host: localhost
  # port: 5432
  # user: spacetraders
  # password: dev_password
  # name: spacetraders

  pool:
    max_open: 25
    max_idle: 5
    max_lifetime: 5m

api:
  base_url: https://api.spacetraders.io/v2
  timeout: 30s
  rate_limit:
    requests: 2
    burst: 10

routing:
  address: localhost:50051
  timeout:
    tsp: 60s
    vrp: 120s

daemon:
  address: localhost:50052
  socket_path: /tmp/spacetraders-daemon.sock
  max_containers: 100

logging:
  level: info
  format: json
  output: stdout
```

## Environment Variables

### Special Variables

#### DATABASE_URL

The `DATABASE_URL` environment variable is a special case that doesn't require the `ST_` prefix:

```bash
export DATABASE_URL=postgresql://user:password@host:port/database
```

This takes precedence over individual database fields.

#### SPACETRADERS_SOCKET

Used by CLI commands to locate the daemon:

```bash
export SPACETRADERS_SOCKET=/tmp/spacetraders-daemon.sock
```

### Standard Variables

All other configuration options use the `ST_` prefix with underscores replacing dots:

```bash
# Database (if not using DATABASE_URL)
export ST_DATABASE_TYPE=postgres
export ST_DATABASE_HOST=localhost
export ST_DATABASE_PORT=5432
export ST_DATABASE_USER=spacetraders
export ST_DATABASE_PASSWORD=dev_password
export ST_DATABASE_NAME=spacetraders

# API
export ST_API_BASE_URL=https://api.spacetraders.io/v2
export ST_API_TIMEOUT=30s

# Routing
export ST_ROUTING_ADDRESS=localhost:50051

# Daemon
export ST_DAEMON_SOCKET_PATH=/tmp/spacetraders-daemon.sock
export ST_DAEMON_MAX_CONTAINERS=100

# Logging
export ST_LOGGING_LEVEL=debug
export ST_LOGGING_FORMAT=json
```

## .env File

You can use a `.env` file in your project root for local development:

```bash
# Copy example file
cp .env.example .env

# Edit with your settings
vim .env
```

The `.env` file is loaded automatically when the daemon starts.

## Configuration Sections

### Database

```yaml
database:
  type: postgres          # postgres or sqlite
  url: ""                 # Full connection URL (optional)
  host: localhost         # Used if url is empty
  port: 5432
  user: spacetraders
  password: dev_password
  name: spacetraders
  sslmode: disable        # disable, require, verify-ca, verify-full

  # For SQLite
  path: ./spacetraders.db # Use ":memory:" for in-memory

  pool:
    max_open: 25          # Maximum open connections
    max_idle: 5           # Maximum idle connections
    max_lifetime: 5m      # Connection lifetime
```

### API

```yaml
api:
  base_url: https://api.spacetraders.io/v2
  timeout: 30s

  rate_limit:
    requests: 2           # Requests per second
    burst: 10             # Burst capacity

  retry:
    max_attempts: 3       # Maximum retry attempts
    backoff_base: 1s      # Base backoff duration
```

### Routing Service

```yaml
routing:
  address: localhost:50051

  timeout:
    connect: 10s
    dijkstra: 30s
    tsp: 60s
    vrp: 120s
```

### Daemon

```yaml
daemon:
  address: localhost:50052
  socket_path: /tmp/spacetraders-daemon.sock
  pid_file: /tmp/spacetraders-daemon.pid
  max_containers: 100
  health_check_interval: 30s
  shutdown_timeout: 30s

  restart_policy:
    enabled: true
    max_attempts: 3
    delay: 5s
    backoff_multiplier: 2.0
```

### Logging

```yaml
logging:
  level: info             # debug, info, warn, error
  format: json            # json, text
  output: stdout          # stdout, stderr, file

  file_path: /var/log/spacetraders/daemon.log

  rotation:
    enabled: true
    max_size: 100         # MB
    max_backups: 3
    max_age: 28           # days
    compress: true

  include_caller: false
  include_stacktrace: false
```

## Viewing Current Configuration

Use the CLI to view your current configuration:

```bash
spacetraders config show
```

This displays:
- User preferences (default player)
- Database settings
- API configuration
- Routing service settings
- Daemon configuration
- Logging settings

## Configuration Validation

The configuration system automatically validates all settings on load:

- Required fields are checked
- Port numbers must be in valid range (1-65535)
- URLs must be properly formatted
- Durations must be parseable (e.g., `30s`, `5m`, `1h`)
- Enum values must match allowed options

If validation fails, you'll see a detailed error message indicating which field is invalid.

## Example Configurations

### Development (SQLite)

```yaml
database:
  type: sqlite
  path: ./dev.db

logging:
  level: debug
  format: text
  output: stdout
```

### Production (PostgreSQL)

```bash
# .env file
DATABASE_URL=postgresql://spacetraders:secure_password@db.example.com:5432/spacetraders?sslmode=require

ST_LOGGING_LEVEL=info
ST_LOGGING_FORMAT=json
ST_LOGGING_OUTPUT=file
ST_LOGGING_FILE_PATH=/var/log/spacetraders/daemon.log
ST_LOGGING_ROTATION_ENABLED=true
```

### Docker Compose

```yaml
services:
  daemon:
    environment:
      - DATABASE_URL=postgresql://spacetraders:dev_password@postgres:5432/spacetraders
      - ST_ROUTING_ADDRESS=routing:50051
      - ST_DAEMON_SOCKET_PATH=/var/run/spacetraders.sock
      - ST_LOGGING_LEVEL=info
```

## Troubleshooting

### Connection Issues

If you see database connection errors:

1. Verify DATABASE_URL or individual database fields
2. Check database is running: `pg_isready -h localhost -p 5432`
3. Test credentials: `psql -h localhost -U spacetraders -d spacetraders`

### Configuration Not Loading

If environment variables aren't taking effect:

1. Ensure `ST_` prefix is used (except DATABASE_URL)
2. Restart the daemon after changing config
3. Check for typos in variable names (use underscores, not dots)
4. Run `spacetraders config show` to verify settings

### Validation Errors

If you see validation errors on startup:

1. Check the error message for the specific field
2. Verify the value matches expected format
3. Refer to `config.yaml.example` for correct structure
4. Ensure required fields are present

## Next Steps

- Learn about [Player Management](PLAYER_MANAGEMENT.md)
- See [Deployment Guide](DEPLOYMENT.md) for production setup
- Review [config.yaml.example](../config.yaml.example) for all options
