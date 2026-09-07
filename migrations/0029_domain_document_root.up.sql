-- A document root per domain, not only per website.
--
-- Until now every name on a site — the primary domain and each alias — was
-- served from the one directory the website recorded. That is right for an
-- alias whose whole purpose is to be another spelling of the same site, and
-- wrong for the ordinary case of one site answering for two things: a
-- marketing domain and a shop, an old name kept alive on a frozen copy, an
-- alias pointed at a staging build.
--
-- NULL means "wherever the website is served from", which is what every
-- existing row means and is why the column is nullable rather than backfilled.
-- A backfill would freeze each alias at today's path and quietly break the
-- link, so moving the site's root would stop moving its aliases with it.

ALTER TABLE domains
    ADD COLUMN document_root TEXT;

-- Absolute, and with no parent traversal in it. The same shape the websites
-- table requires, and for the same reason: this string becomes an nginx root
-- directive read by a process running as root.
ALTER TABLE domains
    ADD CONSTRAINT domains_document_root_absolute
        CHECK (document_root IS NULL
            OR (document_root LIKE '/%' AND document_root NOT LIKE '%..%'));

-- A redirect answers with a Location header and serves no files at all, so a
-- document root on one is a contradiction. Refused here rather than ignored:
-- a path somebody set and the panel silently dropped is worse than an error,
-- because it reads back as configured.
ALTER TABLE domains
    ADD CONSTRAINT domains_document_root_not_redirect
        CHECK (document_root IS NULL OR type <> 'redirect');
