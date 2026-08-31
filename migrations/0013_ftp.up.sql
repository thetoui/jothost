-- Phase 7.1: FTP accounts and the server's settings.

-- One FTP account.
--
-- It belongs to a website, and that is the same rule the cron jobs follow, for
-- the same reason: an FTP session runs as some account, and the website's own
-- unprivileged account is the only one the panel will use. An account with no
-- website would have no identity to map onto.
--
-- There is no password column, and that is deliberate rather than an omission.
-- The panel writes a password to the host once, into proftpd's own hashed
-- password file, and then does not have it. Storing one here — even encrypted —
-- would mean a copy of every customer's FTP credential in a database that is
-- backed up, replicated, and read by every part of the panel that touches this
-- table. "Show me the password" is answered by setting a new one.
CREATE TABLE ftp_users (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Recorded alongside the website so the panel can list a host's accounts
    -- without joining through every site, which is what the FTP page does.
    server_id  UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    -- ON DELETE CASCADE: deleting the website removes the account the sessions
    -- ran as, so keeping the record would leave a login that maps to nothing.
    website_id UUID NOT NULL REFERENCES websites (id) ON DELETE CASCADE,

    -- What the client logs in with.
    username VARCHAR(32) NOT NULL,

    -- The directory the account is confined to, relative to the website's
    -- document root. Empty means the document root itself, which is the common
    -- case.
    --
    -- Relative, always. An absolute path here would be an account rooted
    -- anywhere on the host, which is the whole thing the chroot exists to
    -- prevent; the constraint below refuses one, and refuses "..", so a path
    -- that escapes cannot reach the table through a future code path that
    -- forgets to validate.
    home_subpath VARCHAR(255) NOT NULL DEFAULT '',

    -- 'full' or 'readonly'. Two levels rather than a permission matrix: a
    -- client that can write can rename, overwrite and delete, because those are
    -- the same verbs, so finer distinctions would be ones FTP does not make.
    access_level VARCHAR(20) NOT NULL DEFAULT 'full',

    -- Disk limit in megabytes, 0 for none.
    quota_mb INTEGER NOT NULL DEFAULT 0,

    -- A suspended account keeps its home and its mapping and cannot log in, so
    -- an operator who suspects a credential is loose can act now and decide
    -- later, rather than choosing between leaving it and destroying it.
    suspended BOOLEAN NOT NULL DEFAULT FALSE,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Mirrors the rules enforced in Go. A colon would end a field early in
    -- proftpd's password file and give the next one a value the panel did not
    -- write.
    CONSTRAINT ftp_users_username_format
        CHECK (username ~ '^[a-zA-Z][a-zA-Z0-9._-]{2,31}$'),
    CONSTRAINT ftp_users_access_valid
        CHECK (access_level IN ('full', 'readonly')),
    CONSTRAINT ftp_users_quota_sane
        CHECK (quota_mb >= 0 AND quota_mb <= 1048576),
    -- No leading slash, no "..", no backslash: the three ways a subpath stops
    -- being one.
    CONSTRAINT ftp_users_home_relative
        CHECK (
            home_subpath NOT LIKE '/%'
            AND home_subpath NOT LIKE '%..%'
            AND home_subpath NOT LIKE '%\%'
        )
);

CREATE INDEX ftp_users_website_id_idx ON ftp_users (website_id);
CREATE INDEX ftp_users_server_id_idx ON ftp_users (server_id);
-- One account name per host. proftpd's password file has a single namespace —
-- two websites cannot each have a "backup" account, because the second would
-- overwrite the first and inherit its home directory.
CREATE UNIQUE INDEX ftp_users_username_idx ON ftp_users (server_id, username);

-- The FTP server's own settings, one row per host.
CREATE TABLE ftp_settings (
    server_id UUID PRIMARY KEY REFERENCES servers (id) ON DELETE CASCADE,

    -- The ports data connections use.
    --
    -- A range is stored rather than left to proftpd because the firewall has to
    -- open exactly these ports (Phase 16). Without a fixed range proftpd picks
    -- an ephemeral port per transfer and every passive transfer on a firewalled
    -- host hangs until the client gives up — a server that logs in fine and
    -- never lists a directory.
    passive_from INTEGER NOT NULL DEFAULT 30000,
    passive_to   INTEGER NOT NULL DEFAULT 30100,

    -- The website whose certificate FTPS presents, if any. The certificate
    -- itself is not copied here: Phase 6 owns it, renews it, and knows where it
    -- is, and a second copy would be one that silently went stale at the first
    -- renewal.
    --
    -- ON DELETE SET NULL: deleting that website turns FTPS off rather than
    -- deleting the FTP settings, and the panel then has an explicit "no
    -- certificate" state to report instead of presenting one that is gone.
    tls_website_id UUID REFERENCES websites (id) ON DELETE SET NULL,

    -- Refuse plain-text logins outright.
    --
    -- Off by default, and that default is considered: turning it on rejects
    -- every client still configured for plain FTP, at once, and most of them
    -- render the rejection as "login incorrect".
    require_tls BOOLEAN NOT NULL DEFAULT FALSE,

    -- The public address proftpd advertises for passive connections. Behind NAT
    -- the server otherwise hands the client an address it cannot reach.
    masquerade_address VARCHAR(255) NOT NULL DEFAULT '',

    -- 0 means proftpd's own default.
    max_clients INTEGER NOT NULL DEFAULT 0,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT ftp_settings_passive_range_valid
        CHECK (
            passive_from >= 1024
            AND passive_to <= 65535
            AND passive_to >= passive_from + 15
        ),
    CONSTRAINT ftp_settings_max_clients_sane
        CHECK (max_clients >= 0 AND max_clients <= 10000)
);

-- ------------------------------------------------------------- permission

-- Handing out an FTP account is its own act.
--
-- It is not website.update: an FTP credential reaches a site's files without
-- going through the panel at all, and it keeps working after the person who was
-- given it stops being a panel user. An account that may edit a vhost is not
-- automatically one that should be able to hand out a standing credential to
-- the files behind it.
INSERT INTO permissions (name, description) VALUES
    ('ftp.manage', 'Create, modify, and delete FTP accounts')
ON CONFLICT (name) DO NOTHING;

-- admin has everything.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.name = 'ftp.manage'
WHERE r.name = 'admin'
ON CONFLICT DO NOTHING;

-- operator: this is day-to-day hosting work, alongside file.write and
-- cron.manage, which the operator role already has.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.name = 'ftp.manage'
WHERE r.name = 'operator'
ON CONFLICT DO NOTHING;
