-- Phase 22: multi-tenancy — who an account answers to, what a plan promises,
-- what a subscription actually uses, and who is pretending to be whom.

-- ------------------------------------------------------------- the hierarchy

-- Every account now sits somewhere in a three-tier tree: an admin owns the
-- server, a reseller sells space on it, a customer buys some.
--
-- The tier is on the account rather than derived from its role, because they
-- answer different questions. A role says what somebody may *do*; a tier says
-- whose accounts they may do it to. An operator and a reseller can hold the
-- same permissions and still must not see each other's customers.
ALTER TABLE users
    ADD COLUMN tier VARCHAR(20) NOT NULL DEFAULT 'admin',
    -- Who created this account and is answerable for it. RESTRICT rather than
    -- CASCADE: deleting a reseller must not silently delete every customer
    -- under them, along with their subscriptions and — through those — the
    -- record of which websites were theirs. The panel makes somebody move or
    -- remove the customers first, deliberately.
    ADD COLUMN parent_user_id UUID REFERENCES users (id) ON DELETE RESTRICT,
    ADD COLUMN full_name VARCHAR(255),
    ADD COLUMN company VARCHAR(255);

ALTER TABLE users
    ADD CONSTRAINT users_tier_valid
        CHECK (tier IN ('admin', 'reseller', 'customer')),
    -- An admin answers to nobody; everybody else answers to somebody. The
    -- other half of the rule — that a parent's tier is strictly above the
    -- child's, which is what makes a cycle impossible — needs a lookup and so
    -- lives in Go, in the tenancy package.
    ADD CONSTRAINT users_tier_has_a_parent
        CHECK (
            (tier = 'admin' AND parent_user_id IS NULL)
            OR (tier <> 'admin' AND parent_user_id IS NOT NULL)
        ),
    ADD CONSTRAINT users_not_their_own_parent
        CHECK (parent_user_id IS NULL OR parent_user_id <> id);

CREATE INDEX users_parent_idx ON users (parent_user_id);
CREATE INDEX users_tier_idx ON users (tier);

-- ------------------------------------------------------------------- plans

-- What a plan promises, and what an add-on adds to it.
--
-- One table for both, distinguished by kind, because the columns are the same
-- columns and two tables would mean two definitions of "the disk limit" that
-- could drift apart. What differs is how a number is read: on a plan it is the
-- limit, on an add-on it is an increment.
CREATE TABLE service_plans (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Who owns this plan. NULL means the admin's own catalogue, offered to
    -- everybody; a reseller's id means a plan only that reseller may sell.
    --
    -- A reseller must be able to build their own packages without being able
    -- to edit the ones the server owner published, and a reseller who is
    -- removed should not leave plans behind that nobody can administer, which
    -- is why this cascades where the hierarchy above restricts: a plan is a
    -- price list, not a customer's data.
    owner_user_id UUID REFERENCES users (id) ON DELETE CASCADE,

    name        VARCHAR(120) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    kind        VARCHAR(10) NOT NULL DEFAULT 'plan',

    -- The limits.
    --
    -- NULL is unlimited and 0 is none, and the difference is the whole point:
    -- a plan with no mailbox limit and a plan that includes no mailboxes are
    -- opposite promises, and a scheme using 0 for both cannot tell a customer
    -- which one they bought. On an add-on, NULL means "adds nothing to this
    -- dimension" — there is nothing for an increment of infinity to mean.
    disk_mb        INTEGER,
    bandwidth_mb   INTEGER,
    max_websites   INTEGER,
    max_databases  INTEGER,
    max_mailboxes  INTEGER,
    max_ftp_users  INTEGER,
    max_cron_jobs  INTEGER,
    max_subdomains INTEGER,

    -- What happens when a limit is reached.
    --
    -- 'hard' refuses; 'soft' allows and records the overage. The distinction
    -- only reaches as far as the panel can act: a countable limit is refused
    -- at the moment somebody asks for one more, while disk and bandwidth are
    -- measured after the fact and can only be reported. See docs/PHASE22.md.
    enforcement VARCHAR(10) NOT NULL DEFAULT 'hard',

    -- Resource isolation, applied as a systemd slice per subscription.
    --
    -- NULL is "no limit on this dimension" here too, and it is the default:
    -- capping a customer's CPU is a decision, and a panel that quietly applied
    -- one would be a panel whose sites are slow for a reason nobody can find.
    cpu_percent INTEGER,
    memory_mb   INTEGER,
    io_weight   INTEGER,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT service_plans_kind_valid CHECK (kind IN ('plan', 'addon')),
    CONSTRAINT service_plans_enforcement_valid
        CHECK (enforcement IN ('hard', 'soft')),
    CONSTRAINT service_plans_name_present CHECK (btrim(name) <> ''),
    -- A negative limit is not a smaller limit, it is a typo. NULL is how this
    -- schema says "no limit", so the columns need no sentinel and can refuse
    -- everything below zero.
    CONSTRAINT service_plans_limits_sane CHECK (
        (disk_mb        IS NULL OR disk_mb        >= 0) AND
        (bandwidth_mb   IS NULL OR bandwidth_mb   >= 0) AND
        (max_websites   IS NULL OR max_websites   >= 0) AND
        (max_databases  IS NULL OR max_databases  >= 0) AND
        (max_mailboxes  IS NULL OR max_mailboxes  >= 0) AND
        (max_ftp_users  IS NULL OR max_ftp_users  >= 0) AND
        (max_cron_jobs  IS NULL OR max_cron_jobs  >= 0) AND
        (max_subdomains IS NULL OR max_subdomains >= 0)
    ),
    -- systemd's own ranges. CPUQuota above 100% is meaningful on a multi-core
    -- host, so the ceiling is generous rather than 100; MemoryMax below 16 MiB
    -- kills anything that starts; IOWeight is 1-10000 by definition.
    CONSTRAINT service_plans_isolation_sane CHECK (
        (cpu_percent IS NULL OR cpu_percent BETWEEN 1 AND 10000) AND
        (memory_mb   IS NULL OR memory_mb   >= 16) AND
        (io_weight   IS NULL OR io_weight   BETWEEN 1 AND 10000)
    )
);

-- A plan name is unique within its catalogue. Two "Starter" plans in one
-- reseller's list is a support call waiting to happen; two resellers each with
-- a "Starter" is ordinary.
CREATE UNIQUE INDEX service_plans_owner_name_idx
    ON service_plans (owner_user_id, lower(name)) WHERE owner_user_id IS NOT NULL;
CREATE UNIQUE INDEX service_plans_global_name_idx
    ON service_plans (lower(name)) WHERE owner_user_id IS NULL;

-- ----------------------------------------------------------- subscriptions

-- What a customer actually has: a plan, whatever add-ons were bought on top,
-- and the websites that live inside it.
CREATE TABLE subscriptions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Whose it is. RESTRICT for the same reason the hierarchy restricts: a
    -- subscription is the only row that says which websites belonged to whom.
    owner_user_id UUID NOT NULL REFERENCES users (id) ON DELETE RESTRICT,

    -- The plan it is on. RESTRICT rather than SET NULL: a subscription whose
    -- plan vanished has no limits at all, which is the failure mode where a
    -- panel stops enforcing quotas and nobody notices.
    plan_id UUID NOT NULL REFERENCES service_plans (id) ON DELETE RESTRICT,

    name   VARCHAR(255) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'active',

    -- Why it was suspended and when, so the page can say more than "suspended"
    -- and so an unsuspend has something to undo.
    suspended_reason TEXT NOT NULL DEFAULT '',
    suspended_at     TIMESTAMPTZ,

    -- The systemd slice this subscription's processes belong to, and whether
    -- this host is actually enforcing it.
    --
    -- Four states rather than a boolean, and the fourth is the one that
    -- matters: 'declared' means the panel wrote the limits and the host cannot
    -- apply them — no systemd, or no permission to place processes in a slice.
    -- A panel with only applied/failed would answer "is this customer capped"
    -- with a green tick on a host that caps nothing.
    slice_name           VARCHAR(120) NOT NULL DEFAULT '',
    isolation_state      VARCHAR(20)  NOT NULL DEFAULT 'none',
    isolation_detail     TEXT         NOT NULL DEFAULT '',
    isolation_applied_at TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT subscriptions_status_valid
        CHECK (status IN ('active', 'suspended')),
    CONSTRAINT subscriptions_isolation_state_valid
        CHECK (isolation_state IN ('none', 'applied', 'declared', 'failed')),
    CONSTRAINT subscriptions_name_present CHECK (btrim(name) <> ''),
    -- A suspended subscription says when. A row that is suspended and cannot
    -- say since when is one nobody can argue with a customer about.
    CONSTRAINT subscriptions_suspension_dated
        CHECK (status <> 'suspended' OR suspended_at IS NOT NULL)
);

CREATE INDEX subscriptions_owner_idx ON subscriptions (owner_user_id);
CREATE INDEX subscriptions_plan_idx  ON subscriptions (plan_id);
CREATE UNIQUE INDEX subscriptions_owner_name_idx
    ON subscriptions (owner_user_id, lower(name));

-- The add-ons bought on top of a plan.
--
-- A quantity rather than a row per purchase: "two extra mailbox packs" is one
-- fact, and counting rows to find it would make removing one ambiguous.
CREATE TABLE subscription_addons (
    subscription_id UUID NOT NULL REFERENCES subscriptions (id) ON DELETE CASCADE,
    plan_id         UUID NOT NULL REFERENCES service_plans (id) ON DELETE RESTRICT,
    quantity        INTEGER NOT NULL DEFAULT 1,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (subscription_id, plan_id),
    CONSTRAINT subscription_addons_quantity_sane
        CHECK (quantity BETWEEN 1 AND 1000)
);

-- Which subscription a website belongs to.
--
-- The website is the unit of tenancy in this panel, and this is the column
-- that makes it one: databases, mailboxes, FTP accounts, scheduled jobs and
-- repositories all already reference a website, so every one of them has an
-- owner the moment its website does.
--
-- Nullable, because every website that exists today predates this migration
-- and belongs to the server's own administrator. RESTRICT so a subscription
-- cannot be deleted out from under the sites it owns.
ALTER TABLE websites
    ADD COLUMN subscription_id UUID REFERENCES subscriptions (id) ON DELETE RESTRICT;

CREATE INDEX websites_subscription_idx ON websites (subscription_id);

-- ------------------------------------------------------------------- usage

-- What a subscription is measured to be using, and when it was last looked at.
--
-- Separate from subscriptions because it is written by a sampler on a timer
-- and read by a page, while the row above is written by people; keeping them
-- apart means a measurement never contends with an edit.
CREATE TABLE subscription_usage (
    subscription_id UUID PRIMARY KEY REFERENCES subscriptions (id) ON DELETE CASCADE,

    -- The billing period the bandwidth figure covers. Bandwidth is a total
    -- over a period, unlike disk, which is a level.
    period_start DATE NOT NULL,

    -- NULL means *not measured*, and it is not the same as zero.
    --
    -- This is the same distinction Phase 21 draws about outstanding updates
    -- and Phase 19 about a probe that timed out. A subscription whose disk
    -- could not be read is not a subscription using no disk, and a panel that
    -- showed 0 would tell a customer they are within a quota nobody checked.
    disk_bytes BIGINT,

    -- Bandwidth accumulates across log rotations.
    --
    -- The Agent reports what a site's access log says it has served, which
    -- resets when the log rotates. bandwidth_raw_bytes holds the last raw
    -- reading so the next one can be turned into a delta; a reading lower than
    -- the last means the log rotated, and the whole reading is the delta.
    bandwidth_bytes     BIGINT,
    bandwidth_raw_bytes BIGINT,

    measured_at   TIMESTAMPTZ,
    measure_error TEXT NOT NULL DEFAULT '',

    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT subscription_usage_sane CHECK (
        (disk_bytes          IS NULL OR disk_bytes          >= 0) AND
        (bandwidth_bytes     IS NULL OR bandwidth_bytes     >= 0) AND
        (bandwidth_raw_bytes IS NULL OR bandwidth_raw_bytes >= 0)
    )
);

-- ----------------------------------------------------------- impersonation

-- Every time somebody used the panel as somebody else.
--
-- A table of its own rather than an audit line, because it is the one action
-- in the panel where the audit log's "who" would otherwise be wrong: without
-- this, every row an impersonated session writes is attributed to the customer
-- who did not do it. This table is what lets an operator answer "who actually
-- changed that" months later, and it is why an impersonation is recorded even
-- when it does nothing.
CREATE TABLE impersonation_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    actor_user_id   UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    subject_user_id UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,

    -- The session issued for the subject. SET NULL rather than CASCADE: the
    -- session is cleaned up on a timer, and the record that somebody was
    -- impersonated must outlive it.
    session_id UUID REFERENCES sessions (id) ON DELETE SET NULL,

    reason     TEXT NOT NULL DEFAULT '',
    ip_address INET,
    user_agent TEXT,

    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at   TIMESTAMPTZ,

    CONSTRAINT impersonation_not_self CHECK (actor_user_id <> subject_user_id)
);

CREATE INDEX impersonation_actor_idx
    ON impersonation_sessions (actor_user_id, started_at DESC);
CREATE INDEX impersonation_subject_idx
    ON impersonation_sessions (subject_user_id, started_at DESC);
-- Finding the open one for a session, which is what ending an impersonation
-- has to do and what a page has to show.
CREATE INDEX impersonation_open_idx
    ON impersonation_sessions (session_id) WHERE ended_at IS NULL;

-- ------------------------------------------------- the audit log's own key

-- audit_logs stops referencing users.
--
-- This is a fix to a Phase 1 table, made here because this is the phase that
-- first deletes an account, and it is the phase that found the problem by
-- doing it.
--
-- Migration 0001 gave audit_logs.user_id ON DELETE SET NULL "so removing a
-- user never erases history", and separately made the table append-only with a
-- trigger that refuses every UPDATE. Both are right and together they are
-- impossible: the cascade *is* an UPDATE, so the trigger refuses it, so an
-- account that has ever done anything auditable can never be deleted. It fails
-- with an internal error naming a trigger, which is a message nobody can act
-- on.
--
-- Dropping the constraint resolves it in the direction the append-only rule
-- wants. A row saying "user X did this" must not be rewritten to "somebody did
-- this" because X was later removed — that is exactly the edit the trigger
-- exists to prevent, and a foreign key was quietly performing it. The column
-- keeps the id; nothing rewrites it; a deleted account's rows still say which
-- account.
--
-- What is given up is referential integrity on that column: an id in the log
-- may name an account that no longer exists. For an append-only record of what
-- happened, that is the correct trade — the alternative is a log that forgets.
ALTER TABLE audit_logs DROP CONSTRAINT IF EXISTS audit_logs_user_id_fkey;

-- ------------------------------------------------------------- permissions

-- Three permissions, split along what each one lets somebody find out.
--
-- tenant.view is a customer list with what each one is using. tenant.manage
-- creates accounts and moves quota, which is the ability to give somebody more
-- of a server than was sold to them. tenant.impersonate is separate from both
-- and is the sharpest: it is not "manage a customer's site", it is *be* them —
-- their files, their mail, their databases, with their name on everything that
-- happens. Somebody who provisions accounts all day does not need that, and a
-- panel that bundled it would have an audit trail that cannot tell a customer
-- apart from their reseller.
INSERT INTO permissions (name, description) VALUES
    ('tenant.view',        'See accounts, plans, subscriptions, and what they use'),
    ('tenant.manage',      'Create accounts, build plans, and manage subscriptions'),
    ('tenant.impersonate', 'Sign in as another account')
ON CONFLICT (name) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.name IN ('tenant.view', 'tenant.manage', 'tenant.impersonate')
WHERE r.name = 'admin'
ON CONFLICT DO NOTHING;

-- operator sees the tenancy but does not sell it. An operator's job is the
-- server; deciding who gets how much of it is the owner's.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.name = 'tenant.view'
WHERE r.name = 'operator'
ON CONFLICT DO NOTHING;

-- ------------------------------------------------------------------- roles

-- A role for a reseller.
--
-- It holds the tenancy permissions and nothing server-level: no firewall, no
-- services, no updates, no security findings, no SSH. A reseller sells space on
-- somebody else's machine, and every permission that would let them change the
-- machine itself is deliberately absent.
--
-- What it deliberately does *not* hold either is the hosting permissions —
-- website.view and the rest. Not because a reseller should not manage their
-- customers' sites, but because this phase scopes tenancy and not the resource
-- listings: a role granted website.view today would see every website on the
-- host, including other resellers'. docs/PHASE22.md says so plainly and names
-- it as the next piece of work.
INSERT INTO roles (name, description) VALUES
    ('reseller', 'Sell and manage hosting accounts without control of the server')
ON CONFLICT (name) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.name IN ('tenant.view', 'tenant.manage', 'tenant.impersonate')
WHERE r.name = 'reseller'
ON CONFLICT DO NOTHING;
