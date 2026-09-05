-- Reading the audit trail.
--
-- The trail has been written since migration 0001 and there has never been a
-- way to read it: `audit.view` was defined as a permission and guarded no
-- endpoint. The panel's answer to "who deleted that website" was to connect to
-- PostgreSQL. This adds the index the reading query needs; the endpoint itself
-- is api/internal/audit.
--
-- 0001 indexes created_at, user_id and action, which covers "everything
-- recently", "everything this person did" and "every time this happened". It
-- does not cover the question an operator actually asks first — what happened
-- to *this* website — because that filters on resource_type and resource_id,
-- and without an index it is a sequential scan over the whole history.
--
-- created_at DESC is the third column rather than a separate index: every
-- listing is newest-first, so having it here means the filter and the ordering
-- are answered by one index instead of a sort over the matches.
CREATE INDEX IF NOT EXISTS audit_logs_resource_idx
    ON audit_logs (resource_type, resource_id, created_at DESC);

-- Failures are the rare rows and the interesting ones: a partial index costs
-- almost nothing and turns "show me what was refused" into a lookup rather
-- than a scan filtering out the successes.
CREATE INDEX IF NOT EXISTS audit_logs_failures_idx
    ON audit_logs (created_at DESC)
    WHERE status <> 'SUCCESS';
