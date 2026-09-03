-- Reverses 0018.
--
-- What is lost is first_seen_at: how long this host has been wrong about each
-- thing. The findings themselves come back on the next scan, because they are
-- derived from the host rather than authored — but "we have allowed password
-- logins since March" is not derivable, and it is usually the sentence that
-- gets something fixed.
--
-- The record of accepted risks goes too, with who accepted them and why. Those
-- are decisions people made, not facts about the machine, and nothing will
-- reconstruct them.
DROP TABLE IF EXISTS security_scans;
DROP TABLE IF EXISTS security_findings;

-- The ordering function goes with the table that used it.
DROP FUNCTION IF EXISTS severity_rank(TEXT);

-- The permission goes with them. role_permissions references it, so the grants
-- are removed first rather than left as rows pointing at nothing.
DELETE FROM role_permissions
WHERE permission_id IN (SELECT id FROM permissions WHERE name = 'security.view');
DELETE FROM permissions WHERE name = 'security.view';
