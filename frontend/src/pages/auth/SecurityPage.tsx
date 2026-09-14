import { useState, type FormEvent } from 'react';
import { KeyRound, ShieldCheck, ShieldOff } from 'lucide-react';

import { StatusPill } from '@/components/StatusPill';
import { Button } from '@/components/ui/Button';
import { RecoveryCodes } from '@/features/auth/components/RecoveryCodes';
import {
  errorMessage,
  useDisableTwoFactor,
  useEnableTwoFactor,
  useProfile,
  useRegenerateRecoveryCodes,
  useSetupTwoFactor,
} from '@/features/auth/hooks';

/** Fewer than this many unused codes is worth a warning. */
const LOW_RECOVERY_CODES = 3;

/**
 * Account security page: two-factor enrolment and removal for the signed-in
 * user. Managing other people's accounts arrives with Phase 22.
 */
export function SecurityPage() {
  const { data: profile, isPending } = useProfile();
  // Codes just issued, by enabling or replacing them. Held here rather than in
  // the form that asked for them: enabling flips the profile, which swaps that
  // form out, and the codes must still be on screen when it does.
  const [issuedCodes, setIssuedCodes] = useState<string[] | null>(null);

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
          {issuedCodes ? (
            <RecoveryCodes codes={issuedCodes} onDone={() => setIssuedCodes(null)} />
          ) : profile?.two_factor_enabled ? (
            <div className="space-y-6">
              <RecoveryCodesSection
                remaining={profile.recovery_codes_remaining}
                onIssued={setIssuedCodes}
              />
              <DisableTwoFactor />
            </div>
          ) : (
            <EnableTwoFactor onEnabled={setIssuedCodes} />
          )}
        </div>
      </section>
    </div>
  );
}

function EnableTwoFactor({ onEnabled }: { onEnabled: (codes: string[]) => void }) {
  const setup = useSetupTwoFactor();
  const enable = useEnableTwoFactor();
  const [code, setCode] = useState('');

  function handleEnable(event: FormEvent) {
    event.preventDefault();
    enable.mutate(code, { onSuccess: (result) => onEnabled(result.recovery_codes) });
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

function RecoveryCodesSection({
  remaining,
  onIssued,
}: {
  remaining: number;
  onIssued: (codes: string[]) => void;
}) {
  const regenerate = useRegenerateRecoveryCodes();
  const [password, setPassword] = useState('');

  function handleRegenerate(event: FormEvent) {
    event.preventDefault();
    regenerate.mutate(password, {
      onSuccess: (result) => {
        setPassword('');
        onIssued(result.recovery_codes);
      },
    });
  }

  const low = remaining < LOW_RECOVERY_CODES;

  return (
    <form onSubmit={handleRegenerate} className="space-y-4">
      <div>
        <h3 className="text-sm font-medium text-slate-900">Recovery codes</h3>
        <p className={`mt-1 text-sm ${low ? 'text-danger-700' : 'text-slate-600'}`}>
          {remaining === 1 ? '1 unused code left.' : `${remaining} unused codes left.`}{' '}
          {low
            ? 'Replace them before you run out, or a lost authenticator will lock you out.'
            : 'Each signs you in once without your authenticator.'}
        </p>
      </div>

      <div className="space-y-1">
        <label htmlFor="recovery-password" className="block text-xs font-medium text-slate-700">
          Password to replace them
        </label>
        <input
          id="recovery-password"
          type="password"
          autoComplete="current-password"
          required
          value={password}
          onChange={(event) => setPassword(event.target.value)}
          className="w-full max-w-xs rounded-md border border-surface-border px-3 py-2 text-sm outline-none focus:border-brand-500"
        />
      </div>

      {regenerate.isError && (
        <p role="alert" className="text-sm text-danger-700">
          {errorMessage(regenerate.error, 'Could not replace the recovery codes.')}
        </p>
      )}

      <Button
        type="submit"
        loading={regenerate.isPending}
        disabled={password === ''}
        icon={<KeyRound aria-hidden="true" className="h-4 w-4" />}
      >
        Replace recovery codes
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
