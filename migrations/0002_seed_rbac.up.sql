-- Phase 1: seed the permission catalogue and the built-in roles.
--
-- Idempotent: re-running inserts nothing new, so a repaired or partially
-- applied database converges rather than erroring.

-- --------------------------------------------------------------- permissions

INSERT INTO permissions (name, description) VALUES
    ('server.view',      'View server information and metrics'),
    ('server.manage',    'Manage server services and configuration'),
    ('website.view',     'View websites and domains'),
    ('website.create',   'Create websites'),
    ('website.update',   'Update websites'),
    ('website.delete',   'Delete websites'),
    ('database.manage',  'Create, modify, and delete databases and database users'),
    ('ssl.manage',       'Issue, renew, and revoke SSL certificates'),
    ('firewall.manage',  'Modify firewall rules'),
    ('backup.manage',    'Create, restore, and delete backups'),
    ('file.read',        'Browse and download files'),
    ('file.write',       'Upload, edit, and delete files'),
    ('cron.manage',      'Manage scheduled jobs'),
    ('dns.manage',       'Manage DNS records'),
    ('audit.view',       'View audit logs'),
    ('user.manage',      'Create, modify, and delete panel users')
ON CONFLICT (name) DO NOTHING;

-- --------------------------------------------------------------------- roles

INSERT INTO roles (name, description) VALUES
    ('admin',    'Full control over the server and the panel'),
    ('operator', 'Day-to-day hosting operations without user or firewall control'),
    ('viewer',   'Read-only access')
ON CONFLICT (name) DO NOTHING;

-- ---------------------------------------------------------- role_permissions

-- admin: every permission, including any added by a later migration that
-- re-runs this grant.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
CROSS JOIN permissions p
WHERE r.name = 'admin'
ON CONFLICT DO NOTHING;

-- operator: hosting operations, but not user management, firewall, or
-- server-level configuration.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.name IN (
    'server.view',
    'website.view', 'website.create', 'website.update', 'website.delete',
    'database.manage', 'ssl.manage', 'backup.manage',
    'file.read', 'file.write', 'cron.manage', 'dns.manage'
)
WHERE r.name = 'operator'
ON CONFLICT DO NOTHING;

-- viewer: read-only.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.name IN ('server.view', 'website.view', 'file.read')
WHERE r.name = 'viewer'
ON CONFLICT DO NOTHING;
