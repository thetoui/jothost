-- Phase 21: the host's package updates, what was applied, and when to apply.

-- The panel's cached view of what this host has waiting.
--
-- Cached, because a check refreshes the package index and reaches the network:
-- asking on every page load would make the updates page slow and would hammer a
-- distribution's mirrors from every panel in existence.
--
-- One row per host per check, kept as a short history so "when did this update
-- appear" has an answer. What is *outstanding* is always the newest row.
CREATE TABLE update_checks (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,

    -- The package manager the host was found to use.
    manager VARCHAR(20) NOT NULL DEFAULT '',

    -- Whether the check actually worked.
    --
    -- This column is the reason the table exists in this shape. A check that
    -- could not reach the repositories produces an empty package list that is
    -- indistinguishable from a host with nothing to do — and "up to date" is
    -- what an operator reads to decide they are safe. A row with succeeded
    -- false is "not known", and the page says so rather than showing zero.
    succeeded BOOLEAN NOT NULL DEFAULT FALSE,
    reason    TEXT    NOT NULL DEFAULT '',

    -- Whether this host can distinguish security updates at all. apt can, from
    -- the origin it prints with each candidate; apk has no security channel, so
    -- reporting "0 security updates" there would answer a question nothing
    -- asked.
    security_known BOOLEAN NOT NULL DEFAULT FALSE,

    -- The counts, so a dashboard can read one row rather than a JSON document.
    package_count  INTEGER NOT NULL DEFAULT 0,
    security_count INTEGER NOT NULL DEFAULT 0,
    held_count     INTEGER NOT NULL DEFAULT 0,

    -- How many repositories could not be reached or were left stale. Both are
    -- why a check is untrustworthy, so both are kept.
    unavailable_repositories INTEGER NOT NULL DEFAULT 0,
    stale_repositories       INTEGER NOT NULL DEFAULT 0,

    reboot_required BOOLEAN NOT NULL DEFAULT FALSE,

    -- The packages and the held-back list, as the Agent reported them.
    --
    -- JSONB rather than a table of rows: this is a snapshot of somebody else's
    -- data that is replaced wholesale at the next check, never queried by
    -- package, and never joined to. A child table would be a delete-and-insert
    -- of a few hundred rows every time, for a list that is only ever read whole.
    packages JSONB NOT NULL DEFAULT '[]'::jsonb,
    held     JSONB NOT NULL DEFAULT '[]'::jsonb,

    checked_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT update_checks_counts_sane
        CHECK (
            package_count >= 0 AND security_count >= 0 AND held_count >= 0
            AND security_count <= package_count
        )
);

CREATE INDEX update_checks_server_idx ON update_checks (server_id, checked_at DESC);

-- ------------------------------------------------------------------- runs

-- One application of updates, whether a person asked or the schedule did.
--
-- This is the phase's answer to "rollback", and it is deliberate. Neither apk
-- nor apt keeps the package it replaced, and a Debian security update's
-- predecessor is usually gone from the archive the moment it is superseded — so
-- a panel promising to undo an update would be promising something the host
-- cannot do. What it can do is record exactly what moved, from which version to
-- which, which is what makes a manual recovery possible at all.
CREATE TABLE update_runs (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,

    -- 'manual', 'scheduled' or 'revert'. Who asked matters when reading a
    -- history: an update that appeared overnight was the schedule's.
    trigger VARCHAR(20) NOT NULL DEFAULT 'manual',
    -- 'running', 'succeeded' or 'failed'.
    status VARCHAR(20) NOT NULL DEFAULT 'running',

    -- What was asked for. Empty means everything, which is what "update all"
    -- means to a package manager.
    requested TEXT[] NOT NULL DEFAULT '{}',

    -- What actually moved, read back from the host afterwards. It is routinely
    -- longer than `requested`: a package manager resolves dependencies, so
    -- applying one update moves several, and the list an operator needs is the
    -- one that describes their machine.
    changes JSONB NOT NULL DEFAULT '[]'::jsonb,

    -- The package manager's own transcript, kept because when this goes wrong
    -- its words are the useful ones. Bounded by the API before it is stored.
    output TEXT NOT NULL DEFAULT '',
    error  TEXT NOT NULL DEFAULT '',

    reboot_required BOOLEAN NOT NULL DEFAULT FALSE,

    -- NULL until it finishes, which is also how a run that never finished —
    -- the panel was restarted mid-upgrade — is told from one that failed.
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,

    -- Who asked. NULL for the schedule, which has no user behind it.
    requested_by UUID REFERENCES users (id) ON DELETE SET NULL,

    CONSTRAINT update_runs_trigger_valid
        CHECK (trigger IN ('manual', 'scheduled', 'revert')),
    CONSTRAINT update_runs_status_valid
        CHECK (status IN ('running', 'succeeded', 'failed'))
);

CREATE INDEX update_runs_server_idx ON update_runs (server_id, started_at DESC);

-- --------------------------------------------------------------- settings

-- When and whether the panel applies updates by itself. One row per host.
CREATE TABLE update_settings (
    server_id UUID PRIMARY KEY REFERENCES servers (id) ON DELETE CASCADE,

    -- 'off', 'security' or 'all'.
    --
    -- The default is 'off', and that is not timidity. Applying updates restarts
    -- daemons: an operator who has not asked for that should not discover it
    -- from their monitoring at three in the morning. "security" exists because
    -- it is the setting somebody can leave on without a major version of
    -- something changing under a running site.
    policy VARCHAR(20) NOT NULL DEFAULT 'off',

    -- How often the panel looks. Checking is cheap for the panel and not free
    -- for a distribution's mirrors, so this is hours rather than minutes.
    check_interval_hours INTEGER NOT NULL DEFAULT 6,

    -- The window automatic updates run in: a day and a time, in the host's own
    -- clock. A day and an hour rather than a cron expression, because the
    -- useful question here is "which quiet hour" and a narrower control is one
    -- whose next run the panel can state plainly.
    day_of_week INTEGER NOT NULL DEFAULT -1,
    hour        INTEGER NOT NULL DEFAULT 3,
    minute      INTEGER NOT NULL DEFAULT 0,

    -- Packages the panel will never apply automatically.
    --
    -- The escape hatch that makes automatic updates usable: an operator who has
    -- one package they must upgrade by hand can say so without turning the
    -- whole thing off. It is the panel's own list and is not a pin — the host's
    -- package manager is not told about it, so nothing here changes what a
    -- person can do at a shell.
    excluded TEXT[] NOT NULL DEFAULT '{}',

    -- When the panel last looked and last applied, so the page can say so and
    -- the scheduler knows whether it is due.
    last_checked_at TIMESTAMPTZ,
    last_run_at     TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT update_settings_policy_valid
        CHECK (policy IN ('off', 'security', 'all')),
    CONSTRAINT update_settings_window_sane
        CHECK (
            (day_of_week = -1 OR day_of_week BETWEEN 0 AND 6)
            AND hour BETWEEN 0 AND 23
            AND minute BETWEEN 0 AND 59
        ),
    CONSTRAINT update_settings_interval_sane
        CHECK (check_interval_hours BETWEEN 1 AND 168)
);

-- ------------------------------------------------------------- permission

-- Applying updates is its own act.
--
-- It is not server.manage: an update restarts daemons and can change the
-- version of PHP a customer's site runs on, which is a different kind of
-- decision from restarting a service somebody already chose to run. Reading
-- what is outstanding needs only server.view, because knowing a host is behind
-- is not itself a privilege.
INSERT INTO permissions (name, description) VALUES
    ('update.manage', 'Check for and apply system package updates')
ON CONFLICT (name) DO NOTHING;

-- admin has everything.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.name = 'update.manage'
WHERE r.name = 'admin'
ON CONFLICT DO NOTHING;

-- operator does not. Upgrading the machine every customer's site runs on is a
-- server-level decision, and the operator role deliberately stops short of
-- those — it has no firewall.manage or server.manage either.
