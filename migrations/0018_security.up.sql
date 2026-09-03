-- Phase 15: the Security Center — findings, scans, and the score.

-- ------------------------------------------------------------------ rank

-- The severity scale as a sortable number, worst first.
--
-- A function rather than an ORDER BY CASE repeated at every call site, and
-- rather than an enum type. The panel sorts findings by severity in several
-- places and re-opens an accepted finding when its severity *rises*, and both
-- of those have to mean exactly what shared/validate.FindingRank means. Two
-- copies of an ordering are two things that can drift.
--
-- An unknown severity ranks last rather than first, matching the Go side: a
-- value this panel does not understand must never sort above a critical finding
-- and push it off the top of the page.
CREATE FUNCTION severity_rank(severity TEXT) RETURNS INTEGER
    LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT CASE severity
        WHEN 'critical' THEN 0
        WHEN 'high'     THEN 1
        WHEN 'medium'   THEN 2
        WHEN 'low'      THEN 3
        WHEN 'info'     THEN 4
        ELSE 5
    END
$$;

-- --------------------------------------------------------------- findings

-- What is wrong with this host, one row per thing.
--
-- DATABASE.md section 27 specifies id, server_id, severity, category, title,
-- description, status, metadata and the two timestamps. All of them are here.
-- The additions below are what a *rescan* needs, and a findings table that
-- cannot be rescanned is a findings table that doubles in size every hour.
CREATE TABLE security_findings (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,

    -- Which scanner produced it. Separate from category so the panel can say
    -- "the SSL scanner could not run" and mean something precise.
    scanner  VARCHAR(30)  NOT NULL,
    severity VARCHAR(20)  NOT NULL,
    category VARCHAR(100) NOT NULL,
    title    VARCHAR(255) NOT NULL,
    description TEXT,

    -- What to do about it. A finding without a next step is a nag, and a panel
    -- full of nags is one whose security page nobody opens twice.
    remediation TEXT,

    -- The stable identity of the *thing being reported*, not of this row.
    --
    -- Without it every scan inserts a fresh copy of "SSH permits password
    -- authentication" and an operator watching a nightly scan accumulates
    -- three hundred rows describing one setting. With it, a rescan updates the
    -- row it already has — and first_seen_at means something: how long this
    -- host has been wrong about this.
    fingerprint VARCHAR(200) NOT NULL,

    status VARCHAR(30) NOT NULL DEFAULT 'open',
    metadata JSONB,

    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at   TIMESTAMPTZ,

    -- Accepting is not resolving, and the columns are separate for the same
    -- reason Phase 19 keeps acknowledged_at apart from an alert's status:
    -- whether a condition still holds is the machine's to decide. A person can
    -- say "we know, and we accept it" — with a reason, recorded, forever.
    accepted_at     TIMESTAMPTZ,
    accepted_by     UUID REFERENCES users (id) ON DELETE SET NULL,
    accepted_reason TEXT,
    -- The severity at the moment it was accepted.
    --
    -- This is what makes accepting safe. Accepting "SSH listens on port 22" is
    -- not accepting "SSH permits root login with a password", and a finding
    -- whose severity rises above what somebody accepted is re-opened rather
    -- than staying quietly accepted at its new, worse level.
    accepted_severity VARCHAR(20),

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT security_findings_severity_valid
        CHECK (severity IN ('critical', 'high', 'medium', 'low', 'info')),
    CONSTRAINT security_findings_status_valid
        CHECK (status IN ('open', 'accepted', 'resolved')),
    -- An accepted finding always records who and why. An acceptance with no
    -- reason is a mute button, and a mute button on a security page is how a
    -- real problem becomes permanent.
    CONSTRAINT security_findings_accepted_is_explained
        CHECK (
            status <> 'accepted'
            OR (accepted_at IS NOT NULL AND length(btrim(coalesce(accepted_reason, ''))) > 0)
        ),
    CONSTRAINT security_findings_resolved_has_time
        CHECK (status <> 'resolved' OR resolved_at IS NOT NULL)
);

CREATE INDEX security_findings_server_idx
    ON security_findings (server_id, severity, last_seen_at DESC);
CREATE INDEX security_findings_scanner_idx ON security_findings (server_id, scanner);

-- One live row per thing being reported.
--
-- Partial on "not resolved", so the history of something that was fixed and
-- came back is two rows — which is the correct answer: a setting that was put
-- right in March and undone in June is two incidents, and collapsing them
-- would hide the second.
CREATE UNIQUE INDEX security_findings_live_idx
    ON security_findings (server_id, fingerprint) WHERE status <> 'resolved';

-- ------------------------------------------------------------------ scans

-- One row per scan, with the score it produced.
--
-- Not in DATABASE.md, and added because two things are impossible without it.
--
-- A score is only meaningful next to when it was taken: "68" means nothing and
-- "68, an hour ago, down from 91 last week" means a great deal. And a panel
-- with no scan row cannot distinguish "this host is clean" from "this host has
-- never been looked at", which are opposite facts that a findings table alone
-- renders identically — as no rows.
CREATE TABLE security_scans (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,

    score SMALLINT NOT NULL,

    -- How many of the checks actually ran. A score computed from blanks is the
    -- failure this phase is most able to commit — TASKS.md says as much in the
    -- dependency note — so the number of checks that answered travels with the
    -- score everywhere it goes.
    checks_run   SMALLINT NOT NULL,
    checks_total SMALLINT NOT NULL,

    critical INTEGER NOT NULL DEFAULT 0,
    high     INTEGER NOT NULL DEFAULT 0,
    medium   INTEGER NOT NULL DEFAULT 0,
    low      INTEGER NOT NULL DEFAULT 0,
    info     INTEGER NOT NULL DEFAULT 0,
    accepted INTEGER NOT NULL DEFAULT 0,
    resolved INTEGER NOT NULL DEFAULT 0,

    -- Per-scanner outcomes: which ran, which could not, and why.
    scanners JSONB NOT NULL DEFAULT '[]'::jsonb,

    duration_ms INTEGER NOT NULL DEFAULT 0,
    -- Who asked, or NULL for the panel's own scheduled scan.
    triggered_by UUID REFERENCES users (id) ON DELETE SET NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT security_scans_score_valid CHECK (score BETWEEN 0 AND 100)
);

CREATE INDEX security_scans_server_idx ON security_scans (server_id, created_at DESC);

-- ------------------------------------------------------------- permission

-- Reading the security posture is its own permission.
--
-- Not server.view: a findings list is a list of the ways into this machine,
-- with the exact version and the exact path. It is the most sensitive read in
-- the panel and it belongs behind a grant somebody makes deliberately.
--
-- One permission rather than two. Splitting reading from accepting would mean
-- the people who can see "we still allow password logins" are not the people
-- who can record that it is deliberate, and the finding would then be
-- re-reported forever by a panel nobody can quiet.
INSERT INTO permissions (name, description) VALUES
    ('security.view', 'Read the security score and findings, and accept known risks')
ON CONFLICT (name) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.name = 'security.view'
WHERE r.name IN ('admin', 'operator')
ON CONFLICT DO NOTHING;
