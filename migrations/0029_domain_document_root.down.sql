-- Every domain goes back to being served from its website's directory.
--
-- The paths themselves are lost, which is the honest outcome: there is nowhere
-- else to keep them, and leaving the column behind would make this migration
-- reversible in name only.

ALTER TABLE domains
    DROP CONSTRAINT IF EXISTS domains_document_root_not_redirect,
    DROP CONSTRAINT IF EXISTS domains_document_root_absolute,
    DROP COLUMN IF EXISTS document_root;
