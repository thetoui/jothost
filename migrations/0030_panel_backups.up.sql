-- Backups of the panel's own database (docs/PANEL_BACKUP.md).
--
-- Only the type is new. A panel backup names no website and no database: what
-- it dumps is fixed by the Agent's configuration, not by anything a row could
-- say, so there is nothing else here to constrain.

ALTER TABLE backups
    DROP CONSTRAINT backups_type_valid,
    ADD CONSTRAINT backups_type_valid
        CHECK (type IN ('website', 'database', 'full', 'panel'));

ALTER TABLE backup_schedules
    DROP CONSTRAINT backup_schedules_type_valid,
    ADD CONSTRAINT backup_schedules_type_valid
        CHECK (type IN ('website', 'database', 'full', 'panel'));
