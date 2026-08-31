-- Reverses 0013.
--
-- The accounts on the host are not removed by this: they live in proftpd's own
-- password file, and a migration that reached out to change the running server
-- would be a schema change with a side effect nobody asked for. Rolling this
-- back leaves the host serving what it was serving, with the panel no longer
-- recording it.
DROP TABLE IF EXISTS ftp_settings;
DROP TABLE IF EXISTS ftp_users;

-- The permission goes with them. role_permissions references it, so the grants
-- are removed first rather than left as rows pointing at nothing.
DELETE FROM role_permissions
WHERE permission_id IN (SELECT id FROM permissions WHERE name = 'ftp.manage');
DELETE FROM permissions WHERE name = 'ftp.manage';
