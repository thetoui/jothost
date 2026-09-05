import { useState, type FormEvent } from 'react';
import { ShieldCheck, ShieldOff } from 'lucide-react';

import { StatusPill } from '@/components/StatusPill';
import { Button } from '@/components/ui/Button';
import {
  errorMessage,
  useDisableTwoFactor,
  useEnableTwoFactor,
  useProfile,
  useSetupTwoFactor,
} from '@/features/auth/hooks';

/**
 * Account security page: two-factor enrolment and removal for the signed-in
 * user. Managing other people's accounts arrives with Phase 22.
 */
export function SecurityPage() {
  const { data: profile, isPending } = useProfile();

  return (
    <div className="mx-auto max-w-3xl space-y-6">
      <div>
        <h1 className="text-2xl font-semibold text-slate-900">Account security</h1>
        <p className="mt-1 text-sm text-slate-600">
          Protect your panel account with a second factor.
        </p>
      </div>

      <section
        aria-labelledby="two-factor-heading"
        className="rounded-lg border border-surface-border bg-surface p-5"
      >
        <div className="flex items-center justify-between">
          <h2 id="two-factor-heading" className="text-sm font-semibold text-slate-900">
            Two-factor authentication
          </h2>
          {isPending ? (
            <StatusPill label="Loading" tone="neutral" />
          ) : profile?.two_factor_enabled ? (
            <StatusPill label="Enabled" tone="ok" />
          ) : (
            <StatusPill label="Disabled" tone="warn" />
          )}
        </div>

        <div className="mt-4">
          {profile?.two_factor_enabled ? <DisableTwoFactor /> : <EnableTwoFactor />}
        </div>
      </section>
    </div>
  );
}

function EnableTwoFactor() {
  const setup = useSetupTwoFactor();
  const enable = useEnableTwoFactor();
  const [code, setCode] = useState('');

  function handleEnable(event: FormEvent) {
    event.preventDefault();
    enable.mutate(code);
  }

  if (!setup.data) {
    return (
      <div className="space-y-3">
        <p className="text-sm text-slate-600">
          An authenticator app generates a 6-digit code that changes every 30 seconds.
        </p>
        {setup.isError && (
          <p role="alert" className="text-sm text-danger-700">
            {errorMessage(setup.error, 'Could not start setup.')}
          </p>
        )}
        <Button
          variant="primary"
          onClick={() => setup.mutate()}
          loading={setup.isPending}
          icon={<ShieldCheck aria-hidden="true" className="h-4 w-4" />}
        >
          Set up two-factor authentication
        </Button>
      </div>
    );
  }

  return (
    <form onSubmit={handleEnable} className="space-y-4">
      <ol className="list-decimal space-y-3 pl-5 text-sm text-slate-600">
        <li>Open your authenticator app and add a new account.</li>
        <li>
          Enter this setup key:
          {/* The secret is shown once, here, and is never logged or stored by
              the client. */}
          <code className="mt-1 block break-all rounded bg-surface-muted px-3 py-2 font-mono text-xs text-slate-900">
            {setup.data.secret}
          </code>
        </li>
        <li>Enter the 6-digit code the app displays.</li>
      </ol>

      <div className="space-y-1">
        <label htmlFor="totp-code" className="block text-xs font-medium text-slate-700">
          Verification code
        </label>
        <input
          id="totp-code"
          type="text"
          inputMode="numeric"
          autoComplete="one-time-code"
          maxLength={6}
          required
          value={code}
          onChange={(event) => setCode(event.target.value.replace(/\D/g, ''))}
          className="w-40 rounded-md border border-surface-border px-3 py-2 text-center font-mono tracking-widest outline-none focus:border-brand-500"
        />
      </div>

      {enable.isError && (
        <p role="alert" className="text-sm text-danger-700">
          {errorMessage(enable.error, 'Verification failed.')}
        </p>
      )}

      <Button
        type="submit"
        variant="primary"
        loading={enable.isPending}
        disabled={code.length !== 6}
      >
        Enable
      </Button>
    </form>
  );
}

function DisableTwoFactor() {
  const disable = useDisableTwoFactor();
  const [password, setPassword] = useState('');

  function handleDisable(event: FormEvent) {
    event.preventDefault();
    disable.mutate(password, { onSuccess: () => setPassword('') });
  }

  return (
    <form onSubmit={handleDisable} className="space-y-4">
      <p className="text-sm text-slate-600">
        Two-factor authentication is protecting this account. Confirm your password to turn it
        off.
      </p>

      <div className="space-y-1">
        <label htmlFor="confirm-password" className="block text-xs font-medium text-slate-700">
          Current password
        </label>
        <input
          id="confirm-password"
          type="password"
          autoComplete="current-password"
          required
          value={password}
          onChange={(event) => setPassword(event.target.value)}
          className="w-full max-w-xs rounded-md border border-surface-border px-3 py-2 text-sm outline-none focus:border-brand-500"
        />
      </div>

      {disable.isError && (
        <p role="alert" className="text-sm text-danger-700">
          {errorMessage(disable.error, 'Could not disable two-factor authentication.')}
        </p>
      )}

      <Button
        type="submit"
        variant="danger"
        loading={disable.isPending}
        disabled={password === ''}
        icon={<ShieldOff aria-hidden="true" className="h-4 w-4" />}
      >
        Disable
      </Button>
    </form>
  );
}
