-- Reverse Phase 4.5.
--
-- The configuration on the host is not touched. A down migration is a schema
-- operation; reaching through it to rewrite every vhost and stop Apache would
-- make rolling back the schema the most disruptive command in the system.
--
-- What that means in practice: a host left in hybrid mode after this runs is
-- still serving through Apache, and the panel no longer knows it. Switch the
-- host back to nginx before rolling this back.
DROP INDEX websites_apache_port_idx;

ALTER TABLE websites
    DROP CONSTRAINT websites_apache_port_range;

ALTER TABLE websites
    DROP COLUMN allow_override,
    DROP COLUMN apache_port;

ALTER TABLE servers
    DROP CONSTRAINT servers_webserver_mode_valid;

ALTER TABLE servers
    DROP COLUMN webserver_mode;
