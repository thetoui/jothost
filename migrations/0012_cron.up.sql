-- Phase 10: scheduled jobs.
-- Implements DATABASE.md table 21, with the columns that table's five fields
-- turned out to need. Each addition is noted where it is made.

-- A job the panel schedules on the host.
--
-- It belongs to a website, and that is not a convenience: a job runs as some
-- account, and the website's own unprivileged account is the only one the panel
-- will use. A job with no website would have no account to run as, and the
-- alternative — running it as root — is the thing this design exists to make
-- impossible. There is no column for a user for the same reason.
CREATE TABLE cron_jobs (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Recorded alongside the website so the panel can list a host's jobs
    -- without joining through every site, which is what the jobs page does.
    server_id  UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    -- ON DELETE CASCADE: a job is the website's own work. Deleting the site
    -- removes the account the job ran as, so keeping the record would leave a
    -- schedule that could never run again.
    website_id UUID NOT NULL REFERENCES websites (id) ON DELETE CASCADE,

    -- Not in DATABASE.md table 21, added here: a crontab of twelve entries
    -- distinguished only by their command lines is unreadable, and the name is
    -- what the panel shows and what the job's log is labelled with.
    name VARCHAR(60) NOT NULL,

    -- Which kind of job this is: 'php', 'url' or 'command'. Two of the three
    -- have the panel build the command line itself, from a script path or a
    -- URL, so the common cases carry no free-form command at all. Recorded
    -- rather than inferred from the command, because the panel has to show the
    -- form the operator filled in when they come back to edit it.
    job_type VARCHAR(20) NOT NULL,

    -- A five-field expression, already normalised — the @-shorthands are
    -- expanded before they get here, so everything downstream deals with one
    -- representation.
    schedule VARCHAR(100) NOT NULL,

    -- What the operator asked for: a script path, a URL, or a command line.
    -- Kept separately from the rendered command below so that editing a job
    -- shows what was typed rather than what it became.
    target TEXT NOT NULL,

    -- The command line actually written into the crontab. Stored rather than
    -- rebuilt on demand, because the two must not be able to disagree: what
    -- the panel shows as "what this runs" has to be the string the daemon runs.
    command TEXT NOT NULL,

    enabled BOOLEAN NOT NULL DEFAULT TRUE,

    -- The outcome of the last run the panel knows about. It knows about manual
    -- runs, and it learns about scheduled ones from the job's log; a job that
    -- has only ever run on its schedule shows no status rather than a wrong
    -- one.
    last_run_at    TIMESTAMPTZ,
    last_status    VARCHAR(30),
    last_exit_code INTEGER,
    last_duration_ms BIGINT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT cron_jobs_type_valid
        CHECK (job_type IN ('php', 'url', 'command')),
    CONSTRAINT cron_jobs_status_valid
        CHECK (last_status IS NULL OR last_status IN ('success', 'failed')),

    -- The database is the last place these can be constrained, and what it is
    -- constraining is not a matter of tidiness: a crontab is a line-oriented
    -- format with no quoting, so a value containing a newline is not a long
    -- value, it is a second entry running whatever the caller chose.
    CONSTRAINT cron_jobs_name_single_line
        CHECK (name !~ '[\n\r]' AND length(trim(name)) > 0),
    CONSTRAINT cron_jobs_schedule_single_line
        CHECK (schedule !~ '[\n\r%]' AND length(trim(schedule)) > 0),
    CONSTRAINT cron_jobs_command_single_line
        CHECK (command !~ '[\n\r%]' AND length(trim(command)) > 0),
    CONSTRAINT cron_jobs_target_single_line
        CHECK (target !~ '[\n\r]' AND length(trim(target)) > 0),
    CONSTRAINT cron_jobs_command_length CHECK (length(command) <= 500),
    CONSTRAINT cron_jobs_target_length CHECK (length(target) <= 500)
);

-- The jobs page lists a host's jobs newest first, and a website's page lists
-- that site's.
CREATE INDEX cron_jobs_server_idx ON cron_jobs (server_id, created_at DESC);
CREATE INDEX cron_jobs_website_idx ON cron_jobs (website_id);

-- Two jobs on one site may share a schedule, and may share a command, but a
-- name that appears twice makes the list and the log picker ambiguous.
CREATE UNIQUE INDEX cron_jobs_name_idx ON cron_jobs (website_id, lower(name));
