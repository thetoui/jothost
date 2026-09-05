-- Reverses 0023.
--
-- What is lost is who owned what: the account hierarchy, every plan and its
-- limits, every subscription and the websites' membership of one, the measured
-- usage, and the record of every impersonation.
--
-- What is *not* lost is anything a customer would notice. The websites, their
-- databases, mailboxes and files are untouched — this migration only ever
-- added a column pointing at a subscription. A panel rolled back to here
-- serves exactly the same sites and has forgotten who they were sold to.
--
-- The order matters: the column referencing subscriptions goes before the
-- subscriptions, and the add-ons and usage before the plans they point at.

ALTER TABLE websites DROP COLUMN IF EXISTS subscription_id;

DROP TABLE IF EXISTS impersonation_sessions;
DROP TABLE IF EXISTS subscription_usage;
DROP TABLE IF EXISTS subscription_addons;
DROP TABLE IF EXISTS subscriptions;
DROP TABLE IF EXISTS service_plans;

ALTER TABLE users
    DROP CONSTRAINT IF EXISTS users_not_their_own_parent,
    DROP CONSTRAINT IF EXISTS users_tier_has_a_parent,
    DROP CONSTRAINT IF EXISTS users_tier_valid;

ALTER TABLE users
    DROP COLUMN IF EXISTS company,
    DROP COLUMN IF EXISTS full_name,
    DROP COLUMN IF EXISTS parent_user_id,
    DROP COLUMN IF EXISTS tier;

-- The audit log's foreign key comes back.
--
-- Restoring it can fail on a database where an account has been deleted since:
-- its rows now name an id that is not there. NOT VALID adds the constraint for
-- future rows without re-checking the past ones, which is the only way back
-- that does not require destroying audit history to satisfy a constraint.
-- Dropped first so a rollback is repeatable: a database that has been through
-- this once already has the constraint, and ADD would fail on it.
ALTER TABLE audit_logs DROP CONSTRAINT IF EXISTS audit_logs_user_id_fkey;

ALTER TABLE audit_logs
    ADD CONSTRAINT audit_logs_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE SET NULL NOT VALID;

-- The reseller role and the tenancy permissions go with them. role_permissions
-- references both, so the grants are removed first rather than left as rows
-- pointing at nothing.
DELETE FROM role_permissions
WHERE role_id IN (SELECT id FROM roles WHERE name = 'reseller');

DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions
    WHERE name IN ('tenant.view', 'tenant.manage', 'tenant.impersonate')
);

-- A user still holding the reseller role would keep the role row alive, so the
-- assignments go too. This is the one place the rollback destroys something an
-- operator chose: an account that was a reseller comes back with no role.
DELETE FROM user_roles
WHERE role_id IN (SELECT id FROM roles WHERE name = 'reseller');

DELETE FROM roles WHERE name = 'reseller';
DELETE FROM permissions
WHERE name IN ('tenant.view', 'tenant.manage', 'tenant.impersonate');
