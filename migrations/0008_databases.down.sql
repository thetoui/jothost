-- Reverses 0008_databases.up.sql.
--
-- Dropped child-first so the foreign keys do not have to be broken. This
-- removes the panel's record of the databases; it does not touch the database
-- servers themselves, which is deliberate — a rolled-back migration must not
-- destroy customer data.

DROP TABLE IF EXISTS database_permissions;
DROP TABLE IF EXISTS database_users;
DROP TABLE IF EXISTS databases;
