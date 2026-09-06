-- Dropping the column discards what was written in it, which is the honest
-- behaviour: the directives only mean anything as part of a vhost this build
-- knows how to render, and a rollback stops rendering them.
--
-- The sites themselves are untouched. Their vhosts are regenerated from the
-- panel's records on the next change, so a site that had directives simply
-- stops having them rather than being left with a file nothing maintains.
ALTER TABLE websites DROP CONSTRAINT IF EXISTS websites_nginx_directives_bounded;
ALTER TABLE websites DROP COLUMN IF EXISTS nginx_directives;
