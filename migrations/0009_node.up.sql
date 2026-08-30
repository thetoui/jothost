-- Phase 9: Node.js applications and their configuration.
-- Implements DATABASE.md tables 14 and 15.

-- ------------------------------------------------------------- node_apps

-- An application the panel runs on the host.
--
-- It belongs to a website: the application is what that site serves, it runs
-- as that site's system account, and nginx reverse-proxies the site's domain
-- to it. An application with no website would have nothing pointing at it.
CREATE TABLE node_apps (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id  UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    -- ON DELETE CASCADE, unlike a database: an application is the website's
    -- own process, not data that outlives it. Deleting the site removes the
    -- vhost that reached it, so keeping the record would leave a process
    -- nothing could route to.
    website_id UUID NOT NULL REFERENCES websites (id) ON DELETE CASCADE,

    name         VARCHAR(40) NOT NULL,
    node_version VARCHAR(30) NOT NULL,
    application_root TEXT NOT NULL,
    startup_file TEXT NOT NULL,
    port         INTEGER NOT NULL,
    status       VARCHAR(30) NOT NULL DEFAULT 'stopped',
    -- The unit name on a systemd host. Recorded rather than derived so the
    -- panel can still name it if the naming scheme ever changes.
    systemd_service VARCHAR(255),
    -- Whether the panel should keep it running. Separate from status: "it is
    -- stopped" and "it is meant to be stopped" are different facts, and
    -- conflating them is how a crashed application looks deliberate.
    autostart BOOLEAN NOT NULL DEFAULT TRUE,

    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- The same rule as shared/validate.AppName. Repeated here because this
    -- value becomes a systemd unit name, and the database is the last place
    -- it can be constrained.
    CONSTRAINT node_apps_name_format CHECK (name ~ '^[a-z][a-z0-9_-]{0,39}$'),
    CONSTRAINT node_apps_version_format CHECK (node_version ~ '^[1-9][0-9]?(\.[0-9]{1,2})?$'),
    CONSTRAINT node_apps_status_valid
        CHECK (status IN ('stopped', 'starting', 'running', 'failed')),
    CONSTRAINT node_apps_root_absolute
        CHECK (application_root LIKE '/%' AND application_root NOT LIKE '%..%'),
    -- Relative, and with no traversal: an absolute startup file would let an
    -- application be started from outside its own directory.
    CONSTRAINT node_apps_startup_relative
        CHECK (startup_file NOT LIKE '/%' AND startup_file NOT LIKE '%..%'),
    -- Matches shared/validate.AppPort: above the privileged range, below the
    -- ephemeral range the kernel hands to outgoing connections.
    CONSTRAINT node_apps_port_range CHECK (port BETWEEN 1024 AND 32767)
);

-- One application per website. A second would need a second vhost to reach it,
-- and the site has one domain.
CREATE UNIQUE INDEX node_apps_website_idx ON node_apps (website_id);

-- A port is a host-wide resource: two applications on one port means one of
-- them is failing to bind and the panel would not know which.
CREATE UNIQUE INDEX node_apps_port_idx ON node_apps (server_id, port);

CREATE UNIQUE INDEX node_apps_name_idx ON node_apps (server_id, name);
CREATE INDEX node_apps_status_idx ON node_apps (status);

-- ------------------------------------------------------ node_environment

-- One application's configuration.
--
-- Values are encrypted because this is where a database URL with a password in
-- it goes, and an API key, and a signing secret. A panel that stored them in
-- plain text would make its own database the most valuable thing on the host.
CREATE TABLE node_environment (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_app_id UUID NOT NULL REFERENCES node_apps (id) ON DELETE CASCADE,
    key         VARCHAR(64) NOT NULL,
    -- AES-256-GCM, bound to this row's id, so a ciphertext copied from another
    -- application's row fails to decrypt rather than revealing its secret
    -- (DATABASE.md section 30).
    value_encrypted TEXT NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- The POSIX environment variable name, matching shared/validate.EnvKey.
    CONSTRAINT node_environment_key_format CHECK (key ~ '^[A-Z_][A-Z0-9_]{0,63}$'),
    -- The names that change what runs rather than how it behaves. The
    -- validator refuses them too; this is the last line, for a row written by
    -- some future path that forgot.
    CONSTRAINT node_environment_key_not_reserved
        CHECK (key NOT IN ('LD_PRELOAD', 'LD_LIBRARY_PATH', 'LD_AUDIT',
                           'NODE_OPTIONS', 'PATH', 'IFS', 'SHELL', 'BASH_ENV',
                           'ENV', 'PORT', 'HOME', 'USER', 'PWD'))
);

CREATE UNIQUE INDEX node_environment_key_idx ON node_environment (node_app_id, key);
