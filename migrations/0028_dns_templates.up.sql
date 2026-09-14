-- DNS templates: the records a new zone starts with.
--
-- Until now a zone was seeded with two records hard-coded in Go — an A for the
-- apex and one for www, both pointing at the host. Anybody who wanted a zone
-- to start with an MX, a SPF record or a CAA had to add them by hand to every
-- domain they created, and anybody who did not want www had to delete it.
--
-- A template is that list, made editable. The built-in one below reproduces
-- exactly what the hard-coded seed did, so a host that upgrades and never
-- opens the page keeps the behaviour it had.

CREATE TABLE dns_templates (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,

    name        VARCHAR(80) NOT NULL,
    description TEXT NOT NULL DEFAULT '',

    -- The template applied to a new zone when none is named. Exactly one per
    -- server may hold it; the partial unique index below is what enforces
    -- that, rather than a trigger or a promise in the application.
    is_default BOOLEAN NOT NULL DEFAULT FALSE,

    -- Built-in templates are the panel's own. They can be edited — an operator
    -- who wants a different default should not have to make a second one — but
    -- not deleted, because deleting the only template leaves new zones with
    -- nothing at all.
    builtin BOOLEAN NOT NULL DEFAULT FALSE,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT dns_templates_name_not_blank CHECK (btrim(name) <> '')
);

CREATE UNIQUE INDEX dns_templates_name_idx
    ON dns_templates (server_id, lower(name));

-- One default per server. A partial unique index says so in the schema, where
-- two requests arriving at once cannot both believe they are the only one.
CREATE UNIQUE INDEX dns_templates_default_idx
    ON dns_templates (server_id)
    WHERE is_default;

CREATE TABLE dns_template_records (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    template_id UUID NOT NULL REFERENCES dns_templates (id) ON DELETE CASCADE,

    -- The same shape as dns_records, because a template record becomes one.
    -- Two tables that had to agree and did not would be the whole bug.
    --
    -- The difference is that these may carry placeholders: {domain} for the
    -- zone being created and {ip} for this host's own address. They are
    -- substituted when a zone is made, and a template is validated at write
    -- time by rendering it against a sample — so a template that cannot
    -- produce a valid record is refused when it is written rather than when
    -- somebody creates a domain.
    name     VARCHAR(253) NOT NULL DEFAULT '@',
    type     VARCHAR(10)  NOT NULL,
    ttl      INTEGER      NOT NULL DEFAULT 0,
    value    TEXT         NOT NULL,
    priority INTEGER      NOT NULL DEFAULT 0,
    weight   INTEGER      NOT NULL DEFAULT 0,
    port     INTEGER      NOT NULL DEFAULT 0,

    -- The order they are written in. Not cosmetic: a zone file reads better
    -- with the apex first, and an operator who arranged them expects that
    -- arrangement back.
    position INTEGER NOT NULL DEFAULT 0,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX dns_template_records_template_idx
    ON dns_template_records (template_id, position, created_at);

-- The built-in default, per server, reproducing the hard-coded seed exactly.
--
-- Inserted for every server that exists, and only where the server has no
-- template already, so this is safe to run on a host that has been upgraded
-- before.
INSERT INTO dns_templates (server_id, name, description, is_default, builtin)
SELECT s.id,
       'Default',
       'The records a new domain starts with: the apex and www, both pointing at this host.',
       TRUE,
       TRUE
FROM servers s
WHERE NOT EXISTS (SELECT 1 FROM dns_templates t WHERE t.server_id = s.id);

INSERT INTO dns_template_records (template_id, name, type, value, position)
SELECT t.id, r.name, 'A', '{ip}', r.position
FROM dns_templates t
CROSS JOIN (VALUES ('@', 0), ('www', 1)) AS r (name, position)
WHERE t.builtin
  AND NOT EXISTS (
      SELECT 1 FROM dns_template_records existing WHERE existing.template_id = t.id
  );
