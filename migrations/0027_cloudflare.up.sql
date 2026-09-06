-- Cloudflare synchronisation, per zone.
--
-- The panel runs its own authoritative name server (Phase 13). This is for the
-- zones somebody keeps at Cloudflare instead, so the records they edit here are
-- the records the world resolves.
--
-- Push-only, deliberately. The panel's zone is the source of truth and a sync
-- makes Cloudflare match it; importing an existing Cloudflare zone is a
-- separate, explicit action. Two-way sync means answering "which side wins"
-- when a record differs, and there is no answer to that a panel can pick on
-- somebody's behalf without occasionally being catastrophically wrong.
CREATE TABLE dns_cloudflare (
    -- One row per zone, so the zone's own id is the key: a zone is synced to
    -- one Cloudflare zone or to none.
    zone_id UUID PRIMARY KEY REFERENCES dns_zones (id) ON DELETE CASCADE,

    -- Cloudflare's own identifier for the zone. Not derived from the name:
    -- an account can hold the same name twice across different accounts, and
    -- the id is what the API addresses.
    cloudflare_zone_id VARCHAR(64) NOT NULL,

    -- The API token, encrypted with the panel's key and bound to this row.
    --
    -- A token, not a Global API Key: a token can be scoped to DNS edit on one
    -- zone, and the key is the whole account. The panel cannot enforce which
    -- one somebody pastes in, but it can ask for the right thing and say why.
    api_token_encrypted TEXT NOT NULL,

    -- Off until somebody turns it on. A zone that is configured and not
    -- enabled is one whose credentials were entered and whose owner has not
    -- yet decided to let the panel write to it.
    enabled BOOLEAN NOT NULL DEFAULT false,

    -- What happened last time, for a page that has to answer "is this working"
    -- without making the operator run a sync to find out.
    last_sync_at      TIMESTAMPTZ,
    last_sync_ok      BOOLEAN,
    last_sync_detail  TEXT NOT NULL DEFAULT '',
    -- How many records the last push created, changed and removed. Kept
    -- separately from the detail text because "it worked" and "it worked and
    -- deleted forty records" are answers an operator should be able to tell
    -- apart at a glance.
    last_created INTEGER NOT NULL DEFAULT 0,
    last_updated INTEGER NOT NULL DEFAULT 0,
    last_deleted INTEGER NOT NULL DEFAULT 0,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT dns_cloudflare_zone_id_present CHECK (btrim(cloudflare_zone_id) <> ''),
    CONSTRAINT dns_cloudflare_token_present CHECK (btrim(api_token_encrypted) <> '')
);

-- The page lists zones and shows which are synced, so the lookup is by zone.
-- The primary key already serves that; this index serves the other question,
-- which is "show me everything that is syncing".
CREATE INDEX dns_cloudflare_enabled_idx ON dns_cloudflare (enabled) WHERE enabled;
