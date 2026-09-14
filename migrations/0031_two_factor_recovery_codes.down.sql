-- Removes recovery codes. Accounts keep their authenticator; only the way
-- around a lost one goes.
DROP TABLE IF EXISTS two_factor_recovery_codes;
