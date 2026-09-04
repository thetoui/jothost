-- Phase 20: notifications — channels, events, and the record of every delivery.

-- --------------------------------------------------------------- channels

-- Where notifications go.
--
-- Three kinds, and every one of them has a **fixed endpoint** except email,
-- whose host is the operator's own mail server. There is deliberately no
-- "webhook" kind and no free-form URL anywhere in this phase: a notification
-- channel that accepts a URL is a request forger sitting inside the panel,
-- pointed at whatever an admin account can be talked into typing. Telegram and
-- LINE publish one API host each, and those are compiled in.
CREATE TABLE notification_channels (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,

    name VARCHAR(100) NOT NULL,
    kind VARCHAR(20)  NOT NULL,

    -- Everything the sender needs that is not a secret: a mail host and port,
    -- a from address, the recipients, a chat id. JSON because the three kinds
    -- genuinely have different fields, and columns that are NULL for two kinds
    -- out of three teach a reader nothing.
    config JSONB NOT NULL DEFAULT '{}'::jsonb,

    -- AES-256-GCM, bound to this row's id as additional authenticated data, so
    -- a ciphertext moved from another row fails to decrypt rather than quietly
    -- authenticating somewhere it should not (DATABASE.md section 30).
    --
    -- An SMTP password, a Telegram bot token, a LINE channel access token. A
    -- bot token is a full credential: anyone holding it can post as the bot to
    -- every chat it is in.
    credentials_encrypted TEXT,

    enabled BOOLEAN NOT NULL DEFAULT TRUE,

    -- What this channel wants to hear about.
    --
    -- A severity floor rather than a list of severities, because the question
    -- an operator actually has is "how bad does it have to be before you wake
    -- me", and a set of checkboxes invites the answer "all of them" followed
    -- by a filter rule in their mail client.
    min_severity VARCHAR(20) NOT NULL DEFAULT 'warning',
    -- Which kinds of event. Empty means every kind, which is the right default:
    -- a channel that silently excluded a category would be a channel somebody
    -- believes is watching something it is not.
    kinds TEXT[] NOT NULL DEFAULT '{}',

    -- Whether the panel has ever actually delivered through this channel, and
    -- what happened last time it tried.
    --
    -- These matter more here than anywhere else in the panel, because of the
    -- one failure a notification system cannot report on its own terms: when
    -- delivery is broken, the message saying so does not arrive. The record has
    -- to live where somebody can see it without being told to look.
    last_success_at TIMESTAMPTZ,
    last_failure_at TIMESTAMPTZ,
    last_error      TEXT,
    -- Consecutive failures since the last success. A channel with a run of them
    -- is shown as broken, which is the only warning the panel can give.
    failure_streak INTEGER NOT NULL DEFAULT 0,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT notification_channels_kind_valid
        CHECK (kind IN ('email', 'telegram', 'line')),
    CONSTRAINT notification_channels_name_present
        CHECK (length(btrim(name)) > 0),
    CONSTRAINT notification_channels_severity_valid
        CHECK (min_severity IN ('critical', 'high', 'warning', 'info')),
    -- Every kind needs a credential. There is no channel here that talks to
    -- something unauthenticated, and one with no secret is one that fails at
    -- the moment it is first needed.
    --
    -- Emptiness is checked as well as NULL. "There is no credential" and "the
    -- credential is blank" are different mistakes and the constraint has to
    -- refuse both, or a caller passing an empty string gets past it.
    CONSTRAINT notification_channels_has_credentials
        CHECK (length(btrim(coalesce(credentials_encrypted, ''))) > 0)
);

CREATE UNIQUE INDEX notification_channels_name_idx
    ON notification_channels (server_id, lower(name));

-- ----------------------------------------------------------------- events

-- Something happened that somebody may want to hear about.
--
-- An outbox rather than a direct call from each phase to a sender. Two reasons,
-- and the second is the one that matters:
--
--   * A phase that called a sender directly would block its own loop on
--     somebody's slow SMTP server. The monitor evaluating alerts must not wait
--     on a mail relay.
--   * A process that died between "the alert opened" and "the mail was sent"
--     would lose the notification with nothing recording that it ever existed.
--     A row written in the same breath as the alert survives that.
CREATE TABLE notification_events (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,

    -- Which phase raised it, and what happened.
    source VARCHAR(30)  NOT NULL,
    kind   VARCHAR(40)  NOT NULL,
    severity VARCHAR(20) NOT NULL,

    title TEXT NOT NULL,
    body  TEXT NOT NULL,
    -- Where to go and look, as a panel path. A notification that says something
    -- is wrong without saying where to look is a notification that costs the
    -- reader more than it gives them.
    link TEXT,

    -- The identity of the *thing that happened*, not of this row.
    --
    -- One alert opening is one event however many times the monitor re-reads
    -- it, and the unique index below is what makes that true rather than
    -- hoped-for. Without it a disk sitting above its threshold for a week is
    -- ten thousand emails.
    dedupe_key VARCHAR(200) NOT NULL,

    metadata JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT notification_events_severity_valid
        CHECK (severity IN ('critical', 'high', 'warning', 'info'))
);

-- One event per thing that happened. This is the whole of the panel's
-- protection against flooding, and it is an index rather than a check in Go
-- because two API processes racing would each pass a check.
CREATE UNIQUE INDEX notification_events_dedupe_idx
    ON notification_events (server_id, dedupe_key);

CREATE INDEX notification_events_recent_idx
    ON notification_events (server_id, created_at DESC);

-- ------------------------------------------------------------- deliveries

-- One row per (event, channel): the attempt to get one thing to one place.
--
-- Separate from the event because an event delivered to three channels can
-- succeed at two of them, and a single status on the event would have to pick
-- one of those to report.
CREATE TABLE notification_deliveries (
    id      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id UUID NOT NULL REFERENCES notification_events (id) ON DELETE CASCADE,
    -- ON DELETE CASCADE: a delivery to a channel that no longer exists is a row
    -- describing an attempt to reach nowhere. The *event* survives, which is
    -- what the history is about.
    channel_id UUID NOT NULL REFERENCES notification_channels (id) ON DELETE CASCADE,

    status VARCHAR(20) NOT NULL DEFAULT 'pending',

    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- The reason it failed, kept even after it eventually succeeds: "it worked
    -- on the fourth try" is worth knowing about a channel somebody depends on.
    last_error TEXT,
    sent_at    TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT notification_deliveries_status_valid
        CHECK (status IN ('pending', 'sent', 'failed')),
    CONSTRAINT notification_deliveries_sent_has_time
        CHECK (status <> 'sent' OR sent_at IS NOT NULL),
    -- A failed delivery always says why. A failure with no reason is a failure
    -- nobody can act on, and acting on it is the entire point of storing it.
    CONSTRAINT notification_deliveries_failed_is_explained
        CHECK (status <> 'failed' OR length(btrim(coalesce(last_error, ''))) > 0)
);

-- One delivery per event per channel. Without it, a dispatcher restarted
-- mid-run would queue the same message again.
CREATE UNIQUE INDEX notification_deliveries_unique_idx
    ON notification_deliveries (event_id, channel_id);

-- The dispatcher's working set: what is due, oldest first.
CREATE INDEX notification_deliveries_due_idx
    ON notification_deliveries (status, next_attempt_at)
    WHERE status = 'pending';

CREATE INDEX notification_deliveries_channel_idx
    ON notification_deliveries (channel_id, created_at DESC);

-- ------------------------------------------------------------- permission

-- Configuring where alerts go is its own permission.
--
-- Not server.manage: a channel holds an SMTP password or a bot token, and
-- changing where the panel sends its alerts is how somebody quietly stops them
-- arriving. It is granted to admin only — unlike monitor.manage, which
-- operators need at three in the morning. Silencing every alert on the machine
-- is not a three-in-the-morning decision.
INSERT INTO permissions (name, description) VALUES
    ('notification.manage', 'Configure notification channels and see what was delivered')
ON CONFLICT (name) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.name = 'notification.manage'
WHERE r.name = 'admin'
ON CONFLICT DO NOTHING;
