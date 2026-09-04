-- Reverses 0019.
--
-- What is lost is the delivery record: which notifications arrived, which did
-- not, and why. That is the only place a broken channel is visible — a
-- notification system cannot report its own failure through itself — so a panel
-- rolled back here goes quiet in both senses at once.
--
-- The channels go with it, credentials and all. Their secrets were encrypted
-- and nothing outside this table held them, so they cannot be recovered.
--
-- Deliveries reference both of the others, so they go first.
DROP TABLE IF EXISTS notification_deliveries;
DROP TABLE IF EXISTS notification_events;
DROP TABLE IF EXISTS notification_channels;

-- The permission goes with them. role_permissions references it, so the grants
-- are removed first rather than left as rows pointing at nothing.
DELETE FROM role_permissions
WHERE permission_id IN (SELECT id FROM permissions WHERE name = 'notification.manage');
DELETE FROM permissions WHERE name = 'notification.manage';
