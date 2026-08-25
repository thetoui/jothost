-- Phase 1: make refresh-token reuse detectable.
--
-- Rotation replaces sessions.token_hash, which leaves a replayed older token
-- with no row to match and therefore no way to distinguish theft from a
-- simply-invalid token. This table remembers the hashes a session has already
-- retired, so presenting one is unambiguous evidence the token leaked.
--
-- Extends DATABASE.md section 6; see docs/PHASE1.md for the rationale.

CREATE TABLE session_token_history (
    -- SHA-256 of a retired refresh token. Primary key: one hash can only ever
    -- have belonged to one session.
    token_hash TEXT PRIMARY KEY,
    session_id UUID NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    rotated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX session_token_history_session_id_idx ON session_token_history (session_id);
CREATE INDEX session_token_history_rotated_at_idx ON session_token_history (rotated_at);
