-- Rollback: Restore composite primary key on contracts table

-- Step 1: Drop the single-column primary key
ALTER TABLE contracts DROP CONSTRAINT contracts_pkey;

-- Step 2: Restore the composite primary key
ALTER TABLE contracts ADD PRIMARY KEY (id, player_id);
