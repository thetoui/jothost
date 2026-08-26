-- Phase 4: websites, their domains, and the durable job record.
-- Implements DATABASE.md tables 9, 10, and 24.

-- --------------------------------------------------------------------- jobs

-- Jobs are the durable record of a long-running infrastructure operation.
-- The Agent runs the work and tracks it in memory, but that is lost on an
-- Agent restart; this table is what the panel can still answer from.
CREATE TABLE jobs (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    type         VARCHAR(100) NOT NULL,
    status       VARCHAR(30) NOT NULL DEFAULT 'PENDING',
    payload      JSONB,
    result       JSONB,
    error        TEXT,
    progress     INTEGER NOT NULL DEFAULT 0,
    message      TEXT,
    -- Null when the job was raised by the system rather than a person, and
    -- retained via ON DELETE SET NULL so removing a user keeps the history.
    created_by   UUID REFERENCES users (id) ON DELETE SET NULL,
    -- The resource the job acts on, so a page can show "what is happening to
    -- this website" without scanning every job's payload.
    resource_type VARCHAR(100),
    resource_id   UUID,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at   TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,

    CONSTRAINT jobs_status_valid
        CHECK (status IN ('PENDING', 'RUNNING', 'SUCCESS', 'FAILED', 'CANCELLED')),
    CONSTRAINT jobs_progress_range CHECK (progress BETWEEN 0 AND 100)
);

-- The worker claims pending jobs oldest first; this index is what keeps that
-- claim from scanning the whole table as history accumulates.
CREATE INDEX jobs_pending_idx ON jobs (created_at) WHERE status = 'PENDING';
CREATE INDEX jobs_status_idx ON jobs (status);
CREATE INDEX jobs_created_at_idx ON jobs (created_at DESC);
CREATE INDEX jobs_resource_idx ON jobs (resource_type, resource_id);

-- ----------------------------------------------------------------- websites

CREATE TABLE websites (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id      UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    name           VARCHAR(255),
    primary_domain VARCHAR(255) UNIQUE NOT NULL,
    document_root  TEXT NOT NULL,
    -- Not "system_user": PostgreSQL 16 made SYSTEM_USER a reserved keyword
    -- (SQL:2023), so that name is a syntax error unquoted and, worse, resolves
    -- to the built-in function in some contexts instead of failing loudly. The
    -- API still calls this field system_user; only the column is renamed.
    system_username VARCHAR(100) NOT NULL,
    php_version    VARCHAR(20),
    status         VARCHAR(30) NOT NULL DEFAULT 'creating',
    ssl_enabled    BOOLEAN NOT NULL DEFAULT FALSE,
    https_redirect BOOLEAN NOT NULL DEFAULT FALSE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- A website is never silently "fine": it is being built, working, broken,
    -- or deliberately stopped. Conflating failed with active is how a panel
    -- ends up claiming a site works when its vhost was never written.
    CONSTRAINT websites_status_valid
        CHECK (status IN ('creating', 'active', 'suspended', 'failed', 'deleting')),
    -- Mirrors the domain rules enforced in Go, so a bad value cannot reach the
    -- table through some future code path that forgets to validate.
    CONSTRAINT websites_domain_format
        CHECK (primary_domain ~ '^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$'),
    CONSTRAINT websites_system_user_format
        CHECK (system_username ~ '^[a-z_][a-z0-9_-]{0,31}$'),
    CONSTRAINT websites_document_root_absolute
        CHECK (document_root LIKE '/%' AND document_root NOT LIKE '%..%')
);

CREATE INDEX websites_server_id_idx ON websites (server_id);
CREATE INDEX websites_status_idx ON websites (status);
-- One system user per site: sharing one would let a compromised site read its
-- neighbour's files.
CREATE UNIQUE INDEX websites_system_user_idx ON websites (system_username);

-- ------------------------------------------------------------------ domains

CREATE TABLE domains (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    website_id UUID NOT NULL REFERENCES websites (id) ON DELETE CASCADE,
    domain     VARCHAR(255) UNIQUE NOT NULL,
    type       VARCHAR(30) NOT NULL,
    status     VARCHAR(30) NOT NULL DEFAULT 'active',
    -- Where a redirect points. Required for type 'redirect', meaningless
    -- otherwise, which the constraint below enforces rather than trusting
    -- every caller to remember.
    redirect_to TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT domains_type_valid
        CHECK (type IN ('primary', 'alias', 'subdomain', 'redirect')),
    CONSTRAINT domains_status_valid
        CHECK (status IN ('active', 'pending', 'disabled')),
    CONSTRAINT domains_format
        CHECK (domain ~ '^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$'),
    CONSTRAINT domains_redirect_target
        CHECK ((type = 'redirect') = (redirect_to IS NOT NULL))
);

CREATE INDEX domains_website_id_idx ON domains (website_id);

-- Exactly one primary domain per website. A second would make the vhost's
-- server_name ambiguous and the certificate subject undecidable later.
CREATE UNIQUE INDEX domains_one_primary_idx
    ON domains (website_id) WHERE type = 'primary';
