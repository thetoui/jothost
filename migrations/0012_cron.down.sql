-- Reverses 0012_cron.up.sql.
--
-- The indexes go with the table; naming them here would be redundant, and a
-- DROP INDEX that ran before the DROP TABLE would fail on a re-run.
DROP TABLE IF EXISTS cron_jobs;
