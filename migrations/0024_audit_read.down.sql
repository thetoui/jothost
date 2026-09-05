-- Reverting only drops the indexes. The rows are untouched: audit_logs is
-- append-only by trigger, and a down migration that removed history would be
-- the one way to defeat that.
DROP INDEX IF EXISTS audit_logs_failures_idx;
DROP INDEX IF EXISTS audit_logs_resource_idx;
