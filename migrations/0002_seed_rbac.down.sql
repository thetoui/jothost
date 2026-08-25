-- Reverses 0002_seed_rbac.up.sql.
--
-- Role assignments cascade from the role rows, so removing the built-in roles
-- is enough; permissions are removed by name.

DELETE FROM roles WHERE name IN ('admin', 'operator', 'viewer');

DELETE FROM permissions WHERE name IN (
    'server.view', 'server.manage',
    'website.view', 'website.create', 'website.update', 'website.delete',
    'database.manage', 'ssl.manage', 'firewall.manage', 'backup.manage',
    'file.read', 'file.write', 'cron.manage', 'dns.manage',
    'audit.view', 'user.manage'
);
