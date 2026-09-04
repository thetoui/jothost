-- Phase 27: git deployment — repositories, the steps a deployment runs, and the
-- record of every one that ran.

-- ------------------------------------------------------------ repositories

-- The repository one website is deployed from.
CREATE TABLE git_repositories (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    -- ON DELETE CASCADE: the repository exists to put files into this website's
    -- document root, so without the website it has nowhere to deploy to. Unlike
    -- a mail domain, nothing irreplaceable is lost — the source is in the
    -- repository, which is somewhere else by definition.
    website_id UUID NOT NULL REFERENCES websites (id) ON DELETE CASCADE,

    -- Where the code comes from.
    --
    -- Only https:// and ssh forms reach this column; see validate.GitRemote for
    -- why that is an allowlist rather than a set of refusals. git's remote is
    -- not an address but a small language, and three of its dialects run
    -- programs — "ext::sh -c ..." is remote code execution spelled as a URL,
    -- and a remote beginning with a hyphen is an option rather than a remote.
    remote_url VARCHAR(512) NOT NULL,
    branch     VARCHAR(255) NOT NULL DEFAULT 'main',

    -- The deploy key's public half, and its fingerprint for display.
    --
    -- The private half is not here, for the reason Phase 26 gives about DKIM
    -- keys and one more besides: this key grants read access to a customer's
    -- source code, and this database is backed up, replicated, and read by
    -- every part of the API. It lives on the host that authenticates with it.
    deploy_key_public      TEXT NOT NULL DEFAULT '',
    deploy_key_fingerprint VARCHAR(120) NOT NULL DEFAULT '',

    -- How a push reaches this panel.
    --
    -- The token is the *address* of the webhook and is not a credential: it
    -- selects which repository is being talked about so the panel knows which
    -- secret to verify with. The secret is what authenticates, and it is
    -- encrypted (DATABASE.md section 30) because anybody holding it can make
    -- this panel deploy.
    provider VARCHAR(20) NOT NULL DEFAULT 'none',
    webhook_token VARCHAR(64),
    webhook_secret_encrypted TEXT,

    -- Whether a verified push actually deploys, as opposed to being recorded.
    --
    -- Off by default. Turning it on means a person with write access to the
    -- repository can run code on this host without touching the panel, which is
    -- the point of the feature and is worth deciding rather than inheriting.
    auto_deploy BOOLEAN NOT NULL DEFAULT FALSE,

    -- The script a deployment may run, and how long it may run for.
    --
    -- Plain text, and deliberately not encrypted: it is not a secret, it is
    -- shown on the page, and encrypting it would suggest a confidentiality the
    -- rest of its lifecycle does not have. A script that needs a secret should
    -- read it from the site's own environment rather than carry it here — and
    -- the panel says so.
    deploy_script          TEXT NOT NULL DEFAULT '',
    script_timeout_seconds INTEGER NOT NULL DEFAULT 600,

    -- What the working tree is on now, so a rollback has somewhere to go back
    -- to and the page can say what is deployed.
    current_commit  VARCHAR(40) NOT NULL DEFAULT '',
    current_branch  VARCHAR(255) NOT NULL DEFAULT '',
    last_deployed_at TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT git_repositories_provider_valid
        CHECK (provider IN ('none', 'github', 'gitlab', 'generic')),
    CONSTRAINT git_repositories_timeout_sane
        CHECK (script_timeout_seconds BETWEEN 30 AND 3600),
    -- A remote that begins with a hyphen is an option to git, not a repository.
    -- Mirrored here because this column becomes an argument to a program.
    CONSTRAINT git_repositories_remote_shape
        CHECK (remote_url ~ '^(https://|ssh://|[^-@[:space:]][^@[:space:]]*@)'),
    CONSTRAINT git_repositories_branch_shape
        CHECK (branch !~ '^-' AND branch !~ '[[:space:]:~^?*\[\\]'),
    -- A webhook provider needs both halves: a token nothing verifies is an
    -- unauthenticated deploy endpoint, and a secret nothing addresses is a
    -- secret no request can reach.
    CONSTRAINT git_repositories_webhook_complete
        CHECK (
            provider = 'none'
            OR (webhook_token IS NOT NULL AND webhook_secret_encrypted IS NOT NULL)
        )
);

-- One repository per website. Two would mean two things writing into one
-- document root, and the second deployment would silently undo the first.
CREATE UNIQUE INDEX git_repositories_website_idx ON git_repositories (website_id);
CREATE INDEX git_repositories_server_idx ON git_repositories (server_id);
-- The webhook token is how an unauthenticated request finds its repository, so
-- it has to be unique across the host and has to be indexed: that lookup
-- happens on every push, from the internet.
CREATE UNIQUE INDEX git_repositories_webhook_idx
    ON git_repositories (webhook_token) WHERE webhook_token IS NOT NULL;

-- ----------------------------------------------------------------- actions

-- The steps a deployment runs, in order.
CREATE TABLE deployment_actions (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    repository_id UUID NOT NULL REFERENCES git_repositories (id) ON DELETE CASCADE,

    -- Which step. A closed set, checked here and in Go: every one but 'script'
    -- becomes an argv the Agent builds, so nothing from a request is ever part
    -- of a command line.
    kind VARCHAR(40) NOT NULL,

    -- Where in the sequence. Explicit rather than implied by insertion order,
    -- because "npm ci" after "npm run build" is not a deployment, it is a
    -- deployment that fails.
    position INTEGER NOT NULL,

    -- A step can be turned off without being forgotten. An operator debugging
    -- a failing build wants to skip one, not delete it and retype it later.
    enabled BOOLEAN NOT NULL DEFAULT TRUE,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT deployment_actions_kind_valid
        CHECK (kind IN (
            'composer.install', 'npm.ci', 'npm.install', 'npm.build',
            'artisan.migrate', 'artisan.optimise', 'script'
        )),
    CONSTRAINT deployment_actions_position_sane
        CHECK (position >= 0 AND position < 100)
);

CREATE UNIQUE INDEX deployment_actions_order_idx
    ON deployment_actions (repository_id, position);

-- ------------------------------------------------------------- deployments

-- One deployment: what was attempted, what happened, and what it printed.
CREATE TABLE deployments (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    repository_id UUID NOT NULL REFERENCES git_repositories (id) ON DELETE CASCADE,
    website_id    UUID NOT NULL REFERENCES websites (id) ON DELETE CASCADE,

    -- How it started. Kept because the three are different events: a person
    -- decided, a push arrived, or something failed and this is the undo.
    trigger VARCHAR(20) NOT NULL,

    branch      VARCHAR(255) NOT NULL DEFAULT '',
    commit_sha  VARCHAR(40)  NOT NULL DEFAULT '',
    commit_message VARCHAR(500) NOT NULL DEFAULT '',
    commit_author  VARCHAR(255) NOT NULL DEFAULT '',

    -- What the site was on before this ran.
    --
    -- Recorded at the start rather than derived afterwards, because it is what
    -- a rollback goes back to — and by the time a deployment has failed, the
    -- working tree no longer knows.
    previous_commit VARCHAR(40) NOT NULL DEFAULT '',

    status    VARCHAR(20) NOT NULL DEFAULT 'pending',
    exit_code INTEGER,

    -- What it printed, capped. This is the most sensitive column in the phase:
    -- a build prints whatever the build prints, which regularly includes a
    -- token in a URL or an environment variable a script echoed. Reading it
    -- needs deploy.manage rather than website.view for that reason.
    log TEXT NOT NULL DEFAULT '',
    -- Whether the log was cut short, so a page can say so rather than showing a
    -- truncated failure that looks like a hang.
    log_truncated BOOLEAN NOT NULL DEFAULT FALSE,

    -- Whether the working tree was put back after a failure, and what happened
    -- when it was tried. A rollback that itself failed is the state an operator
    -- most needs to be told about: the site is now on neither commit.
    rolled_back    BOOLEAN NOT NULL DEFAULT FALSE,
    rollback_error TEXT NOT NULL DEFAULT '',

    -- The Agent job, so the panel can follow a deployment that is still
    -- running rather than waiting for it to finish before showing anything.
    job_id VARCHAR(64) NOT NULL DEFAULT '',

    started_at  TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    duration_ms INTEGER,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT deployments_status_valid
        CHECK (status IN ('pending', 'running', 'success', 'failed', 'cancelled')),
    CONSTRAINT deployments_trigger_valid
        CHECK (trigger IN ('manual', 'webhook', 'rollback')),
    -- A finished deployment says how it finished. A row that is neither
    -- running nor explained is one nobody can act on.
    CONSTRAINT deployments_finished_has_an_outcome
        CHECK (
            status IN ('pending', 'running')
            OR finished_at IS NOT NULL
        )
);

CREATE INDEX deployments_repository_idx
    ON deployments (repository_id, created_at DESC);
CREATE INDEX deployments_website_idx ON deployments (website_id, created_at DESC);
-- One deployment at a time per repository, enforced rather than checked.
--
-- Two deployments running into one document root is a working tree being
-- rewritten by one process while another builds from it, and the result is not
-- either commit. A check written in Go would be one that two API processes
-- could both pass.
CREATE UNIQUE INDEX deployments_one_running_idx
    ON deployments (repository_id) WHERE status IN ('pending', 'running');

-- ------------------------------------------------------------- permissions

-- Two permissions, and the split is not the same one Phase 26 made.
--
-- Deploying is running code on the host as the website's account, so
-- deploy.manage is the powerful half. The read half is separated for a
-- narrower reason: a build log prints whatever the build printed, which
-- regularly includes a token in a URL or an environment variable a script
-- echoed. Seeing *that* a deployment failed is support work; reading what it
-- said is not.
INSERT INTO permissions (name, description) VALUES
    ('deploy.view',   'See deployment history and status'),
    ('deploy.manage', 'Configure repositories, deploy, and read deployment logs')
ON CONFLICT (name) DO NOTHING;

-- admin has both.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.name IN ('deploy.view', 'deploy.manage')
WHERE r.name = 'admin'
ON CONFLICT DO NOTHING;

-- operator has both: deploying a customer's site is day-to-day hosting work,
-- and the operator role already has cron.manage — which is the ability to run
-- any command as the same account on a schedule.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.name IN ('deploy.view', 'deploy.manage')
WHERE r.name = 'operator'
ON CONFLICT DO NOTHING;

-- viewer gets the read half only.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.name = 'deploy.view'
WHERE r.name = 'viewer'
ON CONFLICT DO NOTHING;
