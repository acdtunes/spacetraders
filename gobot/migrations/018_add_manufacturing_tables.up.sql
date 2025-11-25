-- Manufacturing Pipelines Table
-- Represents a complete manufacturing run for one product
CREATE TABLE manufacturing_pipelines (
    id              VARCHAR(64) PRIMARY KEY,
    player_id       INTEGER NOT NULL REFERENCES players(id),
    product_good    VARCHAR(64) NOT NULL,      -- Final product (LASER_RIFLES)
    sell_market     VARCHAR(64) NOT NULL,      -- Target sell market
    expected_price  INTEGER NOT NULL,          -- Expected sale price per unit

    status          VARCHAR(32) NOT NULL,      -- PLANNING, EXECUTING, COMPLETED, FAILED, CANCELLED

    -- Financials
    total_cost      INTEGER DEFAULT 0,         -- Cumulative costs
    total_revenue   INTEGER DEFAULT 0,         -- Revenue from sales
    net_profit      INTEGER DEFAULT 0,         -- Revenue - costs

    -- Error tracking
    error_message   TEXT,

    -- Timestamps
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ,

    CONSTRAINT valid_pipeline_status CHECK (status IN ('PLANNING', 'EXECUTING', 'COMPLETED', 'FAILED', 'CANCELLED'))
);

CREATE INDEX idx_pipelines_status ON manufacturing_pipelines(status);
CREATE INDEX idx_pipelines_player ON manufacturing_pipelines(player_id);
CREATE INDEX idx_pipelines_product ON manufacturing_pipelines(player_id, product_good);

-- Manufacturing Tasks Table
-- Represents a single atomic task in the manufacturing pipeline
CREATE TABLE manufacturing_tasks (
    id              VARCHAR(64) PRIMARY KEY,
    pipeline_id     VARCHAR(64) REFERENCES manufacturing_pipelines(id) ON DELETE CASCADE,
    player_id       INTEGER NOT NULL REFERENCES players(id),

    task_type       VARCHAR(32) NOT NULL,      -- ACQUIRE, DELIVER, COLLECT, SELL, LIQUIDATE
    status          VARCHAR(32) NOT NULL,      -- PENDING, READY, ASSIGNED, EXECUTING, COMPLETED, FAILED

    -- What
    good            VARCHAR(64) NOT NULL,
    quantity        INTEGER DEFAULT 0,         -- Target quantity (0 = fill cargo)
    actual_quantity INTEGER DEFAULT 0,         -- Actual quantity handled

    -- Where
    source_market   VARCHAR(64),               -- For ACQUIRE: where to buy
    target_market   VARCHAR(64),               -- For DELIVER/SELL/LIQUIDATE: destination
    factory_symbol  VARCHAR(64),               -- For COLLECT: factory location

    -- Execution
    assigned_ship   VARCHAR(64),               -- Ship symbol executing this task
    priority        INTEGER DEFAULT 0,         -- Higher = more urgent
    retry_count     INTEGER DEFAULT 0,         -- Number of retries
    max_retries     INTEGER DEFAULT 3,         -- Max retry attempts

    -- Results
    total_cost      INTEGER DEFAULT 0,         -- Cost incurred
    total_revenue   INTEGER DEFAULT 0,         -- Revenue earned
    error_message   TEXT,                      -- Last error if failed

    -- Timestamps
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ready_at        TIMESTAMPTZ,               -- When task became ready
    started_at      TIMESTAMPTZ,               -- When execution began
    completed_at    TIMESTAMPTZ,               -- When completed/failed

    CONSTRAINT valid_task_type CHECK (task_type IN ('ACQUIRE', 'DELIVER', 'COLLECT', 'SELL', 'LIQUIDATE')),
    CONSTRAINT valid_task_status CHECK (status IN ('PENDING', 'READY', 'ASSIGNED', 'EXECUTING', 'COMPLETED', 'FAILED'))
);

CREATE INDEX idx_tasks_pipeline ON manufacturing_tasks(pipeline_id);
CREATE INDEX idx_tasks_status ON manufacturing_tasks(status);
CREATE INDEX idx_tasks_ship ON manufacturing_tasks(assigned_ship);
CREATE INDEX idx_tasks_player_status ON manufacturing_tasks(player_id, status);
CREATE INDEX idx_tasks_ready ON manufacturing_tasks(status, priority DESC) WHERE status = 'READY';

-- Task Dependencies Table
-- Tracks dependencies between tasks
CREATE TABLE manufacturing_task_dependencies (
    task_id         VARCHAR(64) NOT NULL REFERENCES manufacturing_tasks(id) ON DELETE CASCADE,
    depends_on_id   VARCHAR(64) NOT NULL REFERENCES manufacturing_tasks(id) ON DELETE CASCADE,

    PRIMARY KEY (task_id, depends_on_id)
);

CREATE INDEX idx_deps_depends_on ON manufacturing_task_dependencies(depends_on_id);

-- Factory States Table
-- Tracks the state of factories for production monitoring
CREATE TABLE manufacturing_factory_states (
    id              SERIAL PRIMARY KEY,
    factory_symbol  VARCHAR(64) NOT NULL,
    output_good     VARCHAR(64) NOT NULL,
    player_id       INTEGER NOT NULL REFERENCES players(id),
    pipeline_id     VARCHAR(64) REFERENCES manufacturing_pipelines(id) ON DELETE CASCADE,

    -- Input tracking (JSONB for flexibility)
    required_inputs  JSONB NOT NULL,            -- ["DIAMONDS", "PLATINUM", "ADV_CIRCUITRY"]
    delivered_inputs JSONB DEFAULT '{}',        -- {"DIAMONDS": {"delivered": true, "quantity": 40, "ship": "AGENT-1"}}

    -- Production state
    all_inputs_delivered BOOLEAN DEFAULT FALSE,
    current_supply       VARCHAR(32),           -- SCARCE, LIMITED, MODERATE, HIGH, ABUNDANT
    previous_supply      VARCHAR(32),           -- Supply before we delivered
    ready_for_collection BOOLEAN DEFAULT FALSE,

    -- Timestamps
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    inputs_completed_at  TIMESTAMPTZ,
    ready_at             TIMESTAMPTZ,           -- When supply reached HIGH

    UNIQUE(factory_symbol, output_good, pipeline_id)
);

CREATE INDEX idx_factory_pipeline ON manufacturing_factory_states(pipeline_id);
CREATE INDEX idx_factory_pending ON manufacturing_factory_states(ready_for_collection)
    WHERE ready_for_collection = FALSE;
CREATE INDEX idx_factory_player ON manufacturing_factory_states(player_id);
