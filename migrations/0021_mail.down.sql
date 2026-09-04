-- Reverses 0021.
--
-- What is lost is every mailbox's password hash, every forwarder, and the
-- record of which domains this host accepts mail for. The messages themselves
-- are not here — they are Maildirs on the host — so rolling back does not
-- destroy anybody's mail. It destroys the panel's ability to serve it: the
-- mailboxes stop existing as far as Postfix and Dovecot are concerned at the
-- next reconcile, and mail for the domain is refused rather than filed.
--
-- The DKIM private keys are not here either, for the reason the up migration
-- gives, so they survive. What goes is the public half the panel publishes and
-- compares against DNS — after this, the panel can no longer tell whether the
-- record the world is reading is the one this host signs with.
--
-- Autoresponders and aliases reference the tables above them, so they go first.
DROP TABLE IF EXISTS mail_autoresponders;
DROP TABLE IF EXISTS mail_aliases;
DROP TABLE IF EXISTS mailboxes;
DROP TABLE IF EXISTS mail_domains;
DROP TABLE IF EXISTS mail_settings;

-- The permissions go with them. role_permissions references them, so the grants
-- are removed first rather than left as rows pointing at nothing.
DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions WHERE name IN ('mail.view', 'mail.manage')
);
DELETE FROM permissions WHERE name IN ('mail.view', 'mail.manage');
