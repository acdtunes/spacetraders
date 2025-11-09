#!/usr/bin/env python3
"""
Migrate data from SQLite to PostgreSQL

This script transfers all data from the SQLite database to PostgreSQL.
It handles the migration table by table to avoid memory issues with large datasets.
"""
import os
import sys
import sqlite3
import psycopg2
from psycopg2.extras import execute_batch

# Database connection info
SQLITE_DB = "var/spacetraders.db"
POSTGRES_URL = "postgresql://spacetraders:dev_password@localhost:5432/spacetraders"

# Tables to migrate in order (respecting foreign keys)
# Note: 'ships' table is skipped - ShipRepository is API-only and doesn't use database
TABLES = [
    'players',
    'containers',
    'container_logs',
    'routes',
    'waypoints',
    'system_graphs',
    'market_data',
    'ship_assignments',
    'contracts',
]


def parse_postgres_url(url):
    """Parse PostgreSQL URL into connection parameters"""
    # postgresql://user:password@host:port/database
    url = url.replace('postgresql://', '')
    auth, rest = url.split('@')
    user, password = auth.split(':')
    host_port, database = rest.split('/')
    host, port = host_port.split(':')

    return {
        'user': user,
        'password': password,
        'host': host,
        'port': port,
        'database': database
    }


def get_table_columns(sqlite_conn, table_name):
    """Get column names and types for a table"""
    cursor = sqlite_conn.cursor()
    cursor.execute(f"PRAGMA table_info({table_name})")
    columns = [(row[1], row[2]) for row in cursor.fetchall()]  # (name, type)
    return columns


def convert_row_types(table_name, columns, row):
    """Convert SQLite values to PostgreSQL compatible types"""
    converted = list(row)

    for i, (col_name, col_type) in enumerate(columns):
        # Convert SQLite INTEGER booleans to Python booleans for PostgreSQL
        if col_type == 'BOOLEAN' and isinstance(converted[i], int):
            converted[i] = bool(converted[i])

    return tuple(converted)


def migrate_table(sqlite_conn, pg_conn, table_name, batch_size=1000):
    """Migrate a single table from SQLite to PostgreSQL"""
    print(f"\n📦 Migrating table: {table_name}")

    # Get columns with types
    columns = get_table_columns(sqlite_conn, table_name)
    column_names = [col[0] for col in columns]  # Extract just the names
    column_list = ', '.join(column_names)
    placeholders = ', '.join(['%s'] * len(column_names))

    # Count rows
    sqlite_cursor = sqlite_conn.cursor()
    sqlite_cursor.execute(f"SELECT COUNT(*) FROM {table_name}")
    total_rows = sqlite_cursor.fetchone()[0]

    if total_rows == 0:
        print(f"  ⏭️  Table {table_name} is empty, skipping")
        return

    print(f"  📊 Total rows: {total_rows}")

    # Fetch all data from SQLite
    sqlite_cursor.execute(f"SELECT {column_list} FROM {table_name}")

    # Insert into PostgreSQL in batches (skip duplicates)
    pg_cursor = pg_conn.cursor()
    rows_migrated = 0
    batch = []

    for row in sqlite_cursor:
        # Convert types (e.g., SQLite INTEGER booleans to Python booleans)
        converted_row = convert_row_types(table_name, columns, row)
        batch.append(converted_row)

        if len(batch) >= batch_size:
            # Use ON CONFLICT DO NOTHING to skip existing rows
            insert_sql = f"INSERT INTO {table_name} ({column_list}) VALUES ({placeholders}) ON CONFLICT DO NOTHING"
            execute_batch(pg_cursor, insert_sql, batch)
            rows_migrated += len(batch)
            print(f"  ⏳ Migrated {rows_migrated}/{total_rows} rows...")
            batch = []

    # Insert remaining rows
    if batch:
        insert_sql = f"INSERT INTO {table_name} ({column_list}) VALUES ({placeholders}) ON CONFLICT DO NOTHING"
        execute_batch(pg_cursor, insert_sql, batch)
        rows_migrated += len(batch)

    pg_conn.commit()
    print(f"  ✅ Migrated {rows_migrated} rows (duplicates skipped)")


def reset_postgres_sequences(pg_conn):
    """Reset PostgreSQL sequences to match the current max IDs"""
    print("\n🔄 Resetting PostgreSQL sequences...")

    cursor = pg_conn.cursor()

    # Tables with auto-increment primary keys
    sequences = {
        'players': 'player_id',
        'containers': 'id',
        'container_logs': 'id',
        'routes': 'id',
        'waypoints': 'id',
        'system_graphs': 'id',
        'market_data': 'id',
        'contracts': 'id',
    }

    for table, id_column in sequences.items():
        try:
            # Get max ID
            cursor.execute(f"SELECT MAX({id_column}) FROM {table}")
            max_id = cursor.fetchone()[0]

            if max_id is not None:
                # Reset sequence
                sequence_name = f"{table}_{id_column}_seq"
                cursor.execute(f"SELECT setval('{sequence_name}', {max_id})")
                print(f"  ✅ Reset {sequence_name} to {max_id}")
        except Exception as e:
            print(f"  ⚠️  Could not reset sequence for {table}: {e}")

    pg_conn.commit()


def main():
    print("🚀 Starting SQLite to PostgreSQL migration")
    print("=" * 60)

    # Check SQLite database exists
    if not os.path.exists(SQLITE_DB):
        print(f"❌ SQLite database not found: {SQLITE_DB}")
        sys.exit(1)

    # Connect to databases
    print("\n📡 Connecting to databases...")
    try:
        sqlite_conn = sqlite3.connect(SQLITE_DB)
        print("  ✅ Connected to SQLite")

        pg_params = parse_postgres_url(POSTGRES_URL)
        pg_conn = psycopg2.connect(**pg_params)
        print("  ✅ Connected to PostgreSQL")
    except Exception as e:
        print(f"❌ Connection failed: {e}")
        sys.exit(1)

    try:
        # Migrate each table
        for table in TABLES:
            try:
                migrate_table(sqlite_conn, pg_conn, table)
            except Exception as e:
                print(f"  ❌ Error migrating {table}: {e}")
                raise

        # Reset sequences
        reset_postgres_sequences(pg_conn)

        print("\n" + "=" * 60)
        print("✅ Migration completed successfully!")

    except Exception as e:
        print(f"\n❌ Migration failed: {e}")
        pg_conn.rollback()
        sys.exit(1)
    finally:
        sqlite_conn.close()
        pg_conn.close()


if __name__ == '__main__':
    main()
