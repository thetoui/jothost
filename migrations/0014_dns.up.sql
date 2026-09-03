-- Phase 13: DNS zones, their records, and the name server's settings.

-- One zone this host is authoritative for.
--
-- A zone rather than a domain: they are usually the same name, but a reverse
-- zone is not a domain anybody owns, and a website can be served without the
-- panel serving DNS for it at all. website_id is therefore nullable, and is a
-- convenience — "which site is this zone for" — rather than the identity.
CREATE TABLE dns_zones (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    -- ON DELETE SET NULL: deleting a website does not delete its zone. The
    -- names in it may be pointed at other hosts, mail included, and a panel
    -- that silently unpublished a customer's MX records because a vhost was
    -- removed would take their mail down with the site.
    website_id UUID REFERENCES websites (id) ON DELETE SET NULL,

    -- The apex: "example.com", or "113.0.203.in-addr.arpa" for a reverse zone.
    name VARCHAR(253) NOT NULL,

    -- 'master' or 'slave'. A slave's contents are transferred from elsewhere
    -- and are not this panel's to edit, which is why the record table below is
    -- empty for one.
    kind VARCHAR(10) NOT NULL DEFAULT 'master',

    -- The network a reverse zone covers, in CIDR form. NULL for a forward
    -- zone. It is kept because a PTR's owner name cannot be checked against
    -- the zone without it, and a PTR written into the wrong reverse zone is a
    -- record no resolver will ever ask this server for.
    reverse_network CIDR,

    -- The start of authority.
    --
    -- primary_ns is the server secondaries are told to ask; hostmaster is
    -- stored as an email address because that is what it means and what an
    -- operator can check. The dotted zone-file form is produced when the file
    -- is written, with the local part's dots escaped.
    primary_ns VARCHAR(253) NOT NULL,
    hostmaster VARCHAR(253) NOT NULL,

    -- The serial every secondary compares to decide whether to transfer.
    --
    -- It is bumped by the service on every change, to the greater of "one more
    -- than this" and the current unix time, which is monotonic and cannot run
    -- out within a day the way the YYYYMMDDnn convention does at the hundredth
    -- edit. It is BIGINT rather than INTEGER because the wire format's field is
    -- unsigned 32-bit and PostgreSQL's INTEGER is signed.
    serial BIGINT NOT NULL DEFAULT 1,

    -- The SOA timers, in seconds.
    refresh INTEGER NOT NULL DEFAULT 3600,
    retry   INTEGER NOT NULL DEFAULT 900,
    expire  INTEGER NOT NULL DEFAULT 1209600,
    minimum INTEGER NOT NULL DEFAULT 3600,
    -- The zone's default record lifetime.
    ttl INTEGER NOT NULL DEFAULT 3600,

    -- The NS records at the apex, as a list of names. A zone with none is a
    -- zone nothing can be delegated to.
    nameservers TEXT[] NOT NULL DEFAULT '{}',

    -- Signing. What is stored is the intent; the keys are named's, in a
    -- directory on the host, and are never copied here. A private key in a
    -- control panel's database is a private key in every backup of it.
    dnssec BOOLEAN NOT NULL DEFAULT FALSE,

    -- Replication. allow_transfer is who may pull the whole zone — empty means
    -- nobody, which is what BIND's default should always have been — and
    -- also_notify is the secondaries to tell about a change beyond those named
    -- by NS records, which is most of them.
    allow_transfer TEXT[] NOT NULL DEFAULT '{}',
    also_notify    TEXT[] NOT NULL DEFAULT '{}',
    -- Where a slave zone transfers from.
    masters TEXT[] NOT NULL DEFAULT '{}',

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT dns_zones_kind_valid CHECK (kind IN ('master', 'slave')),
    -- Mirrors validate.Zone: a zone name is a domain name, and a name carrying
    -- anything else is one that could not survive a zone file.
    CONSTRAINT dns_zones_name_format
        CHECK (name ~ '^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$'),
    CONSTRAINT dns_zones_timers_sane
        CHECK (
            refresh BETWEEN 300 AND 86400
            AND retry BETWEEN 60 AND 28800
            AND retry < refresh
            AND expire BETWEEN 604800 AND 31536000
            AND expire > refresh
            AND minimum BETWEEN 60 AND 86400
            AND ttl BETWEEN 60 AND 604800
        ),
    -- A secondary with no primary is a zone that can never load, and a
    -- primary's contents come from this panel rather than from a transfer.
    CONSTRAINT dns_zones_slave_has_masters
        CHECK (kind <> 'slave' OR cardinality(masters) > 0)
);

-- One zone per host. The name is what a query carries, so two zones of the
-- same name on one server is a configuration named refuses to load.
CREATE UNIQUE INDEX dns_zones_server_name_key ON dns_zones (server_id, name);
CREATE INDEX dns_zones_website_idx ON dns_zones (website_id);

-- ------------------------------------------------------------------ records

-- One resource record.
--
-- This is DATABASE.md section 20's dns_records with three additions, and the
-- reason is written there: an SRV record's weight, port and target in a single
-- text column can only be written to a zone file by parsing operator text at
-- the moment of writing, which is the one thing this panel avoids everywhere
-- else. So the numbers each get a column and are checked as numbers.
CREATE TABLE dns_records (
    id      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    zone_id UUID NOT NULL REFERENCES dns_zones (id) ON DELETE CASCADE,

    -- The owner name, relative to the zone, or '@' for the zone itself. Stored
    -- relative because that is what a zone file holds, and because an absolute
    -- name in the owner column is how a record ends up in a zone it was not
    -- meant for.
    name VARCHAR(253) NOT NULL DEFAULT '@',
    type VARCHAR(10) NOT NULL,
    -- Zero means the zone's default. A per-record TTL of zero is legal in DNS
    -- and is almost never what somebody filling in a form means.
    ttl INTEGER NOT NULL DEFAULT 0,

    -- The address, the target name, the text, or the CAA value.
    value TEXT NOT NULL,
    -- MX and SRV.
    priority INTEGER NOT NULL DEFAULT 0,
    -- SRV.
    weight INTEGER NOT NULL DEFAULT 0,
    port   INTEGER NOT NULL DEFAULT 0,
    -- CAA.
    flags INTEGER NOT NULL DEFAULT 0,
    tag   VARCHAR(20) NOT NULL DEFAULT '',

    -- Where this record came from. 'local' is the panel's own name server;
    -- another value names the remote provider a record was pushed to, and
    -- external_id is that provider's id for it, so a later sync can tell an
    -- update from a create.
    provider    VARCHAR(50) NOT NULL DEFAULT 'local',
    external_id VARCHAR(255) NOT NULL DEFAULT '',

    -- Records the panel writes for itself, which an operator may see and may
    -- not edit: the NS and A records a subdomain needs in its parent's zone.
    -- Deleting the subdomain removes them; editing one by hand would leave the
    -- panel and the zone disagreeing about a name the panel is responsible for.
    managed BOOLEAN NOT NULL DEFAULT FALSE,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT dns_records_type_valid
        CHECK (type IN ('A', 'AAAA', 'CNAME', 'MX', 'TXT', 'NS', 'CAA', 'SRV', 'PTR')),
    -- Relative, and not a name that would end early or carry syntax. The
    -- wildcard is allowed as the leftmost label only.
    CONSTRAINT dns_records_name_format
        CHECK (name = '@' OR name ~ '^(\*|_?[a-z0-9]([a-z0-9-]*[a-z0-9])?)(\._?[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$'),
    CONSTRAINT dns_records_ttl_sane
        CHECK (ttl = 0 OR ttl BETWEEN 60 AND 604800),
    CONSTRAINT dns_records_numbers_sane
        CHECK (
            priority BETWEEN 0 AND 65535
            AND weight BETWEEN 0 AND 65535
            AND port BETWEEN 0 AND 65535
            AND flags BETWEEN 0 AND 255
        ),
    CONSTRAINT dns_records_srv_has_port
        CHECK (type <> 'SRV' OR port > 0),
    CONSTRAINT dns_records_caa_tag
        CHECK (type <> 'CAA' OR tag IN ('issue', 'issuewild', 'iodef'))
);

CREATE INDEX dns_records_zone_idx ON dns_records (zone_id, name, type);

-- The same name, type and value twice is one record written twice: named loads
-- the duplicate and serves it once, so the second row is invisible except as a
-- row nobody can account for.
CREATE UNIQUE INDEX dns_records_unique
    ON dns_records (zone_id, name, type, value, priority, weight, port, tag);

-- ----------------------------------------------------------------- settings

-- The name server's own settings. One row per host.
CREATE TABLE dns_settings (
    server_id UUID PRIMARY KEY REFERENCES servers (id) ON DELETE CASCADE,

    -- The addresses named answers on. Empty means every address, which is what
    -- an authoritative server on a hosting box is for.
    listen_on TEXT[] NOT NULL DEFAULT '{}',
    -- The default for zones that name nobody of their own.
    allow_transfer TEXT[] NOT NULL DEFAULT '{}',

    -- The dnssec-policy named applies to signed zones. A policy name rather
    -- than a set of key knobs: inventing a key policy is how a zone becomes
    -- unresolvable for the length of its longest TTL, and BIND's own default
    -- is a single ECDSA key it rolls on its own schedule.
    dnssec_policy VARCHAR(64) NOT NULL DEFAULT 'default',

    -- The defaults a new zone is created with, so an operator sets their name
    -- servers once rather than on every zone.
    default_ns  TEXT[] NOT NULL DEFAULT '{}',
    default_ttl INTEGER NOT NULL DEFAULT 3600,
    hostmaster  VARCHAR(253) NOT NULL DEFAULT '',

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT dns_settings_policy_format
        CHECK (dnssec_policy ~ '^[A-Za-z0-9_-]{1,64}$'),
    CONSTRAINT dns_settings_ttl_sane
        CHECK (default_ttl BETWEEN 60 AND 604800)
);

-- ------------------------------------------------------- remote providers

-- Credentials for a DNS provider somewhere else, so the panel can publish the
-- same records there.
--
-- The token is encrypted with the panel's ENCRYPTION_KEY, the arrangement
-- DATABASE.md section 30 describes for exactly this: it has to be sent to the
-- provider on every call, so it cannot be hashed, and it is a credential that
-- can rewrite every DNS record in somebody's account.
CREATE TABLE dns_providers (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,

    -- 'cloudflare' today. It is constrained rather than free text: the value
    -- selects which client code runs.
    kind VARCHAR(30) NOT NULL,
    -- What an operator calls this account.
    label VARCHAR(100) NOT NULL DEFAULT '',

    api_token_encrypted TEXT NOT NULL,
    -- The account or zone identifier the provider gave, which is not secret.
    account_id VARCHAR(100) NOT NULL DEFAULT '',

    -- The last time a sync ran and what happened, so a provider that has been
    -- failing quietly for a month is visible.
    last_sync_at     TIMESTAMPTZ,
    last_sync_status VARCHAR(30) NOT NULL DEFAULT '',
    last_sync_error  TEXT NOT NULL DEFAULT '',

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT dns_providers_kind_valid CHECK (kind IN ('cloudflare'))
);

CREATE INDEX dns_providers_server_idx ON dns_providers (server_id);

-- Which zones are pushed to which remote provider.
CREATE TABLE dns_zone_providers (
    zone_id     UUID NOT NULL REFERENCES dns_zones (id) ON DELETE CASCADE,
    provider_id UUID NOT NULL REFERENCES dns_providers (id) ON DELETE CASCADE,
    -- The provider's own id for the zone, learned on the first sync.
    remote_zone_id VARCHAR(100) NOT NULL DEFAULT '',

    last_sync_at     TIMESTAMPTZ,
    last_sync_status VARCHAR(30) NOT NULL DEFAULT '',
    last_sync_error  TEXT NOT NULL DEFAULT '',

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (zone_id, provider_id)
);
