-- Fix contracts table primary key to use only contract id (not composite with player_id)
-- Contract IDs from SpaceTraders API are globally unique, not scoped per player

-- Step 1: Drop the existing composite primary key
ALTER TABLE contracts DROP CONSTRAINT contracts_pkey;

-- Step 2: Add a new primary key on just the id column
ALTER TABLE contracts ADD PRIMARY KEY (id);
