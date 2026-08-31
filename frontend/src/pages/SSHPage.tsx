import { useEffect, useMemo, useState } from 'react';
import {
  AlertTriangle,
  Info,
  KeyRound,
  ShieldAlert,
  Terminal,
  Trash2,
  UserCog,
} from 'lucide-react';

import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { SelectField, TextField, Toggle } from '@/components/ui/Field';
import { SkeletonRows } from '@/components/ui/Loading';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import {
  useAddSSHKey,
  useConfigureSSH,
  useRemoveSSHKey,
  useSSHKeys,
  useSSHStatus,
} from '@/features/ssh/hooks';
import { ApiError } from '@/services/apiClient';
import type { SSHFinding, SSHKey } from '@/types/api';

/** What PermitRootLogin may be set to, in the operator's words. */
const rootLoginOptions: { value: string; label: string }[] = [
  { value: 'no', label: 'Not at all' },
  { value: 'prohibit-password', label: 'With a key only' },
  { value: 'forced-commands-only', label: 'Only for forced commands' },
  { value: 'yes', label: 'With a password or a key' },
];

/**
 * SSHPage shows and changes how the host accepts SSH.
 *
 * The page is arranged around the thing that can go wrong: every control that
 * could lock somebody out is next to the state that decides whether it will,
 * and a change the panel would refuse is refused with the reason rather than
 * accepted and regretted.
 */
export function SSHPage() {
  const { data, isPending, isError, error } = useSSHStatus();

  const config = data?.config;
  const accounts = useMemo(() => data?.accounts ?? [], [data]);
  const findings = data?.findings ?? [];

  const [account, setAccount] = useState('');
  useEffect(() => {
    if (account === '' && accounts.length > 0) {
      setAccount(accounts[0]?.name ?? '');
    }
  }, [accounts, account]);

  return (
    <div className="space-y-5">
      <header>
        <h1 className="text-xl font-semibold text-slate-900">SSH</h1>
        <p className="mt-1 text-sm text-slate-500">
          How this host accepts remote logins, and who may make them.
        </p>
      </header>

      {isError && (
        <Alert tone="danger" title="The SSH settings could not be read">
          {error instanceof Error ? error.message : 'Try again in a moment.'}
        </Alert>
      )}

      {isPending ? (
        <Card>
          <SkeletonRows rows={5} />
        </Card>
      ) : !config?.available ? (
        <Alert tone="info" title="This host has no SSH server">
          {config?.reason ||
            'Nothing here accepts remote logins, so there is nothing to configure.'}
        </Alert>
      ) : (
        <>
          {!config.managed && (
            <Alert tone="warning" title="These settings are read-only on this host">
              {config.reason}
            </Alert>
          )}

          {!config.running && (
            <Alert tone="info" title="The SSH server is not running">
              The settings below are what it would use if it were started. Nothing is
              listening for connections now.
            </Alert>
          )}

          <Findings findings={findings} />

          <Settings
            port={config.ports[0] ?? 22}
            rootLogin={config.root_login}
            passwords={config.password_authentication}
            pubkey={config.pubkey_authentication}
            emptyPasswords={config.permit_empty_passwords}
            x11={config.x11_forwarding}
            maxAuthTries={config.max_auth_tries}
            managed={config.managed}
            dropIn={config.drop_in_path}
          />

          <Keys
            accounts={accounts.map((entry) => ({ name: entry.name, keys: entry.keys }))}
            account={account}
            onAccount={setAccount}
          />
        </>
      )}
    </div>
  );
}

function Findings({ findings }: { findings: SSHFinding[] }) {
  if (findings.length === 0) {
    return (
      <Alert tone="success" title="Nothing to recommend">
        This host&rsquo;s SSH settings are what they should be.
      </Alert>
    );
  }

  const order = { high: 0, warn: 1, info: 2 };
  const sorted = [...findings].sort((a, b) => order[a.severity] - order[b.severity]);

  return (
    <Card>
      <CardHeader
        title="Recommendations"
        description="What is worth changing about this host, and why."
        icon={<TintedIcon tone="warn" icon={<ShieldAlert className="h-4 w-4" />} />}
      />
      <CardBody className="divide-y divide-surface-border p-0">
        {sorted.map((finding) => (
          <div key={finding.id} className="flex gap-3 px-5 py-3.5">
            <span
              className={
                finding.severity === 'high'
                  ? 'mt-0.5 text-rose-500'
                  : finding.severity === 'warn'
                    ? 'mt-0.5 text-amber-500'
                    : 'mt-0.5 text-slate-400'
              }
            >
              {finding.severity === 'info' ? (
                <Info aria-hidden="true" className="h-4 w-4" />
              ) : (
                <AlertTriangle aria-hidden="true" className="h-4 w-4" />
              )}
            </span>
            <div className="min-w-0">
              <p className="font-medium text-slate-900">{finding.title}</p>
              <p className="mt-0.5 text-sm text-slate-600">{finding.detail}</p>
              <p className="mt-1 text-sm text-slate-500">{finding.action}</p>
            </div>
          </div>
        ))}
      </CardBody>
    </Card>
  );
}

interface SettingsProps {
  port: number;
  rootLogin: string;
  passwords: boolean;
  pubkey: boolean;
  emptyPasswords: boolean;
  x11: boolean;
  maxAuthTries: number;
  managed: boolean;
  dropIn: string;
}

function Settings(props: SettingsProps) {
  const configure = useConfigureSSH();
  const [port, setPort] = useState(String(props.port));

  useEffect(() => setPort(String(props.port)), [props.port]);

  const message =
    configure.error instanceof ApiError
      ? configure.error.message
      : configure.error
        ? 'The setting could not be changed.'
        : null;

  const disabled = !props.managed || configure.isPending;

  return (
    <Card>
      <CardHeader
        title="Server settings"
        description={props.managed ? `Written to ${props.dropIn}` : undefined}
        icon={<TintedIcon tone="brand" icon={<Terminal className="h-4 w-4" />} />}
      />
      <CardBody className="space-y-4">
        {message && (
          <Alert tone="danger" title="The change was refused">
            {message}
          </Alert>
        )}

        {configure.isSuccess && configure.data && !configure.data.reloaded && (
          <Alert tone="warning" title="The change is saved but not live">
            The SSH server did not restart, so it is still running the previous
            settings. Restart it from the Services page.
          </Alert>
        )}

        <RequirePermission permission={Permission.ServerManage}>
          <div className="grid gap-4 sm:grid-cols-2">
            <SelectField
              id="ssh-root-login"
              label="Root may log in"
              hint="Root is the account every scanner already knows the name of."
              value={normaliseRootLogin(props.rootLogin)}
              disabled={disabled}
              onChange={(event) => configure.mutate({ root_login: event.target.value })}
            >
              {rootLoginOptions.map((option) => (
                <option key={option.value} value={option.value}>
                  {option.label}
                </option>
              ))}
            </SelectField>

            <TextField
              id="ssh-port"
              label="Port"
              hint="Allow the new port in the firewall before changing it here."
              value={port}
              inputMode="numeric"
              disabled={disabled}
              onChange={(event) => setPort(event.target.value)}
              onBlur={() => {
                const value = Number(port);
                if (Number.isInteger(value) && value !== props.port) {
                  configure.mutate({ port: value });
                }
              }}
            />
          </div>

          <div className="space-y-3">
            <Toggle
              id="ssh-passwords"
              label="Passwords may be used to log in"
              description="A password can be guessed at speed by anyone who can reach the port. A key cannot."
              checked={props.passwords}
              disabled={disabled}
              onChange={(checked) => configure.mutate({ password_authentication: checked })}
            />
            <Toggle
              id="ssh-pubkey"
              label="Keys may be used to log in"
              checked={props.pubkey}
              disabled={disabled}
              onChange={(checked) => configure.mutate({ pubkey_authentication: checked })}
            />
            <Toggle
              id="ssh-empty"
              label="Accounts with no password may log in"
              description="Almost never deliberate."
              checked={props.emptyPasswords}
              disabled={disabled}
              onChange={(checked) => configure.mutate({ permit_empty_passwords: checked })}
            />
            <Toggle
              id="ssh-x11"
              label="X11 forwarding"
              description="A hosting server has no use for it."
              checked={props.x11}
              disabled={disabled}
              onChange={(checked) => configure.mutate({ x11_forwarding: checked })}
            />
          </div>
        </RequirePermission>
      </CardBody>
    </Card>
  );
}

function Keys({
  accounts,
  account,
  onAccount,
}: {
  accounts: { name: string; keys: number }[];
  account: string;
  onAccount: (value: string) => void;
}) {
  const { data, isPending } = useSSHKeys(account);
  const add = useAddSSHKey();
  const remove = useRemoveSSHKey();

  const [entry, setEntry] = useState('');
  const [confirming, setConfirming] = useState<SSHKey | null>(null);

  const message =
    add.error instanceof ApiError
      ? add.error.message
      : remove.error instanceof ApiError
        ? remove.error.message
        : null;

  const keys = data?.keys ?? [];

  return (
    <Card>
      <CardHeader
        title="Authorised keys"
        description="Only accounts that can actually log in are listed."
        icon={<TintedIcon tone="brand" icon={<KeyRound className="h-4 w-4" />} />}
      />
      <CardBody className="space-y-4">
        {message && (
          <Alert tone="danger" title="The key could not be changed">
            {message}
          </Alert>
        )}

        {accounts.length === 0 ? (
          <p className="text-sm text-slate-500">
            No account on this host can log in over SSH.
          </p>
        ) : (
          <>
            <SelectField
              id="ssh-account"
              label="Account"
              value={account}
              onChange={(event) => onAccount(event.target.value)}
            >
              {accounts.map((entry) => (
                <option key={entry.name} value={entry.name}>
                  {entry.name} ({entry.keys} key{entry.keys === 1 ? '' : 's'})
                </option>
              ))}
            </SelectField>

            {isPending ? (
              <SkeletonRows rows={2} />
            ) : keys.length === 0 ? (
              <p className="text-sm text-slate-500">
                This account has no authorised keys, so it can only be logged into with
                a password.
              </p>
            ) : (
              <ul className="divide-y divide-surface-border rounded-md border border-surface-border">
                {keys.map((key) => (
                  <li key={key.fingerprint} className="flex items-center gap-3 px-4 py-3">
                    <UserCog aria-hidden="true" className="h-4 w-4 shrink-0 text-slate-400" />
                    <div className="min-w-0 flex-1">
                      <p className="truncate font-medium text-slate-900">
                        {key.comment || 'No comment'}
                      </p>
                      <p className="truncate font-mono text-xs text-slate-500">
                        {key.fingerprint}
                      </p>
                      <p className="text-xs text-slate-400">
                        {key.type}
                        {key.bits > 0 && <> · {key.bits} bits</>}
                      </p>
                    </div>
                    <RequirePermission permission={Permission.ServerManage}>
                      <Button
                        variant="secondary"
                        onClick={() => setConfirming(key)}
                        icon={<Trash2 aria-hidden="true" className="h-4 w-4" />}
                      >
                        Remove
                      </Button>
                    </RequirePermission>
                  </li>
                ))}
              </ul>
            )}

            <RequirePermission permission={Permission.ServerManage}>
              <div className="space-y-2">
                <TextField
                  id="ssh-new-key"
                  label="Add a key"
                  hint="One public key, as it appears in id_ed25519.pub."
                  placeholder="ssh-ed25519 AAAA… you@laptop"
                  value={entry}
                  onChange={(event) => setEntry(event.target.value)}
                />
                <Button
                  disabled={entry.trim() === '' || add.isPending}
                  onClick={() =>
                    add.mutate(
                      { account, key: entry.trim() },
                      { onSuccess: () => setEntry('') },
                    )
                  }
                >
                  {add.isPending ? 'Adding…' : 'Authorise key'}
                </Button>
              </div>
            </RequirePermission>
          </>
        )}
      </CardBody>

      <ConfirmDialog
        open={confirming !== null}
        onClose={() => setConfirming(null)}
        onConfirm={() => {
          if (confirming) {
            remove.mutate({ account, fingerprint: confirming.fingerprint });
          }
          setConfirming(null);
        }}
        title="Remove this key?"
        confirmLabel="Remove key"
        destructive
        loading={remove.isPending}
      >
        <p>
          Whoever holds it can no longer log in as {account}. If it is the only key and
          passwords are off, nobody can.
        </p>
      </ConfirmDialog>
    </Card>
  );
}

/** normaliseRootLogin maps sshd's two spellings of the same value onto one. */
function normaliseRootLogin(value: string): string {
  return value === 'without-password' ? 'prohibit-password' : value;
}
