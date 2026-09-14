-- One-time recovery codes for two-factor authentication.
--
-- Without these, an account whose authenticator is lost can only be recovered
-- by another administrator, and a panel with one administrator not at all.
--
-- Only a hash of each code is kept. The codes are long enough (about 79 bits)
-- that the hash cannot be reversed by trying every code, so no key is needed,
-- and each hash is bound to its user so identical codes on two accounts do not
-- share a row's worth of evidence.
--
-- The foreign key is to the enrolment, not the user: turning two-factor off
-- deletes the enrolment, and with it every code that could have got past it.

CREATE TABLE two_factor_recovery_codes (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES two_factor_auth (user_id) ON DELETE CASCADE,
    code_hash  TEXT NOT NULL,
    used_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT two_factor_recovery_codes_unique UNIQUE (user_id, code_hash)
);

CREATE INDEX two_factor_recovery_codes_unused
    ON two_factor_recovery_codes (user_id) WHERE used_at IS NULL;
