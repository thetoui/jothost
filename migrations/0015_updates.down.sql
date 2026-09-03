-- Reverses 0015.
--
-- The packages on the host are not touched by this. They were installed by the
-- host's own package manager and a migration that reached out to change them
-- would be a schema change that upgraded — or downgraded — a running machine.
-- Rolling this back leaves the host exactly as it is, with the panel no longer
-- recording what it applied.
--
-- The history goes with the tables, which is the real cost of rolling this back:
-- update_runs is the only record of which versions were replaced, and nothing
-- on the host holds that.
DROP TABLE IF EXISTS update_settings;
DROP TABLE IF EXISTS update_runs;
DROP TABLE IF EXISTS update_checks;

-- The permission goes too. role_permissions references it, so the grants are
-- removed first rather than left as rows pointing at nothing.
DELETE FROM role_permissions
WHERE permission_id IN (SELECT id FROM permissions WHERE name = 'update.manage');
DELETE FROM permissions WHERE name = 'update.manage';
