-- Phase 14: backups, their destinations, and their schedules.

-- ----------------------------------------------------------- destinations

-- Where backups are written.
--
-- DATABASE.md section 23 puts destination_type and destination_config_encrypted
-- on the schedule itself. That is one table fewer and it is wrong in three
-- ways, so it is deliberately not what this migration builds:
--
--   * A manual backup needs a destination too, and the spec's shape gives it
--     nowhere to come from but a copy of the schedule's.
--   * Credentials would be duplicated per schedule. Rotating an S3 key would
--     mean editing every schedule that used it, and missing one means a backup
--     that silently stops working.
--   * A backup row could name a destination that no longer describes anything,
--     because there would be nothing to reference.
--
-- So a destination is a row of its own, and both a schedule and a backup point
-- at it. The divergence is recorded in docs/PHASE14.md section 4.
CREATE TABLE backup_destinations (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,

    name VARCHAR(100) NOT NULL,
    -- local writes to a directory on this host, s3 to an S3-compatible bucket,
    -- sftp to another machine over SSH.
    kind VARCHAR(20) NOT NULL,

    -- Everything the store needs that is not a secret: a directory, a bucket,
    -- an endpoint, a hostname, a port, a prefix. Kept as JSON because the three
    -- kinds genuinely have different fields, and columns that are NULL for two
    -- kinds out of three teach a reader nothing.
    config JSONB NOT NULL DEFAULT '{}'::jsonb,

    -- AES-256-GCM, bound to this row's id as additional authenticated data, so
    -- a ciphertext moved from another row fails to decrypt rather than quietly
    -- authenticating somewhere it should not (DATABASE.md section 30).
    --
    -- NULL for a local destination, which has no secret. Not an empty string:
    -- "there is no credential" and "the credential is blank" are different
    -- facts and only one of them is a misconfiguration.
    credentials_encrypted TEXT,

    -- Whether the panel has ever successfully written to and read back from
    -- this destination, and what happened when it last tried.
    --
    -- A destination that has never been reached is the most dangerous object in
    -- this phase: it looks like protection and is not. The panel shows this.
    last_check_at     TIMESTAMPTZ,
    last_check_ok     BOOLEAN,
    last_check_detail TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT backup_destinations_kind_valid
        CHECK (kind IN ('local', 's3', 'sftp')),
    CONSTRAINT backup_destinations_name_present
        CHECK (length(btrim(name)) > 0),
    -- A local destination has no credential and the other two always do. This
    -- is the one cross-field rule worth putting in the schema: an S3
    -- destination with no secret key is one that fails at the worst moment.
    CONSTRAINT backup_destinations_credentials_match_kind
        CHECK (
            (kind = 'local' AND credentials_encrypted IS NULL)
            OR (kind <> 'local' AND credentials_encrypted IS NOT NULL)
        )
);

CREATE UNIQUE INDEX backup_destinations_name_idx
    ON backup_destinations (server_id, lower(name));

-- ---------------------------------------------------------------- backups

CREATE TABLE backups (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,

    -- The website this is a backup of, for a website backup. NULL for a
    -- database or full backup.
    --
    -- ON DELETE SET NULL, not CASCADE. Deleting a website must not delete the
    -- backups of it: the moment somebody most needs last night's copy of a site
    -- is immediately after deleting the site.
    website_id UUID REFERENCES websites (id) ON DELETE SET NULL,
    -- Likewise for a database backup, and for the same reason.
    database_id UUID REFERENCES databases (id) ON DELETE SET NULL,

    -- What the website or database was called when this was taken. The
    -- references above go NULL when the thing is deleted, and a backup that can
    -- no longer say what it is a backup of is unrestorable in practice.
    subject VARCHAR(255) NOT NULL DEFAULT '',

    type VARCHAR(30) NOT NULL,

    destination_id UUID REFERENCES backup_destinations (id) ON DELETE SET NULL,
    -- The destination's name at the time, kept for the same reason as subject:
    -- a listing must stay readable after a destination is renamed.
    destination VARCHAR(255) NOT NULL DEFAULT '',

    -- The object key within the destination. Not a host path: for S3 and SFTP
    -- there is no host path, and for local it is joined to the destination's
    -- directory by the Agent rather than trusted from here.
    path TEXT,
    size_bytes BIGINT,

    -- SHA-256 of the archive as it was written. This is what makes verifying
    -- mean something: without it, verify can only say a file exists.
    checksum VARCHAR(64),

    status VARCHAR(30) NOT NULL DEFAULT 'pending',
    -- Whether the archive has been read back from the destination and found to
    -- match. Separate from status on purpose: "the upload returned success" and
    -- "the bytes are there and correct" are different claims, and only the
    -- second is a backup.
    verified_at   TIMESTAMPTZ,
    verify_detail TEXT,

    -- What went into it: the file count, the databases, the byte totals. Read
    -- from the archive's own manifest after it was written, not from what was
    -- requested.
    manifest JSONB,

    error TEXT,
    -- The job that produced it, so a running backup can be followed and a
    -- failed one can be traced to its log.
    job_id UUID REFERENCES jobs (id) ON DELETE SET NULL,
    -- The schedule that asked for it, or NULL for one somebody took by hand.
    -- The reference is added below, once that table exists.
    schedule_id UUID,
    created_by  UUID REFERENCES users (id) ON DELETE SET NULL,

    started_at   TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT backups_type_valid
        CHECK (type IN ('website', 'database', 'full')),
    CONSTRAINT backups_status_valid
        CHECK (status IN ('pending', 'running', 'completed', 'failed', 'deleting')),
    -- A completed backup that cannot say where it is or how big it is is not a
    -- backup, it is a row. The check is what stops a partial write being
    -- recorded as a success by some future code path.
    CONSTRAINT backups_completed_is_addressable
        CHECK (
            status <> 'completed'
            OR (path IS NOT NULL AND checksum IS NOT NULL AND size_bytes IS NOT NULL)
        )
);

CREATE INDEX backups_server_idx ON backups (server_id, created_at DESC);
CREATE INDEX backups_website_idx ON backups (website_id, created_at DESC);
CREATE INDEX backups_schedule_idx ON backups (schedule_id, created_at DESC);

-- -------------------------------------------------------------- schedules

CREATE TABLE backup_schedules (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,

    name VARCHAR(100) NOT NULL,
    type VARCHAR(30) NOT NULL,
    -- The website a website schedule backs up. NULL means every website, which
    -- is what an operator almost always wants and what a per-site schedule
    -- cannot express without one row per site.
    website_id  UUID REFERENCES websites (id) ON DELETE CASCADE,
    database_id UUID REFERENCES databases (id) ON DELETE CASCADE,

    destination_id UUID NOT NULL REFERENCES backup_destinations (id) ON DELETE RESTRICT,

    -- The hour and minute it runs, in UTC, and which days.
    --
    -- Not a cron expression, and not a crontab entry. Backing up needs root and
    -- a crontab entry runs as somebody; and a schedule written into a file the
    -- panel does not own is a schedule the panel can no longer answer questions
    -- about. The scheduler in the API decides when this is due, the same
    -- arrangement Phase 21 uses for updates.
    hour   SMALLINT NOT NULL DEFAULT 3,
    minute SMALLINT NOT NULL DEFAULT 0,
    -- 0-6, Sunday first; -1 means every day.
    day_of_week SMALLINT NOT NULL DEFAULT -1,

    -- How long a backup this schedule took is kept, and how many are kept
    -- regardless of age.
    --
    -- keep_last exists because retention by age alone deletes everything you
    -- have the day after a panel is off for a fortnight. A count floor is what
    -- makes "keep 7 days" fail safe rather than fail empty.
    retention_days INTEGER NOT NULL DEFAULT 14,
    keep_last      INTEGER NOT NULL DEFAULT 3,

    enabled BOOLEAN NOT NULL DEFAULT TRUE,

    last_run_at    TIMESTAMPTZ,
    last_status    VARCHAR(30),
    last_backup_id UUID REFERENCES backups (id) ON DELETE SET NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT backup_schedules_type_valid
        CHECK (type IN ('website', 'database', 'full')),
    CONSTRAINT backup_schedules_name_present
        CHECK (length(btrim(name)) > 0),
    CONSTRAINT backup_schedules_hour_valid   CHECK (hour BETWEEN 0 AND 23),
    CONSTRAINT backup_schedules_minute_valid CHECK (minute BETWEEN 0 AND 59),
    CONSTRAINT backup_schedules_day_valid    CHECK (day_of_week BETWEEN -1 AND 6),
    -- A retention of zero days would delete a backup the moment it finished.
    CONSTRAINT backup_schedules_retention_valid CHECK (retention_days BETWEEN 1 AND 3650),
    -- At least one is always kept. A schedule that can prune its way to nothing
    -- is a schedule that eventually does.
    CONSTRAINT backup_schedules_keep_last_valid CHECK (keep_last BETWEEN 1 AND 365)
);

CREATE INDEX backup_schedules_due_idx ON backup_schedules (server_id, enabled);

-- The schedule reference on a backup is added now that the table exists. It is
-- ON DELETE SET NULL: deleting a schedule must not delete the backups it took,
-- which are the only reason the schedule existed.
ALTER TABLE backups
    ADD CONSTRAINT backups_schedule_fk
    FOREIGN KEY (schedule_id) REFERENCES backup_schedules (id) ON DELETE SET NULL;

-- ------------------------------------------------------------- permission

-- backup.manage already exists: migration 0002 seeded it and granted it to
-- admin and operator. Nothing is added here, which is worth saying so a reader
-- does not go looking for the grant this phase forgot to write.
