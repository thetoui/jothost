-- Phase 19: aggregated metrics, service state history, and the alert engine.

-- ------------------------------------------------------------- rollups

-- Hourly summaries of the raw samples, kept far longer than the samples are.
--
-- This is the "aggregated metrics: longer retention" the PRD asks for, and it
-- exists because the two are answering different questions. A raw sample every
-- thirty seconds is what makes an hour of history readable; nobody plotting a
-- year needs that resolution, and keeping a year of it would be a million rows
-- per server to answer a question a few thousand can.
--
-- Both an average and a maximum are kept. The average is what a graph plots;
-- the maximum is what stops an hour of aggregation hiding the five-minute spike
-- that filled a disk, which is precisely the event somebody looks back for.
CREATE TABLE metric_rollups (
    server_id UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    -- The start of the bucket, always on the hour in UTC.
    bucket_start TIMESTAMPTZ NOT NULL,

    cpu_avg     NUMERIC(6, 2),
    cpu_max     NUMERIC(6, 2),
    memory_avg  NUMERIC(6, 2),
    memory_max  NUMERIC(6, 2),
    disk_avg    NUMERIC(6, 2),
    disk_max    NUMERIC(6, 2),
    load_1_avg  NUMERIC(8, 2),
    load_1_max  NUMERIC(8, 2),

    -- Rates, not counters. The raw table stores cumulative counters so a rate
    -- can be derived at any resolution; once a bucket is closed that
    -- derivation has already happened, and storing the counter again would
    -- mean re-deriving it across a gap where samples have since been pruned.
    network_rx_per_second NUMERIC(16, 2),
    network_tx_per_second NUMERIC(16, 2),

    -- How many raw samples went into this bucket. A bucket built from two
    -- samples is not the same evidence as one built from a hundred, and a
    -- reader deserves to be able to tell.
    sample_count INTEGER NOT NULL DEFAULT 0,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (server_id, bucket_start)
);

CREATE INDEX metric_rollups_time_idx ON metric_rollups (server_id, bucket_start DESC);

-- --------------------------------------------------------- service history

-- When a monitored service changed state.
--
-- Transitions, not samples. A row per service per poll would be tens of
-- thousands of rows a day saying "still running", and the question an operator
-- actually asks — "when did it go down, and how long was it down" — is answered
-- by the changes alone.
CREATE TABLE service_states (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,

    -- The service's catalogue key, as the Agent reports it.
    service VARCHAR(100) NOT NULL,
    running BOOLEAN      NOT NULL,
    -- The status word the host used, kept because "stopped" and "not installed"
    -- are different things and both are reported as not running.
    status VARCHAR(50) NOT NULL DEFAULT '',

    -- When this state began, and when it ended. NULL means it is the current
    -- state, which is what makes "how long has it been down" a subtraction
    -- rather than a search.
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at   TIMESTAMPTZ
);

-- One open state per service, enforced rather than assumed: two rows with no
-- end would make every duration ambiguous.
CREATE UNIQUE INDEX service_states_current_idx
    ON service_states (server_id, service) WHERE ended_at IS NULL;
CREATE INDEX service_states_history_idx
    ON service_states (server_id, service, started_at DESC);

-- ------------------------------------------------------------ alert rules

-- What the panel watches, and when it complains.
--
-- Rules are rows rather than constants because the right threshold is a
-- property of the machine, not of this software: 85% memory is alarming on a
-- web server and ordinary on a database host that has been told to cache
-- aggressively. A panel with hardcoded thresholds gets muted, and a muted
-- panel is worse than no panel.
CREATE TABLE alert_rules (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,

    name VARCHAR(100) NOT NULL,

    -- What is being watched. A closed set: each one is a reading the panel
    -- knows how to take, and a metric it cannot take is a rule that would never
    -- fire while looking like it might.
    metric VARCHAR(30) NOT NULL,

    -- Which instance, for the metrics that have more than one. A mount point
    -- for disk, a service key for service. Empty means "any" — a disk rule with
    -- no target watches every filesystem, which is what somebody setting a
    -- general disk threshold means.
    target VARCHAR(255) NOT NULL DEFAULT '',

    -- The comparison and the number. Only two comparisons: above and below.
    -- Everything worth alerting on here is a resource crossing a line, and an
    -- equality test on a floating-point reading is a rule that never fires.
    comparison VARCHAR(10) NOT NULL DEFAULT 'above',
    threshold  NUMERIC(12, 2) NOT NULL,

    -- How long the breach must last before it becomes an alert.
    --
    -- This is the field that decides whether the panel is usable. A rule that
    -- fires on a single reading turns one backup job into a page at 3am; the
    -- same rule with five minutes of sustained breach fires when something is
    -- actually wrong. Zero means "on the first reading", which is right for a
    -- service being down and wrong for almost everything else.
    for_seconds INTEGER NOT NULL DEFAULT 300,

    severity VARCHAR(20) NOT NULL DEFAULT 'warning',
    enabled  BOOLEAN     NOT NULL DEFAULT TRUE,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT alert_rules_metric_valid
        CHECK (metric IN ('cpu', 'memory', 'disk', 'load', 'swap',
                          'network_rx', 'network_tx', 'service')),
    CONSTRAINT alert_rules_comparison_valid
        CHECK (comparison IN ('above', 'below')),
    CONSTRAINT alert_rules_severity_valid
        CHECK (severity IN ('warning', 'critical')),
    CONSTRAINT alert_rules_for_sane
        CHECK (for_seconds BETWEEN 0 AND 86400),
    CONSTRAINT alert_rules_name_present
        CHECK (length(trim(name)) > 0)
);

CREATE INDEX alert_rules_server_idx ON alert_rules (server_id, enabled);

-- A rule is identified by what it watches, not by its name: two rules on the
-- same metric and target with different thresholds would both fire, and the
-- operator would have two alerts about one problem.
CREATE UNIQUE INDEX alert_rules_watch_idx
    ON alert_rules (server_id, metric, target, severity);

-- ----------------------------------------------------------------- alerts

-- An alert that is open, or one that was.
--
-- Alerts are rows here and *not* on the dashboard, where they are computed from
-- the reading in front of you. The two answer different questions: the
-- dashboard's is "what is wrong now", and this table's is "what has been wrong,
-- since when, and is it still". Only the second can be notified about (Phase 20)
-- or looked back at.
CREATE TABLE alerts (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    -- The rule that opened it. ON DELETE SET NULL: deleting a rule must not
    -- erase the history of what it caught.
    rule_id UUID REFERENCES alert_rules (id) ON DELETE SET NULL,

    -- Kept alongside rule_id so an alert still describes itself after its rule
    -- is deleted or edited. An alert whose threshold changed after the fact
    -- would otherwise silently rewrite its own history.
    metric    VARCHAR(30)  NOT NULL,
    target    VARCHAR(255) NOT NULL DEFAULT '',
    severity  VARCHAR(20)  NOT NULL,
    threshold NUMERIC(12, 2),

    status  VARCHAR(20) NOT NULL DEFAULT 'open',
    message TEXT        NOT NULL,

    -- The reading that opened it, and the worst seen since. The worst is what
    -- somebody reading a resolved alert wants: "it was briefly at 91%" and "it
    -- sat at 99% for an hour" are different incidents.
    value      NUMERIC(12, 2),
    worst      NUMERIC(12, 2),
    last_value NUMERIC(12, 2),

    opened_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at  TIMESTAMPTZ,

    -- Acknowledging says "I know, I am dealing with it". It does not resolve
    -- the alert — the machine decides that, when the condition clears — and
    -- that separation is deliberate: a panel where a person can mark a full
    -- disk as fine is a panel that will one day say a full disk is fine.
    acknowledged_at TIMESTAMPTZ,
    acknowledged_by UUID REFERENCES users (id) ON DELETE SET NULL,

    CONSTRAINT alerts_status_valid CHECK (status IN ('open', 'resolved')),
    CONSTRAINT alerts_severity_valid CHECK (severity IN ('warning', 'critical')),
    CONSTRAINT alerts_resolved_has_time
        CHECK (status <> 'resolved' OR resolved_at IS NOT NULL)
);

CREATE INDEX alerts_server_idx ON alerts (server_id, opened_at DESC);
CREATE INDEX alerts_open_idx ON alerts (server_id, status) WHERE status = 'open';

-- One open alert per thing being watched.
--
-- Without this a flapping disk would open a new alert every evaluation, and an
-- operator would wake up to four hundred rows describing one filesystem.
CREATE UNIQUE INDEX alerts_open_unique_idx
    ON alerts (server_id, metric, target, severity) WHERE status = 'open';

-- ------------------------------------------------------------- permission

-- Watching the machine is its own act.
--
-- Not server.manage: tuning a threshold that is crying wolf, or acknowledging a
-- disk alert at three in the morning, cannot change what the server does — and
-- the person who looks after the sites is exactly who needs to do both. Making
-- it server.manage would mean the only people who can silence a false alarm are
-- the ones who can also stop nginx.
INSERT INTO permissions (name, description) VALUES
    ('monitor.manage', 'Configure alert rules and acknowledge alerts')
ON CONFLICT (name) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.name = 'monitor.manage'
WHERE r.name IN ('admin', 'operator')
ON CONFLICT DO NOTHING;
