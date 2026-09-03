-- Reverses 0014.
--
-- The zones on the host are not removed by this. They live in files named
-- reads, and a migration that reached out to a running daemon would be a schema
-- change with a side effect nobody asked for — and one that would take a
-- customer's domain off the internet. Rolling this back leaves the host serving
-- what it was serving, with the panel no longer recording it.
--
-- The DNSSEC keys are likewise untouched. They are named's, and destroying them
-- is not reversible: a zone whose keys are gone cannot be re-signed with the
-- same keys, and resolvers hold the old DNSKEY set until it expires.
DROP TABLE IF EXISTS dns_zone_providers;
DROP TABLE IF EXISTS dns_providers;
DROP TABLE IF EXISTS dns_settings;
DROP TABLE IF EXISTS dns_records;
DROP TABLE IF EXISTS dns_zones;

-- dns.manage is not dropped. It was seeded by 0002 with the rest of the
-- permission set, so removing it here would leave a role losing a grant this
-- migration never made.
