import { useEffect, useState } from 'react';
import { Ban, Download, ShieldCheck, ShieldOff, Trash2 } from 'lucide-react';

import { StatusPill } from '@/components/StatusPill';
import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { SelectField, TextField, Toggle } from '@/components/ui/Field';
import { TextLink } from '@/components/ui/Link';
import { EmptyState, SkeletonRows } from '@/components/ui/Loading';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import {
  useBannedAddresses,
  useConfigureJail,
  useFail2BanStatus,
  useInstallFail2Ban,
  useSetIgnored,
  useUnban,
} from '@/features/fail2ban/hooks';
import { ApiError } from '@/services/apiClient';
import type { Fail2BanJail } from '@/types/api';

/** The ban lengths offered, in the operator's words. */
const banTimes: { value: number; label: string }[] = [
  { value: 600, label: '10 minutes' },
  { value: 3600, label: '1 hour' },
  { value: 6 * 3600, label: '6 hours' },
  { value: 24 * 3600, label: '1 day' },
  { value: 7 * 24 * 3600, label: '1 week' },
];

/** How many failures are allowed before a ban. */
const retries = [3, 5, 10, 20];

/**
 * Fail2BanPage shows and changes what this host bans.
 *
 * What it deliberately does not offer: a box to type a filter into. A jail is a
 * regular expression matched against a log, and the wrong one bans the wrong
 * person — or, far more often, bans nobody while the page reports protection.
 * The panel offers the jails it knows and the numbers that decide how strict
 * they are.
 */
export function Fail2BanPage() {
  const { data, isPending, isError, error } = useFail2BanStatus();
  const install = useInstallFail2Ban();

  const status = data;
  const banned = useBannedAddresses(Boolean(status?.running));

  const message =
    install.error instanceof ApiError ? install.error.message : null;

  return (
    <div className="space-y-5">
      <header>
        <h1 className="text-xl font-semibold text-slate-900">Intrusion prevention</h1>
        <p className="mt-1 text-sm text-slate-500">
          What this host bans, for how long, and who it is banning now.
        </p>
      </header>

      {isError && (
        <Alert tone="danger" title="The intrusion prevention settings could not be read">
          {error instanceof Error ? error.message : 'Try again in a moment.'}
        </Alert>
      )}

      {message && (
        <Alert tone="danger" title="fail2ban could not be installed">
          {message}
        </Alert>
      )}

      {isPending ? (
        <Card>
          <SkeletonRows rows={4} />
        </Card>
      ) : !status?.available ? (
        <Card>
          <CardBody>
            <EmptyState
              icon={<ShieldOff className="h-6 w-6" />}
              title="fail2ban is not installed"
              description={
                status?.can_install
                  ? 'It watches the logs for repeated failures and bans the address they come from.'
                  : status?.reason || 'This host has no package manager the panel can install with.'
              }
              action={
                status?.can_install ? (
                  <RequirePermission permission={Permission.FirewallManage}>
                    <Button
                      onClick={() => install.mutate()}
                      disabled={install.isPending}
                      icon={<Download aria-hidden="true" className="h-4 w-4" />}
                    >
                      {install.isPending ? 'Installing…' : 'Install fail2ban'}
                    </Button>
                  </RequirePermission>
                ) : undefined
              }
            />
          </CardBody>
        </Card>
      ) : (
        <>
          {!status.running && (
            <Alert tone="warning" title="fail2ban is not running">
              The settings below are what it would use. Nothing is being banned until it
              is started, which is done from the{' '}
              <TextLink to="/services">
                Services page
              </TextLink>
              .
            </Alert>
          )}

          <Jails jails={status.jails} running={status.running} />

          <Ignored addresses={status.ignored} />

          <Banned
            entries={banned.data?.banned ?? []}
            running={status.running}
            loading={banned.isPending && Boolean(status.running)}
          />
        </>
      )}
    </div>
  );
}

function Jails({ jails, running }: { jails: Fail2BanJail[]; running: boolean }) {
  return (
    <Card>
      <CardHeader
        title="Jails"
        description="Each watches a log and bans the addresses that keep failing."
        icon={<TintedIcon tone="brand" icon={<ShieldCheck className="h-4 w-4" />} />}
      />
      <CardBody className="divide-y divide-surface-border p-0">
        {jails.length === 0 ? (
          <p className="px-5 py-4 text-sm text-slate-500">This host has no jails.</p>
        ) : (
          jails.map((jail) => <JailRow key={jail.name} jail={jail} running={running} />)
        )}
      </CardBody>
    </Card>
  );
}

function JailRow({ jail, running }: { jail: Fail2BanJail; running: boolean }) {
  const configure = useConfigureJail();

  const message =
    configure.error instanceof ApiError ? configure.error.message : null;

  // A jail somebody wrote by hand is shown and not touched: it is banning
  // people whether the panel knows how it was configured or not.
  const editable = jail.managed && jail.available;

  return (
    <div className="px-5 py-3.5">
      <div className="flex flex-wrap items-start gap-3">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-medium text-slate-900">{jail.label || jail.name}</span>
            {jail.enabled && running ? (
              <StatusPill label="Watching" tone="ok" dot />
            ) : jail.enabled ? (
              <StatusPill label="Configured" tone="warn" dot />
            ) : (
              <StatusPill label="Off" tone="neutral" dot />
            )}
            {!jail.managed && (
              <span className="rounded-full bg-slate-100 px-2 py-0.5 text-xs text-slate-600">
                Configured outside the panel
              </span>
            )}
          </div>

          <p className="mt-0.5 text-sm text-slate-500">{jail.summary || jail.name}</p>

          {jail.available ? (
            <p className="mt-0.5 text-xs text-slate-400">
              {jail.log_paths.join(', ') || 'no log'}
              {jail.enabled && running && (
                <>
                  {' '}
                  · {jail.currently_failed} failing now · {jail.total_banned} banned in
                  total
                </>
              )}
            </p>
          ) : (
            <p className="mt-0.5 text-xs text-warn-600">{jail.reason}</p>
          )}
        </div>

        <RequirePermission permission={Permission.FirewallManage}>
          <Toggle
            id={`jail-${jail.name}`}
            label="On"
            checked={jail.enabled}
            disabled={!editable || configure.isPending}
            onChange={(checked) =>
              configure.mutate({ jail: jail.name, change: { enabled: checked } })
            }
          />
        </RequirePermission>
      </div>

      {editable && jail.enabled && (
        <RequirePermission permission={Permission.FirewallManage}>
          <div className="mt-3 grid gap-3 sm:grid-cols-2">
            <SelectField
              id={`retry-${jail.name}`}
              label="Failures allowed"
              value={String(jail.max_retry)}
              disabled={configure.isPending}
              onChange={(event) =>
                configure.mutate({
                  jail: jail.name,
                  change: { max_retry: Number(event.target.value) },
                })
              }
            >
              {[...new Set([...retries, jail.max_retry])]
                .sort((a, b) => a - b)
                .map((value) => (
                  <option key={value} value={value}>
                    {value}
                  </option>
                ))}
            </SelectField>

            <SelectField
              id={`bantime-${jail.name}`}
              label="Banned for"
              value={String(jail.ban_time)}
              disabled={configure.isPending}
              onChange={(event) =>
                configure.mutate({
                  jail: jail.name,
                  change: { ban_time: Number(event.target.value) },
                })
              }
            >
              {[...banTimes, { value: jail.ban_time, label: `${jail.ban_time} seconds` }]
                .filter(
                  (option, index, all) =>
                    all.findIndex((other) => other.value === option.value) === index,
                )
                .sort((a, b) => a.value - b.value)
                .map((option) => (
                  <option key={option.value} value={option.value}>
                    {option.label}
                  </option>
                ))}
            </SelectField>
          </div>
        </RequirePermission>
      )}

      {message && (
        <Alert tone="danger" title={`${jail.label || jail.name} could not be changed`}>
          {message}
        </Alert>
      )}
    </div>
  );
}

function Ignored({ addresses }: { addresses: string[] }) {
  const setIgnored = useSetIgnored();
  const [draft, setDraft] = useState('');

  useEffect(() => setDraft(addresses.join(', ')), [addresses]);

  const message = setIgnored.error instanceof ApiError ? setIgnored.error.message : null;

  return (
    <Card>
      <CardHeader
        title="Never banned"
        description="Addresses no jail will block, whatever they do."
        icon={<TintedIcon tone="neutral" icon={<ShieldCheck className="h-4 w-4" />} />}
      />
      <CardBody className="space-y-3">
        {message && (
          <Alert tone="danger" title="The list could not be saved">
            {message}
          </Alert>
        )}

        <RequirePermission permission={Permission.FirewallManage}>
          <TextField
            id="fail2ban-ignored"
            label="Addresses"
            hint="Separated by commas. Your own address belongs here — a few mistyped passwords is all it takes. Loopback is always included."
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
          />
          <Button
            disabled={setIgnored.isPending}
            onClick={() =>
              setIgnored.mutate(
                draft
                  .split(',')
                  .map((entry) => entry.trim())
                  .filter((entry) => entry !== ''),
              )
            }
          >
            {setIgnored.isPending ? 'Saving…' : 'Save'}
          </Button>
        </RequirePermission>
      </CardBody>
    </Card>
  );
}

function Banned({
  entries,
  running,
  loading,
}: {
  entries: { address: string; jail: string }[];
  running: boolean;
  loading: boolean;
}) {
  const unban = useUnban();
  const [confirming, setConfirming] = useState<{ address: string; jail: string } | null>(
    null,
  );

  return (
    <Card>
      <CardHeader
        title={`Banned now${entries.length > 0 ? ` (${entries.length})` : ''}`}
        description="Blocked at the firewall until the ban expires."
        icon={<TintedIcon tone="warn" icon={<Ban className="h-4 w-4" />} />}
      />
      <CardBody className="p-0">
        {!running ? (
          <p className="px-5 py-4 text-sm text-slate-500">
            Nothing is banned while fail2ban is stopped.
          </p>
        ) : loading ? (
          <SkeletonRows rows={2} />
        ) : entries.length === 0 ? (
          <p className="px-5 py-4 text-sm text-slate-500">
            Nothing is banned at the moment.
          </p>
        ) : (
          <ul className="divide-y divide-surface-border">
            {entries.map((entry) => (
              <li
                key={`${entry.jail}:${entry.address}`}
                className="flex items-center gap-3 px-5 py-3"
              >
                <div className="min-w-0 flex-1">
                  <p className="font-mono text-sm text-slate-900">{entry.address}</p>
                  <p className="text-xs text-slate-400">banned by {entry.jail}</p>
                </div>
                <RequirePermission permission={Permission.FirewallManage}>
                  <Button
                    variant="secondary"
                    onClick={() => setConfirming(entry)}
                    icon={<Trash2 aria-hidden="true" className="h-4 w-4" />}
                  >
                    Unban
                  </Button>
                </RequirePermission>
              </li>
            ))}
          </ul>
        )}
      </CardBody>

      <ConfirmDialog
        open={confirming !== null}
        onClose={() => setConfirming(null)}
        onConfirm={() => {
          if (confirming) {
            unban.mutate(confirming);
          }
          setConfirming(null);
        }}
        title={`Unban ${confirming?.address ?? 'this address'}?`}
        confirmLabel="Unban"
        loading={unban.isPending}
      >
        <p>
          It can reach this host again immediately. If it is still trying, it will be
          banned again the next time it passes the threshold.
        </p>
      </ConfirmDialog>
    </Card>
  );
}
