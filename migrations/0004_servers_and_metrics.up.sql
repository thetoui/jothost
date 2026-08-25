-- Phase 3: the managed server and its metric history.
-- Implements DATABASE.md tables 8 and 26.

-- ---------------------------------------------------------------- servers

CREATE TABLE servers (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    hostname      VARCHAR(255) NOT NULL,
    os_name       VARCHAR(100),
    os_version    VARCHAR(100),
    kernel        VARCHAR(255),
    architecture  VARCHAR(50),
    ipv4          INET,
    ipv6          INET,
    status        VARCHAR(30) NOT NULL DEFAULT 'unknown',
    agent_version VARCHAR(50),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT servers_status_valid CHECK (status IN ('online', 'offline', 'unknown'))
);

-- The panel manages one host in this phase, identified by hostname. The unique
-- constraint is what makes registration an idempotent upsert rather than a
-- source of duplicate rows on every restart.
CREATE UNIQUE INDEX servers_hostname_idx ON servers (hostname);

-- ---------------------------------------------------------- system_metrics

CREATE TABLE system_metrics (
    id             BIGSERIAL PRIMARY KEY,
    server_id      UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    timestamp      TIMESTAMPTZ NOT NULL,
    cpu_percent    NUMERIC(5, 2),
    memory_percent NUMERIC(5, 2),
    disk_percent   NUMERIC(5, 2),
    load_1         NUMERIC(8, 2),
    load_5         NUMERIC(8, 2),
    load_15        NUMERIC(8, 2),
    -- Cumulative interface counters, not rates: storing the raw counter lets a
    -- rate be recomputed over any window, while storing a rate would lock the
    -- data to the sampling interval it was taken at.
    network_rx     BIGINT,
    network_tx     BIGINT
);

-- Every history query filters by server and orders by time, so the composite
-- index carries both. DESC matches the "most recent first" access pattern.
CREATE INDEX system_metrics_server_timestamp_idx
    ON system_metrics (server_id, timestamp DESC);

-- Retention pruning deletes by timestamp across all servers.
CREATE INDEX system_metrics_timestamp_idx ON system_metrics (timestamp);
