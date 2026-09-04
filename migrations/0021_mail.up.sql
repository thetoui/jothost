-- Phase 26: the mail server — domains, mailboxes, forwarders and the policy
-- records that decide whether any of it is believed.

-- ---------------------------------------------------------------- settings

-- The mail server's own settings, one row per host.
CREATE TABLE mail_settings (
    server_id UUID PRIMARY KEY REFERENCES servers (id) ON DELETE CASCADE,

    -- Whether the panel manages mail on this host at all.
    --
    -- Off by default, and that default is the important one. A mail server is
    -- not a thing to switch on by accident: it listens on port 25, it will be
    -- found within hours of appearing, and everything that follows in this
    -- table is about what happens when it is.
    enabled BOOLEAN NOT NULL DEFAULT FALSE,

    -- The name the server calls itself in EHLO and in every Received header.
    --
    -- Not the domain mail is for — this is the machine. It has to be a real
    -- fully qualified name that resolves, and ideally the one the connecting
    -- address reverse-resolves to, because that pair is the first thing a
    -- receiving server checks. Getting it wrong is the most common reason a
    -- correctly configured mail server's mail is refused.
    hostname VARCHAR(255) NOT NULL DEFAULT '',

    -- The website whose certificate the mail server presents.
    --
    -- The certificate is not copied here for the reason the FTP settings give:
    -- Phase 6 owns it and renews it, and a second copy is one that silently
    -- goes stale at the first renewal. ON DELETE SET NULL turns TLS off
    -- explicitly rather than leaving a path to a certificate that is gone.
    tls_website_id UUID REFERENCES websites (id) ON DELETE SET NULL,

    -- Refuse to accept a password over an unencrypted connection.
    --
    -- Default TRUE, which is the opposite of the FTP setting's default, and the
    -- difference is deliberate. An FTP server with require_tls on rejects every
    -- client still configured for plain FTP at once. A mail server does not
    -- have that problem: submission has required STARTTLS everywhere for a
    -- decade, every mail client made this century does it, and the thing being
    -- protected is a password that also unlocks the customer's webmail.
    --
    -- It does not disable the port. A client connecting to 587 in the clear is
    -- offered STARTTLS and refused only if it declines and then tries to
    -- authenticate; unauthenticated delivery from other mail servers on port 25
    -- is unaffected, because that is how mail works.
    require_tls BOOLEAN NOT NULL DEFAULT TRUE,

    -- Spam and virus filtering, and the score above which a message is refused
    -- outright rather than filed.
    --
    -- Rejected rather than delivered to a spam folder, above the threshold,
    -- and that is worth stating: a rejection at SMTP time is a bounce the
    -- *sender* sees. A false positive that is rejected gets a phone call; a
    -- false positive filed in a spam folder nobody opens is silence, and the
    -- customer finds out a week later that they lost an order.
    spam_enabled       BOOLEAN NOT NULL DEFAULT TRUE,
    spam_reject_score  INTEGER NOT NULL DEFAULT 15,
    virus_enabled      BOOLEAN NOT NULL DEFAULT FALSE,

    -- The largest message the server will accept, in megabytes.
    max_message_mb INTEGER NOT NULL DEFAULT 25,

    -- The website serving webmail, if one has been installed.
    --
    -- ON DELETE SET NULL: deleting the site removes the webmail, and a row
    -- pointing at a site that is gone would have the panel offering a link to
    -- a 404.
    webmail_website_id UUID REFERENCES websites (id) ON DELETE SET NULL,
    -- Which webmail was installed and at what version, so the panel can say
    -- what is running rather than what it once installed.
    webmail_version VARCHAR(50) NOT NULL DEFAULT '',

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT mail_settings_spam_score_sane
        CHECK (spam_reject_score BETWEEN 5 AND 30),
    CONSTRAINT mail_settings_message_size_sane
        CHECK (max_message_mb BETWEEN 1 AND 512),
    -- Mail cannot be switched on without a name to call the server. A host
    -- greeting the world as "localhost" has its mail refused by most of it.
    CONSTRAINT mail_settings_enabled_needs_hostname
        CHECK (NOT enabled OR length(btrim(hostname)) > 0)
);

-- ----------------------------------------------------------------- domains

-- A domain this host accepts mail for.
CREATE TABLE mail_domains (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,

    -- The website this domain belongs to, where there is one.
    --
    -- Nullable, and the two cases are both real: a customer's site with mail on
    -- the same name, and a domain that exists only for mail. ON DELETE SET NULL
    -- rather than CASCADE, and this is the one place in the panel where that
    -- choice is not obvious — deleting a website must not silently delete
    -- everybody's mailboxes and every message in them. The panel makes the
    -- operator delete the mail domain deliberately, and says why.
    website_id UUID REFERENCES websites (id) ON DELETE SET NULL,

    domain VARCHAR(255) NOT NULL,

    -- A domain can be recorded and not accepting mail. Turning it off leaves
    -- the mailboxes and the messages alone and stops the server admitting new
    -- mail for the name, which is what an operator wants during a migration.
    active BOOLEAN NOT NULL DEFAULT TRUE,

    -- Where mail addressed to a mailbox that does not exist goes.
    --
    -- Empty means it is refused, which is the default and the right one. A
    -- catch-all looks helpful and is a spam magnet: every dictionary attack
    -- against the domain lands in it, and because the server can no longer say
    -- "no such user" it has already accepted the message by the time it finds
    -- out — so a bounce it then generates is backscatter sent to a forged
    -- sender, which is how a host gets blacklisted.
    catch_all VARCHAR(320) NOT NULL DEFAULT '',

    -- DKIM.
    --
    -- The public key is here; the private key is not, and that is a decision
    -- rather than an oversight. See docs/PHASE26.md — a signing key in the
    -- control-plane database is a key that leaves with a database dump, and
    -- this is a database that is backed up and replicated. The key lives on the
    -- host that signs with it. What is stored here is what the world can read
    -- anyway: the public half, which the panel publishes and compares against
    -- what DNS is actually serving.
    dkim_selector   VARCHAR(63)  NOT NULL DEFAULT '',
    dkim_public_key TEXT         NOT NULL DEFAULT '',
    dkim_created_at TIMESTAMPTZ,

    -- The policies the panel publishes for this domain.
    --
    -- Both default to the safe end rather than the strict end. An SPF record
    -- ending in "-all" published before the owner knows what else sends as
    -- them is how a company's invoicing system stops being delivered, and the
    -- panel would have caused it without anybody changing anything visible.
    spf_policy   VARCHAR(20)  NOT NULL DEFAULT 'soft',
    dmarc_policy VARCHAR(20)  NOT NULL DEFAULT 'none',
    -- Where DMARC reports are sent. Empty means the record asks for none,
    -- which is a legitimate choice and a wasted one: the reports are how a
    -- domain owner discovers what else sends as them.
    dmarc_rua    VARCHAR(320) NOT NULL DEFAULT '',

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Mirrors the rules enforced in Go. A colon or a space would end a field
    -- early in Postfix's lookup tables; a wildcard has no zone to publish DKIM
    -- into.
    CONSTRAINT mail_domains_name_format
        CHECK (domain ~ '^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$'),
    CONSTRAINT mail_domains_spf_valid
        CHECK (spf_policy IN ('none', 'soft', 'strict')),
    CONSTRAINT mail_domains_dmarc_valid
        CHECK (dmarc_policy IN ('off', 'none', 'quarantine', 'reject')),
    CONSTRAINT mail_domains_selector_format
        CHECK (dkim_selector = '' OR dkim_selector ~ '^[a-z0-9]([a-z0-9-]*[a-z0-9])?$'),
    -- A public key with no selector is a key that cannot be published, and a
    -- selector with no key is a record that would be published empty. Neither
    -- half is useful alone.
    CONSTRAINT mail_domains_dkim_complete
        CHECK ((dkim_selector = '') = (dkim_public_key = ''))
);

-- One domain per host. Two rows for the same name would produce two entries in
-- Postfix's domain table, and the second would decide which mailboxes exist.
CREATE UNIQUE INDEX mail_domains_name_idx ON mail_domains (server_id, domain);
CREATE INDEX mail_domains_website_idx ON mail_domains (website_id);

-- --------------------------------------------------------------- mailboxes

-- One mailbox: an address that receives, and a login.
CREATE TABLE mailboxes (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    domain_id UUID NOT NULL REFERENCES mail_domains (id) ON DELETE CASCADE,

    local_part VARCHAR(64) NOT NULL,

    -- The password hash, in the form Dovecot reads: "{SHA512-CRYPT}$6$...".
    --
    -- The hash and not the password, and the difference is the whole point:
    -- what is stored cannot be replayed against IMAP, against webmail, or
    -- against the customer's other accounts. "Show me the password" is answered
    -- by setting a new one.
    --
    -- This diverges from ftp_users, which stores nothing at all, and the reason
    -- is the reconcile model. The panel rebuilds Dovecot's passwd-file from
    -- this table; without the hash it could only *preserve* whatever the host
    -- already had, so a reinstalled host would come back with every mailbox
    -- present and no login working — and the panel would have no way to know.
    -- A hash is not a credential. Storing one is what makes the host
    -- reconstructible from the panel's own record.
    password_hash TEXT NOT NULL,

    -- Size limit in megabytes. 0 means unlimited.
    --
    -- The default is a real number rather than 0, unlike the FTP quota, because
    -- of how the two fail. An unlimited FTP account fills a disk with files
    -- somebody uploaded on purpose. An unlimited mailbox fills it with mail
    -- somebody else sent — and the first thing that stops working when the disk
    -- is full is every other service on the host.
    quota_mb INTEGER NOT NULL DEFAULT 2048,

    -- A suspended mailbox keeps its mail and cannot log in or receive. Same
    -- reasoning as the FTP account: an operator who suspects a credential is
    -- loose can act now and decide later.
    active BOOLEAN NOT NULL DEFAULT TRUE,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT mailboxes_local_part_format
        CHECK (local_part ~ '^[a-z0-9]([a-z0-9._-]*[a-z0-9])?$' AND local_part !~ '\.\.'),
    CONSTRAINT mailboxes_quota_sane
        CHECK (quota_mb >= 0 AND quota_mb <= 1048576),
    -- A row whose password field holds something that is not a hash is a row
    -- where a plaintext password reached the wrong function. The shape is
    -- checked here as well as in Go because this is the last place it can be
    -- caught before it is written into a file the mail server authenticates
    -- against.
    CONSTRAINT mailboxes_password_is_hashed
        CHECK (password_hash LIKE '{SHA512-CRYPT}$6$%')
);

CREATE UNIQUE INDEX mailboxes_address_idx ON mailboxes (domain_id, local_part);

-- ----------------------------------------------------------------- aliases

-- A forwarder: mail to one address is delivered to another.
CREATE TABLE mail_aliases (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    domain_id UUID NOT NULL REFERENCES mail_domains (id) ON DELETE CASCADE,

    -- The local part being forwarded. It may be the same as a mailbox's, and
    -- that combination is useful rather than a conflict: mail is delivered to
    -- the mailbox *and* copied onward, which is how "keep a copy" works.
    source VARCHAR(64) NOT NULL,

    -- Where it goes. A full address, anywhere — including outside this host,
    -- which is the common case.
    destination VARCHAR(320) NOT NULL,

    active BOOLEAN NOT NULL DEFAULT TRUE,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT mail_aliases_source_format
        CHECK (source ~ '^[a-z0-9]([a-z0-9._-]*[a-z0-9])?$' AND source !~ '\.\.'),
    CONSTRAINT mail_aliases_destination_format
        CHECK (destination ~ '^[^[:space:]@]+@[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$')
);

-- One destination per source, once. The same source may forward to several
-- addresses — that is a distribution list — but recording the same pair twice
-- would deliver two copies.
CREATE UNIQUE INDEX mail_aliases_pair_idx
    ON mail_aliases (domain_id, source, destination);
CREATE INDEX mail_aliases_domain_idx ON mail_aliases (domain_id);

-- ---------------------------------------------------------- autoresponders

-- A vacation reply. One per mailbox, which is why the mailbox is the key.
CREATE TABLE mail_autoresponders (
    mailbox_id UUID PRIMARY KEY REFERENCES mailboxes (id) ON DELETE CASCADE,

    subject VARCHAR(200) NOT NULL,
    body    TEXT         NOT NULL,

    -- When it applies. Both nullable: an autoresponder with no dates is on
    -- until it is turned off, which is what somebody who has left the company
    -- needs.
    starts_at TIMESTAMPTZ,
    ends_at   TIMESTAMPTZ,

    -- How long before the same sender gets another copy.
    --
    -- Never zero. A reply to every message includes a reply to somebody else's
    -- vacation reply, which is a loop that ends when one of the two mailboxes
    -- is full.
    interval_days INTEGER NOT NULL DEFAULT 7,

    active BOOLEAN NOT NULL DEFAULT TRUE,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT mail_autoresponders_interval_sane
        CHECK (interval_days BETWEEN 1 AND 30),
    CONSTRAINT mail_autoresponders_dates_ordered
        CHECK (starts_at IS NULL OR ends_at IS NULL OR ends_at > starts_at),
    CONSTRAINT mail_autoresponders_has_a_message
        CHECK (length(btrim(body)) > 0)
);

-- ------------------------------------------------------------- permissions

-- Two permissions rather than one.
--
-- Reading is separated from changing because a mailbox is the one thing in this
-- panel that holds a customer's correspondence. Seeing that "sales@example.com
-- exists and is 40% full" is support work; being able to set its password is
-- being able to read every message in it, silently, without the owner ever
-- seeing a trace. Those are not the same act and they are not the same
-- permission.
INSERT INTO permissions (name, description) VALUES
    ('mail.view',   'See mail domains, mailboxes, and forwarders'),
    ('mail.manage', 'Create and change mail domains, mailboxes, and forwarders')
ON CONFLICT (name) DO NOTHING;

-- admin has both.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.name IN ('mail.view', 'mail.manage')
WHERE r.name = 'admin'
ON CONFLICT DO NOTHING;

-- operator has both as well: creating a mailbox is day-to-day hosting work,
-- alongside the FTP accounts and cron jobs the operator role already manages.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.name IN ('mail.view', 'mail.manage')
WHERE r.name = 'operator'
ON CONFLICT DO NOTHING;

-- viewer gets the read half only, which is what the split is for.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.name = 'mail.view'
WHERE r.name = 'viewer'
ON CONFLICT DO NOTHING;
