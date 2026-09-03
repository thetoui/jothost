-- Reverses 0016.
--
-- The raw samples in system_metrics are left alone: they are Phase 3's and this
-- migration never touched them. What goes is the aggregated history, which
-- cannot be recovered once the raw samples behind it have been pruned — that is
-- the real cost of rolling this back, and it is worth saying out loud rather
-- than discovering.
DROP TABLE IF EXISTS alerts;
DROP TABLE IF EXISTS alert_rules;
DROP TABLE IF EXISTS service_states;
DROP TABLE IF EXISTS metric_rollups;

-- The permission goes with them. role_permissions references it, so the grants
-- are removed first rather than left as rows pointing at nothing.
DELETE FROM role_permissions
WHERE permission_id IN (SELECT id FROM permissions WHERE name = 'monitor.manage');
DELETE FROM permissions WHERE name = 'monitor.manage';
