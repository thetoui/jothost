-- Phase 4.5: the hybrid web server arrangement — nginx in front, Apache behind.
-- Implements DATABASE.md tables 6 and 9 (extended).

-- ------------------------------------------------------------------ servers

-- Which web server arrangement this host runs.
--
-- It is a property of the host, not of a site: both servers are one process
-- tree serving every site on the machine, and a panel that let one site opt in
-- would be a panel that has to run Apache for one site and explain to everyone
-- else why their memory went.
ALTER TABLE servers
    ADD COLUMN webserver_mode VARCHAR(20) NOT NULL DEFAULT 'nginx';

ALTER TABLE servers
    ADD CONSTRAINT servers_webserver_mode_valid
        CHECK (webserver_mode IN ('nginx', 'hybrid'));

-- ----------------------------------------------------------------- websites

ALTER TABLE websites
    -- The loopback port Apache serves this site on in hybrid mode.
    --
    -- Kept when the host goes back to nginx alone, rather than cleared: the
    -- number is this site's for as long as it exists, so switching modes twice
    -- does not renumber every backend and rewrite every configuration file for
    -- no reason.
    ADD COLUMN apache_port INTEGER,
    -- Whether Apache reads .htaccess for this site.
    --
    -- Defaults to on, because .htaccess is what hybrid mode is turned on for:
    -- a host that enabled Apache and then found rewrites ignored would have
    -- gained nothing but a second process. It is per-site because reading a
    -- .htaccess in every directory of every request is not free, and a site
    -- that does not need it should not pay.
    ADD COLUMN allow_override BOOLEAN NOT NULL DEFAULT TRUE;

ALTER TABLE websites
    -- The same range as shared/validate.BackendPort. Repeated here because
    -- this value becomes a Listen directive in a file a root process reads,
    -- and the database is the last place it can be constrained.
    ADD CONSTRAINT websites_apache_port_range
        CHECK (apache_port IS NULL
            OR apache_port BETWEEN 7080 AND 7979);

-- A backend port is a host-wide resource: two sites on one port means one
-- Apache vhost silently serving the other's traffic, because the first
-- VirtualHost on a port answers for every name that matches no other.
CREATE UNIQUE INDEX websites_apache_port_idx
    ON websites (server_id, apache_port)
    WHERE apache_port IS NOT NULL;

-- Note on the other port table.
--
-- node_apps.port has its own uniqueness and its own range (1024-32767), which
-- overlaps this one. Two tables cannot be constrained against each other
-- without a trigger on both, and a trigger that fires on every website write to
-- scan another table earns its cost only if the collision is otherwise
-- unnoticed — it would not be: it is checked in Go when either port is
-- assigned, in both directions, and a taken port is refused with a message
-- naming what holds it.
