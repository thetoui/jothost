-- Reverses 0009_node.up.sql.
--
-- Dropped child-first so the foreign key does not have to be broken. This
-- removes the panel's record of the applications; it does not stop anything
-- running on the host, and it does not touch a line of anybody's code.

DROP TABLE IF EXISTS node_environment;
DROP TABLE IF EXISTS node_apps;
