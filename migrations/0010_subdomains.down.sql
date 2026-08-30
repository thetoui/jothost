-- Reverse Phase 4.1.
--
-- Subdomain rows are deleted rather than left behind. Without the parent
-- column a subdomain row is indistinguishable from a top-level website: the
-- panel would list "shop.example.com" as an independent site, and the next
-- deletion of it would take the parent's nested directory with it. Removing
-- the rows loses the panel's record of those sites; leaving them loses the
-- fact that makes the record safe to act on.
--
-- The vhosts and files stay on the host. A down migration is a schema
-- operation, and reaching through it to delete a customer's content would make
-- rolling back the schema the most destructive command in the system.
DELETE FROM websites WHERE parent_website_id IS NOT NULL;

DROP TRIGGER websites_no_nested_subdomains ON websites;
DROP FUNCTION websites_reject_nested_subdomain();

ALTER TABLE domains DROP CONSTRAINT domains_format;
ALTER TABLE domains ADD CONSTRAINT domains_format
    CHECK (domain ~ '^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$');

ALTER TABLE websites DROP CONSTRAINT websites_domain_format;
ALTER TABLE websites ADD CONSTRAINT websites_domain_format
    CHECK (primary_domain ~ '^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$');

DROP INDEX websites_system_user_idx;
CREATE UNIQUE INDEX websites_system_user_idx ON websites (system_username);

DROP INDEX websites_parent_idx;

ALTER TABLE websites
    DROP CONSTRAINT websites_ownership_coherent,
    DROP CONSTRAINT websites_parent_not_self,
    DROP CONSTRAINT websites_system_user_mode_valid,
    DROP CONSTRAINT websites_php_pool_mode_valid,
    DROP CONSTRAINT websites_document_root_mode_valid,
    DROP CONSTRAINT websites_subdomain_modes_present;

ALTER TABLE websites
    DROP COLUMN system_user_mode,
    DROP COLUMN php_pool_mode,
    DROP COLUMN document_root_mode,
    DROP COLUMN parent_website_id;
