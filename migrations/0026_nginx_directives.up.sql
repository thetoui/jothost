-- Per-site additional nginx directives.
--
-- What Plesk calls "Additional nginx directives": a block of configuration
-- written into the site's own server block, for the rewrites, headers, caching
-- rules and proxy settings the panel does not have a field for.
--
-- This is server configuration, not website content, and it is gated on
-- server.manage rather than website.update for that reason. A customer who
-- could write directives into their own vhost could serve any file the web
-- server can read; somebody who already holds server.manage can edit nginx by
-- hand over SSH, so the feature grants nothing new to the people who have it.
--
-- Empty string rather than NULL. Every site has this setting and most sites
-- have nothing in it, so "no directives" is a value rather than an absence —
-- which keeps every read of the column from needing a null check for a state
-- that means the same as the empty one.
ALTER TABLE websites
    ADD COLUMN IF NOT EXISTS nginx_directives TEXT NOT NULL DEFAULT '';

-- Bounded in the database as well as in the validator. The application refuses
-- anything larger, and a constraint here means a row that got past it some
-- other way is refused too, rather than becoming a vhost nobody can read.
-- IF NOT EXISTS on the column and a guarded add for the constraint, so a
-- partially applied migration heals on the next run rather than wedging the
-- API in a restart loop. Postgres has no ADD CONSTRAINT IF NOT EXISTS.
DO $$
BEGIN
    ALTER TABLE websites
        ADD CONSTRAINT websites_nginx_directives_bounded
        CHECK (length(nginx_directives) <= 16384);
EXCEPTION
    WHEN duplicate_object THEN NULL;
END
$$;
