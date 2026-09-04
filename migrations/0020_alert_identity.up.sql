-- Phase 20 dependency: an alert belongs to the rule that raised it.
--
-- Migration 0016 keyed one open alert per (server, metric, target, severity),
-- which is right about the *thing being watched* and wrong about who is
-- watching it. Two rules can legitimately watch the same filesystem at the same
-- severity — a general "any disk above 85%" and a specific "this one above 60%"
-- — and 0016's own uniqueness on alert_rules permits it, because "" and
-- "/var/www" are different targets.
--
-- Under the old key those two rules share one alert row and fight over it. On
-- every evaluation the rule that is not breaching resolves the alert the other
-- one just opened, and the next tick reverses it. Phase 19 logged that quietly
-- and it looked like nothing.
--
-- Phase 20 made it visible and harmful: alert churn becomes one email a minute,
-- which is precisely the flood that phase exists to prevent. So the smallest
-- correct fix is here rather than there — CLAUDE.md section 21 — and the fix is
-- to say what was always meant: an alert is one rule's opinion about one
-- target.
--
-- Severity leaves the key because it is a property of the rule, not of the
-- alert: one rule has one severity, so keying on the rule already covers it.
DROP INDEX IF EXISTS alerts_open_unique_idx;

-- Any alert already orphaned by a deleted rule is resolved rather than carried
-- forward. It could never have been resolved by the monitor — there is no rule
-- left to evaluate — so it was going to sit open forever, and under the new key
-- it would also escape the index entirely.
UPDATE alerts
SET status = 'resolved', resolved_at = COALESCE(resolved_at, now())
WHERE status = 'open' AND rule_id IS NULL;

-- Two rules watching one target at the same severity may each have opened an
-- alert under the old key's DO UPDATE, which collapsed them into one row. There
-- is no duplicate to clean up: the old index guaranteed at most one. What the
-- new index guarantees is one per rule.
CREATE UNIQUE INDEX alerts_open_unique_idx
    ON alerts (server_id, rule_id, target) WHERE status = 'open';
