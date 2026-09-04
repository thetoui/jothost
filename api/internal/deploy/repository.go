package deploy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Errors returned by the repository.
var (
	// ErrNotFound covers a row that is not there.
	ErrNotFound = errors.New("not found")
	// ErrDuplicate covers a website that already has a repository.
	ErrDuplicate = errors.New("already exists")
	// ErrInProgress covers a second deployment while one is running.
	//
	// It is its own error because it is the one conflict here that is not a
	// mistake: somebody pressed the button twice, or a push arrived while a
	// deployment was already going. The right answer is to say so, not to
	// queue a second write into the same working tree.
	ErrInProgress = errors.New("a deployment is already running for this website")
)

// Repository reads and writes the deployment tables.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository builds a Repository.
func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// GitRepository is one website's source.
type GitRepository struct {
	ID        string `json:"id"`
	ServerID  string `json:"server_id"`
	WebsiteID string `json:"website_id"`

	RemoteURL string `json:"remote_url"`
	Branch    string `json:"branch"`

	DeployKeyPublic      string `json:"deploy_key_public,omitempty"`
	DeployKeyFingerprint string `json:"deploy_key_fingerprint,omitempty"`

	Provider string `json:"provider"`
	// WebhookToken is the address of the webhook, not a credential: it selects
	// which repository a push is about so the panel knows which secret to
	// verify with. It is returned because the operator has to paste the URL
	// into a forge.
	WebhookToken string `json:"webhook_token,omitempty"`
	AutoDeploy   bool   `json:"auto_deploy"`

	DeployScript         string `json:"deploy_script"`
	ScriptTimeoutSeconds int    `json:"script_timeout_seconds"`

	CurrentCommit  string     `json:"current_commit"`
	CurrentBranch  string     `json:"current_branch"`
	LastDeployedAt *time.Time `json:"last_deployed_at,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Action is one step of a deployment.
type Action struct {
	ID           string    `json:"id"`
	RepositoryID string    `json:"repository_id"`
	Kind         string    `json:"kind"`
	Position     int       `json:"position"`
	Enabled      bool      `json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
}

// Deployment is one attempt.
type Deployment struct {
	ID           string `json:"id"`
	RepositoryID string `json:"repository_id"`
	WebsiteID    string `json:"website_id"`

	Trigger       string `json:"trigger"`
	Branch        string `json:"branch"`
	CommitSHA     string `json:"commit_sha"`
	CommitMessage string `json:"commit_message"`
	CommitAuthor  string `json:"commit_author"`

	PreviousCommit string `json:"previous_commit"`

	Status   string `json:"status"`
	ExitCode *int   `json:"exit_code,omitempty"`

	// Log is omitted from the list view and filled in only for a caller with
	// deploy.manage — a build prints whatever the build printed, which
	// regularly includes a token in a URL.
	Log          string `json:"log,omitempty"`
	LogTruncated bool   `json:"log_truncated"`

	RolledBack    bool   `json:"rolled_back"`
	RollbackError string `json:"rollback_error,omitempty"`

	JobID string `json:"job_id,omitempty"`

	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	DurationMS *int       `json:"duration_ms,omitempty"`

	CreatedAt time.Time `json:"created_at"`
}

const repositoryColumns = `
	id, server_id, website_id, remote_url, branch,
	deploy_key_public, deploy_key_fingerprint,
	provider, COALESCE(webhook_token, ''), auto_deploy,
	deploy_script, script_timeout_seconds,
	current_commit, current_branch, last_deployed_at, created_at, updated_at`

// CreateRepository records a website's source.
func (r *Repository) CreateRepository(ctx context.Context, repo GitRepository,
	webhookToken, secret string,
) (GitRepository, error) {
	row := r.pool.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO git_repositories (
				server_id, website_id, remote_url, branch, provider,
				webhook_token, webhook_secret_encrypted, auto_deploy,
				deploy_script, script_timeout_seconds
			) VALUES (
				$1::uuid, $2::uuid, $3, $4, $5,
				NULLIF($6, ''), NULLIF($7, ''), $8, $9, $10
			)
			RETURNING `+repositoryColumns+`
		)
		SELECT * FROM inserted`,
		repo.ServerID, repo.WebsiteID, repo.RemoteURL, repo.Branch, repo.Provider,
		webhookToken, secret, repo.AutoDeploy, repo.DeployScript,
		repo.ScriptTimeoutSeconds)
	created, err := scanRepository(row)
	return created, mapWriteError(err, "repository")
}

// UpdateRepository changes a repository's settings.
func (r *Repository) UpdateRepository(ctx context.Context, id string, repo GitRepository,
	webhookToken, secret string,
) (GitRepository, error) {
	row := r.pool.QueryRow(ctx, `
		WITH updated AS (
			UPDATE git_repositories SET
				remote_url             = $2,
				branch                 = $3,
				provider               = $4,
				webhook_token          = COALESCE(NULLIF($5, ''), webhook_token),
				webhook_secret_encrypted = COALESCE(NULLIF($6, ''), webhook_secret_encrypted),
				auto_deploy            = $7,
				deploy_script          = $8,
				script_timeout_seconds = $9,
				updated_at             = now()
			WHERE id = $1::uuid
			RETURNING `+repositoryColumns+`
		)
		SELECT * FROM updated`,
		id, repo.RemoteURL, repo.Branch, repo.Provider, webhookToken, secret,
		repo.AutoDeploy, repo.DeployScript, repo.ScriptTimeoutSeconds)
	updated, err := scanRepository(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return GitRepository{}, ErrNotFound
	}
	return updated, mapWriteError(err, "repository")
}

// SaveDeployKey records the public half of a website's deploy key.
func (r *Repository) SaveDeployKey(ctx context.Context, id, public, fingerprint string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE git_repositories
		SET deploy_key_public = $2, deploy_key_fingerprint = $3, updated_at = now()
		WHERE id = $1::uuid`, id, public, fingerprint)
	if err != nil {
		return fmt.Errorf("record the deploy key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetRepository reads one repository.
func (r *Repository) GetRepository(ctx context.Context, id string) (GitRepository, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+repositoryColumns+` FROM git_repositories WHERE id = $1::uuid`, id)
	repo, err := scanRepository(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return GitRepository{}, ErrNotFound
	}
	return repo, err
}

// RepositoryForWebsite reads the repository a website deploys from.
func (r *Repository) RepositoryForWebsite(ctx context.Context, websiteID string) (GitRepository, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+repositoryColumns+` FROM git_repositories WHERE website_id = $1::uuid`,
		websiteID)
	repo, err := scanRepository(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return GitRepository{}, ErrNotFound
	}
	return repo, err
}

// ByWebhookToken finds the repository a push is about.
//
// The token is the address rather than the credential: what authenticates the
// request is the signature, checked against the secret this returns. A token
// that matches nothing is reported as not found, and the caller answers exactly
// as it answers a bad signature — because telling an unauthenticated caller
// which tokens exist is telling them what to attack.
func (r *Repository) ByWebhookToken(ctx context.Context, token string) (GitRepository, string, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+repositoryColumns+`, COALESCE(webhook_secret_encrypted, '')
		 FROM git_repositories WHERE webhook_token = $1`, token)

	var repo GitRepository
	var secret string
	err := row.Scan(&repo.ID, &repo.ServerID, &repo.WebsiteID, &repo.RemoteURL,
		&repo.Branch, &repo.DeployKeyPublic, &repo.DeployKeyFingerprint,
		&repo.Provider, &repo.WebhookToken, &repo.AutoDeploy, &repo.DeployScript,
		&repo.ScriptTimeoutSeconds, &repo.CurrentCommit, &repo.CurrentBranch,
		&repo.LastDeployedAt, &repo.CreatedAt, &repo.UpdatedAt, &secret)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return GitRepository{}, "", ErrNotFound
		}
		return GitRepository{}, "", fmt.Errorf("read the repository: %w", err)
	}
	return repo, secret, nil
}

// ListRepositories reads every repository on a host.
func (r *Repository) ListRepositories(ctx context.Context, serverID string) ([]GitRepository, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+repositoryColumns+` FROM git_repositories
		 WHERE server_id = $1::uuid ORDER BY created_at`, serverID)
	if err != nil {
		return nil, fmt.Errorf("list the repositories: %w", err)
	}
	defer rows.Close()

	repositories := []GitRepository{}
	for rows.Next() {
		repo, err := scanRepository(rows)
		if err != nil {
			return nil, err
		}
		repositories = append(repositories, repo)
	}
	return repositories, rows.Err()
}

// DeleteRepository removes a repository and its history.
func (r *Repository) DeleteRepository(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM git_repositories WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("delete the repository: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordDeployed updates what a website is running.
func (r *Repository) RecordDeployed(ctx context.Context, id, commit, branch string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE git_repositories
		SET current_commit = $2, current_branch = $3, last_deployed_at = now(),
		    updated_at = now()
		WHERE id = $1::uuid`, id, commit, branch)
	if err != nil {
		return fmt.Errorf("record what is deployed: %w", err)
	}
	return nil
}

func scanRepository(row pgx.Row) (GitRepository, error) {
	var repo GitRepository
	err := row.Scan(&repo.ID, &repo.ServerID, &repo.WebsiteID, &repo.RemoteURL,
		&repo.Branch, &repo.DeployKeyPublic, &repo.DeployKeyFingerprint,
		&repo.Provider, &repo.WebhookToken, &repo.AutoDeploy, &repo.DeployScript,
		&repo.ScriptTimeoutSeconds, &repo.CurrentCommit, &repo.CurrentBranch,
		&repo.LastDeployedAt, &repo.CreatedAt, &repo.UpdatedAt)
	if err != nil {
		return GitRepository{}, err
	}
	return repo, nil
}

// WebhookSecret reads a repository's secret, for verifying a signature.
func (r *Repository) WebhookSecret(ctx context.Context, id string) (string, error) {
	var secret string
	err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(webhook_secret_encrypted, '') FROM git_repositories WHERE id = $1::uuid`,
		id).Scan(&secret)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("read the webhook secret: %w", err)
	}
	return secret, nil
}

const actionColumns = `id, repository_id, kind, position, enabled, created_at`

// ReplaceActions writes a repository's steps, in one transaction.
//
// Replaced rather than merged, because the *order* is the thing being edited: a
// caller reordering three steps sends three rows, and applying that as three
// updates would pass through states where two steps share a position — which
// the unique index refuses, so a legitimate edit would fail depending on which
// row was written first.
func (r *Repository) ReplaceActions(ctx context.Context, repositoryID string,
	actions []Action,
) ([]Action, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("save the deployment steps: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`DELETE FROM deployment_actions WHERE repository_id = $1::uuid`, repositoryID); err != nil {
		return nil, fmt.Errorf("save the deployment steps: %w", err)
	}

	for index, action := range actions {
		if _, err := tx.Exec(ctx, `
			INSERT INTO deployment_actions (repository_id, kind, position, enabled)
			VALUES ($1::uuid, $2, $3, $4)`,
			repositoryID, action.Kind, index, action.Enabled); err != nil {
			return nil, mapWriteError(err, "deployment step")
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("save the deployment steps: %w", err)
	}
	return r.ListActions(ctx, repositoryID)
}

// ListActions reads a repository's steps in order.
func (r *Repository) ListActions(ctx context.Context, repositoryID string) ([]Action, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+actionColumns+` FROM deployment_actions
		 WHERE repository_id = $1::uuid ORDER BY position`, repositoryID)
	if err != nil {
		return nil, fmt.Errorf("list the deployment steps: %w", err)
	}
	defer rows.Close()

	actions := []Action{}
	for rows.Next() {
		var action Action
		if err := rows.Scan(&action.ID, &action.RepositoryID, &action.Kind,
			&action.Position, &action.Enabled, &action.CreatedAt); err != nil {
			return nil, err
		}
		actions = append(actions, action)
	}
	return actions, rows.Err()
}

const deploymentColumns = `
	id, repository_id, website_id, trigger, branch, commit_sha, commit_message,
	commit_author, previous_commit, status, exit_code, log, log_truncated,
	rolled_back, rollback_error, job_id, started_at, finished_at, duration_ms,
	created_at`

// StartDeployment records a deployment that is about to run.
//
// The unique partial index on (repository_id) where status is pending or
// running is what makes this refuse a second one, and that is deliberate: two
// deployments into one document root is a working tree being rewritten by one
// process while another builds from it, and the result is neither commit. A
// check written here in Go would be one two API processes could both pass.
func (r *Repository) StartDeployment(ctx context.Context, deployment Deployment) (Deployment, error) {
	row := r.pool.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO deployments (
				repository_id, website_id, trigger, branch, previous_commit,
				status, started_at
			) VALUES ($1::uuid, $2::uuid, $3, $4, $5, 'running', now())
			RETURNING `+deploymentColumns+`
		)
		SELECT * FROM inserted`,
		deployment.RepositoryID, deployment.WebsiteID, deployment.Trigger,
		deployment.Branch, deployment.PreviousCommit)

	created, err := scanDeployment(row)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return Deployment{}, ErrInProgress
	}
	return created, mapWriteError(err, "deployment")
}

// FinishDeployment records how a deployment ended.
func (r *Repository) FinishDeployment(ctx context.Context, id string, deployment Deployment) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE deployments SET
			status         = $2,
			commit_sha     = $3,
			commit_message = $4,
			commit_author  = $5,
			exit_code      = $6,
			log            = $7,
			log_truncated  = $8,
			rolled_back    = $9,
			rollback_error = $10,
			finished_at    = now(),
			duration_ms    = $11
		WHERE id = $1::uuid`,
		id, deployment.Status, deployment.CommitSHA, deployment.CommitMessage,
		deployment.CommitAuthor, deployment.ExitCode, deployment.Log,
		deployment.LogTruncated, deployment.RolledBack, deployment.RollbackError,
		deployment.DurationMS)
	if err != nil {
		return fmt.Errorf("record the deployment's outcome: %w", err)
	}
	return nil
}

// GetDeployment reads one deployment.
func (r *Repository) GetDeployment(ctx context.Context, id string) (Deployment, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+deploymentColumns+` FROM deployments WHERE id = $1::uuid`, id)
	deployment, err := scanDeployment(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Deployment{}, ErrNotFound
	}
	return deployment, err
}

// ListDeployments reads a repository's history, newest first.
func (r *Repository) ListDeployments(ctx context.Context, repositoryID string,
	limit int,
) ([]Deployment, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+deploymentColumns+` FROM deployments
		 WHERE repository_id = $1::uuid ORDER BY created_at DESC LIMIT $2`,
		repositoryID, limit)
	if err != nil {
		return nil, fmt.Errorf("list the deployments: %w", err)
	}
	defer rows.Close()

	deployments := []Deployment{}
	for rows.Next() {
		deployment, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		deployments = append(deployments, deployment)
	}
	return deployments, rows.Err()
}

// ReleaseStale marks deployments that were running when the panel stopped.
//
// Called at startup. A deployment left "running" by a restart would hold the
// unique index for ever, so no further deployment of that website could start —
// and the page would show one in progress that nothing is progressing. Being
// honest about it costs a row saying "failed" and unblocks the site.
func (r *Repository) ReleaseStale(ctx context.Context) (int, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE deployments
		SET status = 'failed',
		    finished_at = now(),
		    log = log || E'\n[the panel restarted while this deployment was running, so ' ||
		          'its outcome is not known. The working tree is in whatever state it ' ||
		          'reached.]\n'
		WHERE status IN ('pending', 'running')`)
	if err != nil {
		return 0, fmt.Errorf("release stale deployments: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func scanDeployment(row pgx.Row) (Deployment, error) {
	var deployment Deployment
	err := row.Scan(&deployment.ID, &deployment.RepositoryID, &deployment.WebsiteID,
		&deployment.Trigger, &deployment.Branch, &deployment.CommitSHA,
		&deployment.CommitMessage, &deployment.CommitAuthor, &deployment.PreviousCommit,
		&deployment.Status, &deployment.ExitCode, &deployment.Log,
		&deployment.LogTruncated, &deployment.RolledBack, &deployment.RollbackError,
		&deployment.JobID, &deployment.StartedAt, &deployment.FinishedAt,
		&deployment.DurationMS, &deployment.CreatedAt)
	if err != nil {
		return Deployment{}, err
	}
	return deployment, nil
}

// mapWriteError turns a constraint violation into an error a caller can act on.
func mapWriteError(err error, what string) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return fmt.Errorf("%w: this %s", ErrDuplicate, what)
		case "23503":
			return fmt.Errorf("%w: the %s it belongs to", ErrNotFound, what)
		case "23514":
			return fmt.Errorf("the %s is not valid (%s)", what, pgErr.ConstraintName)
		}
	}
	return err
}
