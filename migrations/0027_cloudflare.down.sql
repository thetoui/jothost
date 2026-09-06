-- Dropping the table discards the stored tokens, which is the right way round:
-- a rollback that left encrypted credentials in a table nothing reads would be
-- leaving somebody's Cloudflare token on disk for no purpose.
--
-- Nothing is removed from Cloudflare. The panel pushes records there; it does
-- not own the zone, and a down migration that deleted a customer's live DNS
-- because the panel was rolled back would be the worst kind of tidy-up.
DROP TABLE IF EXISTS dns_cloudflare;
