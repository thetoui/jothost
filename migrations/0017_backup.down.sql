-- Reverses 0017.
--
-- What is lost is the record of every backup: where it is, how big it was, and
-- the checksum that says whether it is intact. The archives themselves are on
-- their destinations and survive this, but a panel that has forgotten their
-- checksums can no longer tell a good one from a truncated one. That is the
-- real cost of rolling this back and it is worth stating rather than
-- discovering.
--
-- The constraint is dropped first: backups references backup_schedules, so
-- dropping the schedules table while the constraint stands would fail.
ALTER TABLE backups DROP CONSTRAINT IF EXISTS backups_schedule_fk;

DROP TABLE IF EXISTS backup_schedules;
DROP TABLE IF EXISTS backups;
DROP TABLE IF EXISTS backup_destinations;

-- backup.manage is left alone. Migration 0002 created it and 0002 is what
-- removes it; deleting it here would revoke a permission this migration never
-- granted.
