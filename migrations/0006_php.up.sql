-- Phase 5: PHP versions and per-site FPM pools.
-- Implements DATABASE.md tables 11 and 12.

-- ------------------------------------------------------------ php_versions

-- What PHP the host actually has. Rows are written by detection against the
-- host, not by an operator asserting a version exists: a panel that offers a
-- version nothing can run produces sites that return 502.
CREATE TABLE php_versions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    version     VARCHAR(20) UNIQUE NOT NULL,
    binary_path TEXT,
    fpm_service VARCHAR(255),
    status      VARCHAR(30) NOT NULL DEFAULT 'available',
    installed   BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Detection reruns; this records when the row last reflected the host.
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Only major.minor. A patch level is a property of what happens to be
    -- installed, not something a user selects, and accepting both spellings
    -- would let one version exist as two rows.
    CONSTRAINT php_versions_format CHECK (version ~ '^[5-9]\.[0-9]{1,2}$'),
    CONSTRAINT php_versions_status_valid
        CHECK (status IN ('available', 'installing', 'removing', 'failed')),
    -- An installed version must say how to run it, or nothing can use it.
    CONSTRAINT php_versions_installed_has_binary
        CHECK (installed = FALSE OR binary_path IS NOT NULL)
);

CREATE INDEX php_versions_installed_idx ON php_versions (installed);

-- --------------------------------------------------------------- php_pools

-- One FPM pool per website. The UNIQUE constraint on website_id is the point:
-- two pools for one site would race for the same socket path, and whichever
-- FPM started last would win silently.
CREATE TABLE php_pools (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    website_id   UUID UNIQUE NOT NULL REFERENCES websites (id) ON DELETE CASCADE,
    php_version  VARCHAR(20) NOT NULL,
    pool_name    VARCHAR(100) NOT NULL,
    socket_path  TEXT NOT NULL,
    memory_limit VARCHAR(30),
    max_children INTEGER,
    -- Per-site php.ini values. Held as columns rather than a JSON blob so the
    -- CHECK constraints below apply; these are written into a configuration
    -- file, so the database is the last place they can be constrained.
    upload_max_filesize VARCHAR(30),
    max_execution_time  INTEGER,
    opcache_enabled     BOOLEAN NOT NULL DEFAULT TRUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT php_pools_version_format CHECK (php_version ~ '^[5-9]\.[0-9]{1,2}$'),
    CONSTRAINT php_pools_name_format CHECK (pool_name ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    CONSTRAINT php_pools_socket_absolute
        CHECK (socket_path LIKE '/%' AND socket_path NOT LIKE '%..%'),
    -- Mirrors shared/validate: a size, or -1 for memory_limit's "no limit".
    CONSTRAINT php_pools_memory_limit_format
        CHECK (memory_limit IS NULL OR memory_limit ~ '^(-1|[0-9]+[KMG]?)$'),
    CONSTRAINT php_pools_upload_size_format
        CHECK (upload_max_filesize IS NULL OR upload_max_filesize ~ '^[0-9]+[KMG]?$'),
    CONSTRAINT php_pools_execution_time_range
        CHECK (max_execution_time IS NULL OR max_execution_time BETWEEN 0 AND 3600),
    CONSTRAINT php_pools_max_children_range
        CHECK (max_children IS NULL OR max_children BETWEEN 1 AND 512)
);

CREATE INDEX php_pools_version_idx ON php_pools (php_version);

-- A pool name must be unique across the host: FPM would otherwise refuse to
-- start, taking down every site sharing that version.
CREATE UNIQUE INDEX php_pools_name_idx ON php_pools (pool_name);
