-- Phase 6: SSL certificates.
-- Implements DATABASE.md table 19.

CREATE TABLE ssl_certificates (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- A certificate belongs to one website and goes with it: leaving the row
    -- behind would have the panel offering HTTPS for a site that is gone.
    website_id UUID NOT NULL REFERENCES websites (id) ON DELETE CASCADE,
    provider   VARCHAR(50) NOT NULL,
    -- Every name the certificate is valid for, primary first. Held as JSON
    -- because a certificate covers a set, and the set is read as a whole.
    domains    JSONB NOT NULL,

    certificate_path TEXT,
    private_key_path TEXT,
    -- The certificate's own fingerprint, so a renewal that produced the same
    -- file can be told from one that actually replaced it.
    fingerprint VARCHAR(95),
    issuer      VARCHAR(255),

    issued_at  TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    auto_renew BOOLEAN NOT NULL DEFAULT TRUE,
    status     VARCHAR(30) NOT NULL DEFAULT 'pending',
    -- Why the last attempt failed, shown to the user. Never holds key material.
    last_error TEXT,
    -- When renewal was last attempted, so a failing certificate is retried on a
    -- cadence rather than on every sweep.
    last_renewal_attempt TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT ssl_certificates_provider_valid
        CHECK (provider IN ('letsencrypt', 'selfsigned')),
    -- A certificate is never quietly "fine": it is being issued, valid,
    -- expiring, expired, revoked, or it failed. Conflating any of these with
    -- valid is how a panel reports HTTPS working the day after it stopped.
    CONSTRAINT ssl_certificates_status_valid
        CHECK (status IN ('pending', 'issuing', 'valid', 'expiring', 'expired',
                          'revoked', 'failed')),
    CONSTRAINT ssl_certificates_paths_absolute
        CHECK (
            (certificate_path IS NULL OR
             (certificate_path LIKE '/%' AND certificate_path NOT LIKE '%..%'))
            AND
            (private_key_path IS NULL OR
             (private_key_path LIKE '/%' AND private_key_path NOT LIKE '%..%'))
        ),
    -- A usable certificate must say where both halves are; one without its key
    -- cannot serve a single request.
    CONSTRAINT ssl_certificates_valid_has_files
        CHECK (status <> 'valid' OR
               (certificate_path IS NOT NULL AND private_key_path IS NOT NULL))
);

-- One certificate per website. A second would make the vhost's ssl_certificate
-- directive ambiguous, and nginx would silently use whichever was written last.
CREATE UNIQUE INDEX ssl_certificates_website_idx ON ssl_certificates (website_id);

-- The renewal sweep asks "what expires soon", so that ordering is indexed
-- (DATABASE.md section 29).
CREATE INDEX ssl_certificates_expires_at_idx ON ssl_certificates (expires_at);
CREATE INDEX ssl_certificates_renewable_idx
    ON ssl_certificates (expires_at) WHERE auto_renew = TRUE;
CREATE INDEX ssl_certificates_status_idx ON ssl_certificates (status);
