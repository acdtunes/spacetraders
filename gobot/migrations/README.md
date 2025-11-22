# Database Migrations

This directory contains SQL migration scripts for schema changes that cannot be handled by GORM's AutoMigrate.

## Migration Files

- `000_cleanup_orphaned_data.sql` - Removes orphaned data before adding foreign keys
- `001_normalize_id_columns.up.sql` - Renames primary key columns to use `id` instead of `{table}_id`
- `001_normalize_id_columns.down.sql` - Rollback for column renaming
- `002_add_foreign_key_constraints.up.sql` - Adds foreign key constraints for referential integrity
- `002_add_foreign_key_constraints.down.sql` - Rollback for foreign key constraints
- `003_drop_operation_column.up.sql` - Drops redundant `operation` column from `ship_assignments`
- `003_drop_operation_column.down.sql` - Rollback for operation column removal
- `004_add_mining_tables.up.sql` - Adds mining operations tables
- `004_add_mining_tables.down.sql` - Rollback for mining tables
- `005_drop_contract_purchase_history.up.sql` - Drops contract purchase history tables
- `005_drop_contract_purchase_history.down.sql` - Rollback for contract purchase history removal
- `006_add_metadata_column.up.sql` - Adds metadata JSONB column to container_logs table
- `006_add_metadata_column.down.sql` - Rollback for metadata column

## Running Migrations

### PostgreSQL Production Database

```bash
# Apply migrations (up)
psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -f migrations/000_cleanup_orphaned_data.sql
psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -f migrations/001_normalize_id_columns.up.sql
psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -f migrations/002_add_foreign_key_constraints.up.sql
psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -f migrations/003_drop_operation_column.up.sql
psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -f migrations/004_add_mining_tables.up.sql
psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -f migrations/005_drop_contract_purchase_history.up.sql
psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -f migrations/006_add_metadata_column.up.sql

# Rollback migrations (down) - run in reverse order
psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -f migrations/006_add_metadata_column.down.sql
psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -f migrations/005_drop_contract_purchase_history.down.sql
psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -f migrations/004_add_mining_tables.down.sql
psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -f migrations/003_drop_operation_column.down.sql
psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -f migrations/002_add_foreign_key_constraints.down.sql
psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -f migrations/001_normalize_id_columns.down.sql
```

### Using Environment Variables

```bash
# Set your database credentials
export DB_HOST=localhost
export DB_PORT=5432
export DB_USER=your_user
export DB_NAME=spacetraders
export PGPASSWORD=your_password

# Apply migrations
psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -f migrations/000_cleanup_orphaned_data.sql
psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -f migrations/001_normalize_id_columns.up.sql
psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -f migrations/002_add_foreign_key_constraints.up.sql
psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -f migrations/003_drop_operation_column.up.sql
psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -f migrations/004_add_mining_tables.up.sql
psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -f migrations/005_drop_contract_purchase_history.up.sql
psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -f migrations/006_add_metadata_column.up.sql
```

## Testing

For test databases using SQLite in-memory, these migrations are **not needed**. The `AutoMigrate()` function in `internal/infrastructure/database/connection.go` will create the schema with the correct column names and constraints automatically.

## Migration Order

**Important**: Always run migrations in numerical order!

1. First run `000_cleanup_orphaned_data.sql`
2. Then run `001_normalize_id_columns.up.sql`
3. Then run `002_add_foreign_key_constraints.up.sql`
4. Then run `003_drop_operation_column.up.sql`
5. Then run `004_add_mining_tables.up.sql`
6. Then run `005_drop_contract_purchase_history.up.sql`
7. Finally run `006_add_metadata_column.up.sql`

When rolling back, run in **reverse order**:

1. First run `006_add_metadata_column.down.sql`
2. Then run `005_drop_contract_purchase_history.down.sql`
3. Then run `004_add_mining_tables.down.sql`
4. Then run `003_drop_operation_column.down.sql`
5. Then run `002_add_foreign_key_constraints.down.sql`
6. Finally run `001_normalize_id_columns.down.sql`

## Notes

- GORM's AutoMigrate **cannot rename columns** - that's why these SQL migrations are necessary
- PostgreSQL automatically updates foreign key references when renaming columns
- The foreign key constraints use `CASCADE` for updates and deletes, except for `ship_assignments.container_id` which uses `SET NULL` on delete
- Always backup your database before running migrations!

## Future Migrations

To add new migrations, follow this naming convention:

```
{number}_{description}.up.sql    # Apply migration
{number}_{description}.down.sql  # Rollback migration
```

Example:
```
003_add_user_preferences_table.up.sql
003_add_user_preferences_table.down.sql
```
