-- Takes 'panel' back out of the backup types.
--
-- Refused while any panel backup or schedule is recorded, rather than deleting
-- them. The rows are the only record of where each sealed archive was stored,
-- and a rollback that quietly discarded them would leave encrypted copies of
-- the panel's database scattered across destinations with nothing pointing at
-- them. Delete them from the panel first if that is really what is wanted.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM backups WHERE type = 'panel')
       OR EXISTS (SELECT 1 FROM backup_schedules WHERE type = 'panel') THEN
        RAISE EXCEPTION 'panel backups or schedules are still recorded; delete them before rolling back migration 0030';
    END IF;
END
$$;

ALTER TABLE backups
    DROP CONSTRAINT backups_type_valid,
    ADD CONSTRAINT backups_type_valid
        CHECK (type IN ('website', 'database', 'full'));

ALTER TABLE backup_schedules
    DROP CONSTRAINT backup_schedules_type_valid,
    ADD CONSTRAINT backup_schedules_type_valid
        CHECK (type IN ('website', 'database', 'full'));
