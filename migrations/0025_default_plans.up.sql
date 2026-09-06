-- Four plans to start from.
--
-- Phase 22 built the subscription machinery and shipped with an empty
-- catalogue, so the tenancy page opened on "No plans yet" and the first thing
-- anybody had to do was invent a set of limits from nothing. These are a
-- starting point, not a policy: they are ordinary shared-hosting tiers, and an
-- operator is expected to edit or delete them.
--
-- owner_user_id is NULL, which in this schema means the server owner's own
-- catalogue, offered to everybody rather than belonging to one reseller.
--
-- The NULL/0 distinction matters in every row below and is the reason these
-- are worth seeding carefully rather than filling in round numbers: NULL is
-- unlimited, 0 is none, and a plan that says 0 mailboxes is promising the
-- opposite of one that says NULL. Starter includes no mailboxes on purpose;
-- Unlimited leaves the countable dimensions NULL on purpose.
--
-- Inserted only when the table is empty. A panel that has been in use has a
-- catalogue somebody built, and a migration that added rows to it would be
-- editing their price list.
INSERT INTO service_plans (
    name, description, kind,
    disk_mb, bandwidth_mb,
    max_websites, max_databases, max_mailboxes, max_ftp_users,
    max_cron_jobs, max_subdomains,
    enforcement, cpu_percent, memory_mb
)
SELECT * FROM (VALUES
    -- A single site, no mail. The cheapest thing worth selling.
    ('Starter',
     'One website with a database. No mailboxes.',
     'plan',
     5120, 51200,
     1, 1, 0, 1,
     2, 3,
     'hard', 25, 512),

    -- The ordinary tier: a few sites, mail included.
    ('Personal',
     'A handful of sites with mail, for a person or a small project.',
     'plan',
     20480, 204800,
     5, 5, 10, 5,
     10, 25,
     'hard', 50, 1024),

    -- Room to grow, and soft limits: a business whose traffic spikes should be
    -- billed for the overage, not taken off the air by it.
    ('Business',
     'Room for a growing site. Disk and bandwidth overages are recorded rather than refused.',
     'plan',
     102400, 1048576,
     25, 25, 50, 25,
     50, 100,
     'soft', 100, 4096),

    -- No countable limits at all. NULL, not a large number: "unlimited" and
    -- "one million" are different promises and only one of them is honest.
    ('Unlimited',
     'No limits on what is counted. The disk is still the disk.',
     'plan',
     NULL, NULL,
     NULL, NULL, NULL, NULL,
     NULL, NULL,
     'soft', NULL, NULL)
) AS seed
WHERE NOT EXISTS (SELECT 1 FROM service_plans);
