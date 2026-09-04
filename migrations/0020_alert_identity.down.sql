-- Reverses 0020, restoring 0016's key.
--
-- Going back reintroduces the churn this migration fixed: two rules watching
-- one target at the same severity will resolve and reopen each other's alert on
-- every evaluation, and with Phase 20 present that is an email a minute.
--
-- The restore can also fail outright. If two rules currently have open alerts
-- on the same (metric, target, severity) — which the new key permits and the
-- old one does not — the old index cannot be built until one of them is
-- resolved. That is not a fault in this migration: it is the old key being
-- unable to represent the truth.
DROP INDEX IF EXISTS alerts_open_unique_idx;

CREATE UNIQUE INDEX alerts_open_unique_idx
    ON alerts (server_id, metric, target, severity) WHERE status = 'open';
