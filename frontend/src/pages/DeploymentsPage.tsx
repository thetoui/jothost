import { useState } from 'react';
import {
  AlertTriangle,
  CheckCircle2,
  GitBranch,
  KeyRound,
  Play,
  Plus,
  RotateCcw,
  Trash2,
  XCircle,
} from 'lucide-react';

import { Alert as AlertBanner } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { SelectField, TextField, Toggle } from '@/components/ui/Field';
import { EmptyState, SkeletonRows } from '@/components/ui/Loading';
import { Modal } from '@/components/ui/Modal';
import { TextButton } from '@/components/ui/TextButton';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import {
  useConfigureRepository,
  useDeploy,
  useDeploymentLog,
  useDeployments,
  useGenerateDeployKey,
  useRemoveRepository,
  useRollback,
} from '@/features/deployments/hooks';
import { useWebsites } from '@/features/websites/hooks';
import { ApiError } from '@/services/apiClient';
import type { DeployProvider, Deployment, GitRepository } from '@/types/api';

/**
 * DeploymentsPage shows where each website's code comes from and what happened
 * the last few times it was deployed.
 *
 * A deployment runs code on this host as the website's own account, so the page
 * is built to make that visible rather than incidental: what is checked out
 * right now, whether the working tree has changes a deployment would destroy,
 * whether a push can start one without anybody touching the panel, and what the
 * last build printed.
 */
export function DeploymentsPage() {
  const { data, isPending, isError, error } = useDeployments();
  const [adding, setAdding] = useState(false);

  const repositories = data?.repositories ?? [];

  return (
    <div className="space-y-5">
      <header>
        <h1 className="text-xl font-semibold text-slate-900">Deployments</h1>
        <p className="mt-1 text-sm text-slate-500">
          Where each website&rsquo;s code comes from, and what happened last time it was
          deployed.
        </p>
      </header>

      {isError && (
        <AlertBanner tone="danger" title="The deployments could not be read">
          {error instanceof Error ? error.message : 'Try again in a moment.'}
        </AlertBanner>
      )}

      <Card>
        <CardHeader
          icon={<TintedIcon tone="brand" icon={<GitBranch className="h-4 w-4" />} />}
          title="Repositories"
          description="One per website. Deploying replaces the working tree with what the repository says."
          action={
            <RequirePermission permission={Permission.DeployManage}>
              <Button
                onClick={() => setAdding(true)}
                icon={<Plus aria-hidden="true" className="h-4 w-4" />}
              >
                Connect a repository
              </Button>
            </RequirePermission>
          }
        />

        {isPending && !data ? (
          <CardBody>
            <SkeletonRows rows={3} />
          </CardBody>
        ) : repositories.length === 0 ? (
          <CardBody>
            <EmptyState
              icon={<GitBranch className="h-6 w-6" />}
              title="No repositories"
              description="Connect one to deploy a website from git."
            />
          </CardBody>
        ) : (
          <ul className="divide-y divide-slate-100">
            {repositories.map((repository) => (
              <RepositoryRow key={repository.id} repository={repository} />
            ))}
          </ul>
        )}
      </Card>

      {adding && <ConnectRepository onClose={() => setAdding(false)} />}
    </div>
  );
}

function RepositoryRow({ repository }: { repository: GitRepository }) {
  const [open, setOpen] = useState(false);
  const [removing, setRemoving] = useState(false);

  const deploy = useDeploy();
  const remove = useRemoveRepository();
  const generateKey = useGenerateDeployKey();

  const status = repository.status;
  const running = (repository.recent ?? []).some(
    (deployment) => deployment.status === 'running' || deployment.status === 'pending',
  );

  return (
    <li className="px-5 py-4">
      <div className="flex items-start gap-4">
        <div className="min-w-0 flex-1">
          <p className="text-sm font-medium text-slate-900">
            {repository.website}
            <span className="ml-2 font-normal text-slate-500">
              {repository.branch}
            </span>
            {repository.auto_deploy && (
              <span className="ml-2 rounded bg-warn-50 px-1.5 py-0.5 text-xs text-warn-800">
                deploys on push
              </span>
            )}
          </p>
          <p className="mt-0.5 truncate text-xs text-slate-500">{repository.remote_url}</p>

          {repository.current_commit && (
            <p className="mt-1 text-xs text-slate-500">
              Running {repository.current_commit.slice(0, 8)}
              {status?.message ? ` · ${status.message}` : ''}
            </p>
          )}

          {status?.dirty && (
            <p className="mt-1.5 flex gap-2 text-xs text-warn-700">
              <AlertTriangle aria-hidden="true" className="mt-0.5 h-3.5 w-3.5 shrink-0" />
              <span>
                This working tree has uncommitted changes. Deploying resets it and they
                would be lost.
              </span>
            </p>
          )}

          {(status?.warnings ?? []).map((warning) => (
            <p key={warning} className="mt-1.5 flex gap-2 text-xs text-warn-700">
              <AlertTriangle aria-hidden="true" className="mt-0.5 h-3.5 w-3.5 shrink-0" />
              <span>{warning}</span>
            </p>
          ))}
        </div>

        <div className="flex shrink-0 flex-wrap justify-end gap-2">
          <Button variant="secondary" onClick={() => setOpen(!open)}>
            {open ? 'Hide' : 'History'}
          </Button>
          <RequirePermission permission={Permission.DeployManage}>
            <Button
              loading={deploy.isPending || running}
              onClick={() => deploy.mutate({ id: repository.id })}
              icon={<Play aria-hidden="true" className="h-4 w-4" />}
            >
              {running ? 'Deploying' : 'Deploy'}
            </Button>
            <Button
              variant="secondary"
              loading={generateKey.isPending && generateKey.variables === repository.id}
              onClick={() => generateKey.mutate(repository.id)}
              icon={<KeyRound aria-hidden="true" className="h-4 w-4" />}
              title="Generate a deploy key for this website"
            >
              Deploy key
            </Button>
            <Button
              variant="secondary"
              onClick={() => setRemoving(true)}
              icon={<Trash2 aria-hidden="true" className="h-4 w-4" />}
            >
              Disconnect
            </Button>
          </RequirePermission>
        </div>
      </div>

      {deploy.isError && (
        <p className="mt-2 text-xs text-danger-700">
          {deploy.error instanceof ApiError ? deploy.error.message : 'The deployment did not start.'}
        </p>
      )}

      {open && <RepositoryDetail repository={repository} />}

      <ConfirmDialog
        open={removing}
        title={`Disconnect ${repository.website}?`}
        confirmLabel="Disconnect"
        destructive
        onConfirm={() => {
          remove.mutate(repository.id);
          setRemoving(false);
        }}
        onClose={() => setRemoving(false)}
      >
        The website stops being deployable and its deploy key is removed. The files it
        is serving stay exactly where they are — disconnecting is not taking the site
        offline.
      </ConfirmDialog>
    </li>
  );
}

function RepositoryDetail({ repository }: { repository: GitRepository }) {
  const [openLog, setOpenLog] = useState<string | null>(null);
  const rollback = useRollback();

  return (
    <div className="mt-4 space-y-4 rounded-md bg-surface-muted p-4">
      {repository.deploy_key_public && (
        <div>
          <h3 className="text-xs font-semibold uppercase tracking-wide text-slate-500">
            Deploy key
          </h3>
          <p className="mt-1 text-xs text-slate-500">
            Add this to the repository&rsquo;s deploy keys. The private half is on the
            host and is not stored in this panel.
          </p>
          <pre className="mt-1.5 overflow-x-auto rounded bg-white p-2 text-[11px] text-slate-700">
            {repository.deploy_key_public}
          </pre>
        </div>
      )}

      {repository.webhook_url && (
        <div>
          <h3 className="text-xs font-semibold uppercase tracking-wide text-slate-500">
            Webhook
          </h3>
          <p className="mt-1 text-xs text-slate-500">
            This URL is an address, not a secret. What authenticates a push is the
            signature, computed with the secret you set on both sides.
          </p>
          <pre className="mt-1.5 overflow-x-auto rounded bg-white p-2 text-[11px] text-slate-700">
            {repository.webhook_url}
          </pre>
        </div>
      )}

      <div>
        <h3 className="text-xs font-semibold uppercase tracking-wide text-slate-500">
          Steps
        </h3>
        {(repository.actions ?? []).length === 0 ? (
          <p className="mt-1 text-xs text-slate-500">
            None. A deployment checks the code out and does nothing else.
          </p>
        ) : (
          <ol className="mt-1.5 space-y-1 text-sm text-slate-700">
            {(repository.actions ?? []).map((action, index) => (
              <li key={action.id}>
                {index + 1}. {describeAction(action.kind)}
                {!action.enabled && (
                  <span className="ml-2 text-xs text-slate-500">· skipped</span>
                )}
              </li>
            ))}
          </ol>
        )}
      </div>

      <div>
        <h3 className="text-xs font-semibold uppercase tracking-wide text-slate-500">
          Recent deployments
        </h3>
        {(repository.recent ?? []).length === 0 ? (
          <p className="mt-1 text-xs text-slate-500">Nothing has been deployed yet.</p>
        ) : (
          <ul className="mt-1.5 space-y-2">
            {(repository.recent ?? []).map((deployment) => (
              <li key={deployment.id} className="text-sm">
                <div className="flex items-center gap-2">
                  <DeploymentIcon status={deployment.status} />
                  <span className="min-w-0 flex-1 truncate text-slate-900">
                    {deployment.commit_sha
                      ? `${deployment.commit_sha.slice(0, 8)} ${deployment.commit_message}`
                      : deployment.status}
                  </span>
                  <span className="shrink-0 text-xs text-slate-500">
                    {deployment.trigger}
                  </span>
                  <RequirePermission permission={Permission.DeployManage}>
                    <TextButton
                      size="xs"
                      className="shrink-0"
                      onClick={() =>
                        setOpenLog(openLog === deployment.id ? null : deployment.id)
                      }
                    >
                      Log
                    </TextButton>
                    {deployment.previous_commit && (
                      <TextButton
                        size="xs"
                        className="shrink-0"
                        onClick={() =>
                          rollback.mutate({
                            id: repository.id,
                            commit: deployment.previous_commit,
                          })
                        }
                        title="Deploy the commit this website was on before"
                      >
                        <RotateCcw aria-hidden="true" className="inline h-3 w-3" /> Roll back
                      </TextButton>
                    )}
                  </RequirePermission>
                </div>
                {deployment.rolled_back && (
                  <p className="ml-6 text-xs text-warn-700">
                    Rolled back. The source is back on the previous commit; files the
                    build wrote are still there.
                  </p>
                )}
                {deployment.rollback_error && (
                  <p className="ml-6 text-xs text-danger-700">
                    The rollback failed: {deployment.rollback_error}
                  </p>
                )}
                {openLog === deployment.id && <DeploymentLog id={deployment.id} />}
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

function DeploymentIcon({ status }: { status: Deployment['status'] }) {
  if (status === 'success') {
    return <CheckCircle2 aria-hidden="true" className="h-3.5 w-3.5 shrink-0 text-ok-600" />;
  }
  if (status === 'failed' || status === 'cancelled') {
    return <XCircle aria-hidden="true" className="h-3.5 w-3.5 shrink-0 text-danger-600" />;
  }
  return <Play aria-hidden="true" className="h-3.5 w-3.5 shrink-0 text-slate-400" />;
}

function DeploymentLog({ id }: { id: string }) {
  const { data, isPending } = useDeploymentLog(id);

  if (isPending) {
    return <p className="ml-6 mt-1 text-xs text-slate-500">Reading the log&hellip;</p>;
  }
  if (!data?.log) {
    return <p className="ml-6 mt-1 text-xs text-slate-500">This deployment printed nothing.</p>;
  }
  return (
    <pre className="ml-6 mt-1 max-h-80 overflow-auto rounded bg-slate-900 p-3 text-[11px] leading-relaxed text-slate-100">
      {data.truncated && '[earlier output was dropped]\n'}
      {data.log}
    </pre>
  );
}

/** describeAction names a step in words rather than in its key. */
function describeAction(kind: string): string {
  switch (kind) {
    case 'composer.install':
      return 'composer install (production dependencies)';
    case 'npm.ci':
      return 'npm ci (exactly what the lockfile says)';
    case 'npm.install':
      return 'npm install';
    case 'npm.build':
      return 'npm run build';
    case 'artisan.migrate':
      return 'php artisan migrate';
    case 'artisan.optimise':
      return 'php artisan optimize';
    case 'script':
      return 'the deployment script';
    default:
      return kind;
  }
}

function ConnectRepository({ onClose }: { onClose: () => void }) {
  const configure = useConfigureRepository();
  const websites = useWebsites();

  const [websiteID, setWebsiteID] = useState('');
  const [remote, setRemote] = useState('');
  const [branch, setBranch] = useState('main');
  const [provider, setProvider] = useState<DeployProvider>('none');
  const [secret, setSecret] = useState('');
  const [autoDeploy, setAutoDeploy] = useState(false);

  return (
    <Modal open title="Connect a repository" onClose={onClose}>
      <div className="space-y-4">
        {configure.isError && (
          <AlertBanner tone="danger" title="The repository was not connected">
            {configure.error instanceof ApiError
              ? configure.error.message
              : 'Try again.'}
          </AlertBanner>
        )}

        <SelectField
          id="deploy-website"
          label="Website"
          value={websiteID}
          onChange={(event) => setWebsiteID(event.target.value)}
          hint="The deployment runs as this website's own account, in its document root."
        >
          <option value="">Choose a website</option>
          {(websites.data?.websites ?? []).map((website) => (
            <option key={website.id} value={website.id}>
              {website.primary_domain}
            </option>
          ))}
        </SelectField>

        <TextField
          id="deploy-remote"
          label="Repository"
          value={remote}
          onChange={(event) => setRemote(event.target.value)}
          placeholder="git@github.com:owner/repo.git"
          hint="An https:// or ssh address. Other git transports can name a program to run, so the panel does not accept them."
        />

        <TextField
          id="deploy-branch"
          label="Branch"
          value={branch}
          onChange={(event) => setBranch(event.target.value)}
        />

        <SelectField
          id="deploy-provider"
          label="Deploy on push"
          value={provider}
          onChange={(event) => setProvider(event.target.value as DeployProvider)}
          hint="A webhook lets a push start a deployment. It is verified by a signature over the request body, not by the URL."
        >
          <option value="none">No webhook — deploy from this panel only</option>
          <option value="github">GitHub</option>
          <option value="gitlab">GitLab</option>
          <option value="generic">Anything that can sign a request</option>
        </SelectField>

        {provider !== 'none' && (
          <>
            <TextField
              id="deploy-secret"
              label="Webhook secret"
              type="password"
              value={secret}
              onChange={(event) => setSecret(event.target.value)}
              hint="At least 16 characters, and the same value in the forge. Without it, anybody who finds the URL could deploy this website."
            />
            <Toggle
              id="deploy-auto"
              label="Deploy automatically on a verified push"
              description="A person with write access to the repository can then run code on this host without touching the panel. That is the point of it, and it is worth deciding rather than inheriting."
              checked={autoDeploy}
              onChange={setAutoDeploy}
            />
          </>
        )}

        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button
            loading={configure.isPending}
            onClick={() =>
              configure.mutate(
                {
                  website_id: websiteID,
                  remote_url: remote,
                  branch,
                  provider,
                  webhook_secret: secret,
                  auto_deploy: autoDeploy,
                },
                { onSuccess: onClose },
              )
            }
          >
            Connect
          </Button>
        </div>
      </div>
    </Modal>
  );
}
