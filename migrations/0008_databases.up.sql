-- Phase 8: databases, their accounts, and who may reach what.
-- Implements DATABASE.md tables 16, 17, and 18.

-- ------------------------------------------------------------- databases

-- What the panel believes exists on the host's database servers.
--
-- The engine is part of the row rather than a property of the server because a
-- host can run more than one: a MariaDB and a PostgreSQL side by side is an
-- ordinary arrangement, and "app" may exist on both without the two being the
-- same database.
CREATE TABLE databases (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id  UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    -- A database may belong to a website, which is how the panel shows it on
    -- that site's page and offers to remove it with the site. It may also
    -- stand alone: a shared database is a legitimate arrangement, so this is
    -- nullable rather than required.
    --
    -- ON DELETE SET NULL, not CASCADE: deleting a website must never silently
    -- delete a database. The data outlives the vhost, and dropping it is a
    -- separate decision an operator has to make deliberately.
    website_id UUID REFERENCES websites (id) ON DELETE SET NULL,

    name   VARCHAR(63) NOT NULL,
    engine VARCHAR(30) NOT NULL,
    status VARCHAR(30) NOT NULL DEFAULT 'creating',

    charset VARCHAR(64),
    -- Named collation_name rather than "collation": the bare word is reserved
    -- in PostgreSQL and would have to be quoted at every use, which is exactly
    -- the kind of thing that works until someone writes one query without the
    -- quotes.
    collation_name VARCHAR(64),
    -- Last observed size. It is a cache of what the server reported, refreshed
    -- on demand, so it is nullable: "not measured yet" and "empty" are
    -- different facts and showing 0 B for the first would be a lie.
    size_bytes BIGINT,
    size_checked_at TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT databases_engine_valid
        CHECK (engine IN ('mysql', 'mariadb', 'postgres')),
    CONSTRAINT databases_status_valid
        CHECK (status IN ('creating', 'active', 'deleting', 'failed')),
    -- The same rule as shared/validate.DatabaseName. It is repeated here
    -- because the database is the last place a name can be constrained: a row
    -- written by anything that skipped the validator still cannot hold a name
    -- that would need escaping in a statement.
    CONSTRAINT databases_name_format CHECK (name ~ '^[a-z][a-z0-9_]{0,62}$'),
    CONSTRAINT databases_size_non_negative CHECK (size_bytes IS NULL OR size_bytes >= 0)
);

-- One name per engine per server. Two rows would let the panel offer to delete
-- a database twice, and the second deletion would report success having done
-- nothing.
CREATE UNIQUE INDEX databases_server_engine_name_idx
    ON databases (server_id, engine, name);

CREATE INDEX databases_website_idx ON databases (website_id);
CREATE INDEX databases_status_idx ON databases (status);

-- --------------------------------------------------------- database_users

-- An account on a database server.
--
-- Accounts are per-server, not per-database: one account routinely has access
-- to several databases, which is what database_permissions records.
CREATE TABLE database_users (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    engine    VARCHAR(30) NOT NULL,
    username  VARCHAR(32) NOT NULL,
    -- MySQL identifies an account by user and host together; PostgreSQL roles
    -- are global. Empty on PostgreSQL rather than filled with a value that
    -- would imply a restriction the server does not enforce.
    host      VARCHAR(64) NOT NULL DEFAULT '',

    -- AES-256-GCM, bound to this row's id as additional authenticated data, so
    -- a ciphertext moved from another row fails to decrypt rather than
    -- silently revealing another site's password (DATABASE.md section 30).
    --
    -- The panel stores it because the server does not: MySQL and PostgreSQL
    -- both keep only a hash, so a password not captured here can never be
    -- shown to the person who has to put it in a configuration file.
    password_encrypted TEXT NOT NULL,
    password_updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT database_users_engine_valid
        CHECK (engine IN ('mysql', 'mariadb', 'postgres')),
    CONSTRAINT database_users_name_format CHECK (username ~ '^[a-z][a-z0-9_]{0,31}$'),
    -- Only the two host patterns shared/validate permits. A literal address or
    -- a wildcard pattern is how a typo turns into a database reachable from a
    -- whole subnet.
    CONSTRAINT database_users_host_valid CHECK (host IN ('', 'localhost', '%')),
    -- A PostgreSQL role must not carry a host, and a MySQL account must.
    CONSTRAINT database_users_host_matches_engine
        CHECK ((engine = 'postgres' AND host = '') OR (engine <> 'postgres' AND host <> ''))
);

CREATE UNIQUE INDEX database_users_identity_idx
    ON database_users (server_id, engine, username, host);

-- --------------------------------------------------- database_permissions

-- Which account may do what to which database.
--
-- The privilege level is stored rather than derived because the two engines
-- express the same intent differently: "readwrite" is four MySQL privileges
-- and, on PostgreSQL, a schema grant plus default privileges. Recording the
-- level the operator chose keeps the panel able to say what it meant, and to
-- reapply it identically on either server.
CREATE TABLE database_permissions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    database_user_id UUID NOT NULL REFERENCES database_users (id) ON DELETE CASCADE,
    database_id      UUID NOT NULL REFERENCES databases (id) ON DELETE CASCADE,
    privilege        VARCHAR(20) NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT database_permissions_level_valid
        CHECK (privilege IN ('readonly', 'readwrite', 'full'))
);

-- One grant per account per database. A second row would make "what access
-- does this account have" a question with two answers.
CREATE UNIQUE INDEX database_permissions_pair_idx
    ON database_permissions (database_user_id, database_id);

CREATE INDEX database_permissions_database_idx ON database_permissions (database_id);
