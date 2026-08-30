-- Phase 4.1: subdomains as websites in their own right.
-- Implements DATABASE.md table 9 (extended) and section 31.
--
-- A subdomain is a row in `websites` with a parent, not a separate table.
--
-- That is the decision this whole migration rests on, so it is worth stating.
-- A subdomain needs a document root, a system account, a PHP version, a
-- certificate, logs, a file manager, a database, possibly a Node application —
-- which is the list of things a website has. A separate `subdomains` table
-- would duplicate every one of them, and every feature already built would
-- have to learn that a site is sometimes one thing and sometimes another. As a
-- website row it inherits all of them unchanged, and the parent link is the
-- only new fact.
--
-- What it costs: `websites` no longer means "top-level site", so anything
-- listing sites has to say whether it wants subdomains too. That is one
-- explicit clause per query, against a duplicate of the entire feature set.

ALTER TABLE websites
    -- ON DELETE CASCADE: a subdomain cannot outlive its parent. Its name is
    -- beneath the parent's, and with a nested layout its files are inside the
    -- parent's directory — which the parent's own deletion removes. Keeping
    -- the row would leave a site the panel lists and nothing serves.
    ADD COLUMN parent_website_id UUID REFERENCES websites (id) ON DELETE CASCADE,
    -- Where the subdomain's files live: 'nested' inside the parent's
    -- directory, or 'isolated' at the top level like any other site.
    --
    -- Recorded rather than derived from the path. The two are the same fact
    -- today, but reading a mode out of a string is how a path that was
    -- hand-edited on the host becomes a mode the panel believes.
    ADD COLUMN document_root_mode VARCHAR(10),
    -- Whether the subdomain gets its own FPM pool or serves through the
    -- parent's. See shared/validate.PHPPoolMode.
    ADD COLUMN php_pool_mode VARCHAR(10),
    -- Whether the subdomain's files belong to the parent's account or to one
    -- of its own. See shared/validate.SystemUserMode.
    ADD COLUMN system_user_mode VARCHAR(10);

-- The three modes describe a subdomain and nothing else. A top-level site has
-- no parent to inherit from, so a mode on one of those rows would be a value
-- with no meaning that some later query would read anyway.
ALTER TABLE websites
    ADD CONSTRAINT websites_subdomain_modes_present
        CHECK ((parent_website_id IS NULL) = (document_root_mode IS NULL)
           AND (parent_website_id IS NULL) = (php_pool_mode IS NULL)
           AND (parent_website_id IS NULL) = (system_user_mode IS NULL)),
    ADD CONSTRAINT websites_document_root_mode_valid
        CHECK (document_root_mode IS NULL
            OR document_root_mode IN ('nested', 'isolated')),
    ADD CONSTRAINT websites_php_pool_mode_valid
        CHECK (php_pool_mode IS NULL OR php_pool_mode IN ('inherit', 'dedicated')),
    ADD CONSTRAINT websites_system_user_mode_valid
        CHECK (system_user_mode IS NULL OR system_user_mode IN ('inherit', 'dedicated')),
    -- A site cannot be its own parent. The trigger below catches the deeper
    -- cycles; this catches the one a single UPDATE can create.
    ADD CONSTRAINT websites_parent_not_self
        CHECK (parent_website_id IS NULL OR parent_website_id <> id),
    -- A dedicated account with an inherited pool would run PHP as the parent's
    -- user over files owned by the subdomain's: every write from PHP fails,
    -- and it reads as a broken deployment rather than a bad configuration.
    ADD CONSTRAINT websites_ownership_coherent
        CHECK (system_user_mode IS DISTINCT FROM 'dedicated'
            OR php_pool_mode = 'dedicated');

CREATE INDEX websites_parent_idx ON websites (parent_website_id)
    WHERE parent_website_id IS NOT NULL;

-- ------------------------------------------------- shared system accounts

-- A subdomain that inherits its parent's account shares that account's name,
-- which the old unconditional unique index forbade.
--
-- The index still has to do its original job: two *independent* sites sharing
-- an account would let a compromised one read the other's files. So the
-- uniqueness now applies to the rows that own their account, and an inheriting
-- row is excluded — its name is a copy of a parent row that is itself unique.
DROP INDEX websites_system_user_idx;
CREATE UNIQUE INDEX websites_system_user_idx ON websites (system_username)
    WHERE system_user_mode IS DISTINCT FROM 'inherit';

-- ------------------------------------------------------ wildcard names

-- A wildcard subdomain answers for every name beneath the parent that no other
-- site claims: "*.example.com". It is allowed only on a subdomain row, because
-- on a top-level site it would be a site whose own identity matches nothing.
ALTER TABLE websites DROP CONSTRAINT websites_domain_format;
ALTER TABLE websites ADD CONSTRAINT websites_domain_format
    CHECK (primary_domain ~ '^(\*\.)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$'
       AND (primary_domain NOT LIKE '*%' OR parent_website_id IS NOT NULL));

ALTER TABLE domains DROP CONSTRAINT domains_format;
ALTER TABLE domains ADD CONSTRAINT domains_format
    CHECK (domain ~ '^(\*\.)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$');

-- ------------------------------------------------------------- depth rule

-- A subdomain's parent must be a top-level site.
--
-- This cannot be a CHECK constraint: the rule is about another row, and a
-- CHECK may not run a subquery. A trigger is the only way to state it in the
-- database, and it is worth stating there — the alternative is a chain of
-- parents that every listing, every deletion and every path computation has to
-- walk, arriving at a different answer if the chain is ever cyclic.
--
-- A name several levels below the parent is still reachable: the label may
-- contain dots, so "dev.shop.example.com" is one subdomain of example.com
-- rather than a subdomain of a subdomain.
CREATE FUNCTION websites_reject_nested_subdomain() RETURNS trigger AS $$
BEGIN
    IF NEW.parent_website_id IS NOT NULL THEN
        IF EXISTS (SELECT 1 FROM websites
                    WHERE id = NEW.parent_website_id
                      AND parent_website_id IS NOT NULL) THEN
            RAISE EXCEPTION
                'a subdomain cannot be created under another subdomain'
                USING ERRCODE = 'check_violation';
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER websites_no_nested_subdomains
    BEFORE INSERT OR UPDATE OF parent_website_id ON websites
    FOR EACH ROW EXECUTE FUNCTION websites_reject_nested_subdomain();
