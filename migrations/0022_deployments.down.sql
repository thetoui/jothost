-- Reverses 0022.
--
-- What is lost is the record: which commit each site is on, what every
-- deployment printed, and the webhook secrets. The secrets were encrypted and
-- nothing outside this table held them, so they cannot be recovered — a
-- repository configured again after this needs a new secret set on the forge.
--
-- What is *not* lost is anything irreplaceable. The source is in the
-- repository, which is somewhere else by definition; the deployed files are
-- still in each document root; and the deploy keys are on the host, where they
-- always were. A panel rolled back here forgets how each site was deployed
-- without changing what is being served.
--
-- Deployments and actions reference the repositories, so they go first.
DROP TABLE IF EXISTS deployments;
DROP TABLE IF EXISTS deployment_actions;
DROP TABLE IF EXISTS git_repositories;

-- The permissions go with them. role_permissions references them, so the
-- grants are removed first rather than left as rows pointing at nothing.
DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions WHERE name IN ('deploy.view', 'deploy.manage')
);
DELETE FROM permissions WHERE name IN ('deploy.view', 'deploy.manage');
