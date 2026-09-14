import { useState, type FormEvent } from 'react';
import { Navigate, useLocation } from 'react-router-dom';
import { ShieldCheck } from 'lucide-react';

import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { TextField } from '@/components/ui/Field';
import { errorMessage, useLogin, useVerifyTwoFactor } from '@/features/auth/hooks';
import { useAuthStore } from '@/stores/authStore';

interface LocationState {
  from?: string;
}

export function LoginPage() {
  const status = useAuthStore((state) => state.status);
  const mfaToken = useAuthStore((state) => state.mfaToken);
  const location = useLocation();

  if (status === 'authenticated') {
    // Return the user to whatever they were trying to reach.
    const state = location.state as LocationState | null;
    return <Navigate to={state?.from ?? '/'} replace />;
  }

  return (
    <div className="relative flex min-h-full items-center justify-center overflow-hidden bg-rail p-6">
      {/* A soft brand wash behind the card, so the sign-in screen belongs to
          this panel rather than being a generic form on a grey page. */}
      <div
        aria-hidden="true"
        className="pointer-events-none absolute inset-0 bg-[radial-gradient(60rem_40rem_at_50%_-10%,rgba(59,110,246,0.28),transparent_65%)]"
      />

      <div className="relative w-full max-w-sm">
        <div className="mb-7 flex flex-col items-center gap-3">
          <span className="grid h-12 w-12 place-items-center rounded-xl bg-brand-500 text-lg font-bold text-white shadow-raised">
            J
          </span>
          <h1 className="text-lg font-semibold text-rail-bright">JotHost Panel</h1>
        </div>

        <div className="rounded-card border border-surface-border bg-surface p-6 shadow-dialog">
          {mfaToken ? <TwoFactorStep /> : <PasswordStep />}
        </div>

        <p className="mt-6 text-center text-xs text-rail-text">
          Server management for Linux hosting
        </p>
      </div>
    </div>
  );
}

function PasswordStep() {
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const login = useLogin();

  function handleSubmit(event: FormEvent) {
    event.preventDefault();
    login.mutate({ username, password });
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      <div>
        <h2 className="text-base font-semibold text-slate-900">Sign in</h2>
        <p className="mt-1 text-sm text-slate-500">Use your panel administrator account.</p>
      </div>

      <TextField
        id="username"
        label="Username"
        name="username"
        type="text"
        autoComplete="username"
        autoFocus
        required
        value={username}
        onChange={(event) => setUsername(event.target.value)}
      />

      <TextField
        id="password"
        label="Password"
        name="password"
        type="password"
        autoComplete="current-password"
        required
        value={password}
        onChange={(event) => setPassword(event.target.value)}
      />

      {login.isError && <Alert tone="danger">{errorMessage(login.error, 'Sign in failed.')}</Alert>}

      <Button type="submit" variant="primary" loading={login.isPending} className="w-full">
        {login.isPending ? 'Signing in…' : 'Sign in'}
      </Button>
    </form>
  );
}

function TwoFactorStep() {
  const [code, setCode] = useState('');
  const [recoveryCode, setRecoveryCode] = useState('');
  const [useRecovery, setUseRecovery] = useState(false);
  const verify = useVerifyTwoFactor();
  const setMfaToken = useAuthStore((state) => state.setMfaToken);

  // A recovery code is sixteen characters however it was copied; the server
  // ignores the dashes and spaces, so the button does too.
  const recoveryReady = recoveryCode.replace(/[\s-]/g, '').length === 16;

  function handleSubmit(event: FormEvent) {
    event.preventDefault();
    verify.mutate(useRecovery ? { recoveryCode } : { code });
  }

  function switchMethod() {
    verify.reset();
    setUseRecovery((current) => !current);
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      <div className="flex items-start gap-3">
        <ShieldCheck aria-hidden="true" className="mt-0.5 h-5 w-5 shrink-0 text-brand-600" />
        <div>
          <h2 className="text-sm font-semibold text-slate-900">Two-factor verification</h2>
          <p className="mt-1 text-xs text-slate-500">
            {useRecovery
              ? 'Enter one of the recovery codes you saved when you turned on two-factor. Each works once.'
              : 'Enter the 6-digit code from your authenticator app.'}
          </p>
        </div>
      </div>

      {useRecovery ? (
        <div className="space-y-1">
          <label htmlFor="recovery-code" className="block text-xs font-medium text-slate-700">
            Recovery code
          </label>
          <input
            id="recovery-code"
            name="recovery-code"
            type="text"
            autoComplete="off"
            autoCapitalize="off"
            spellCheck={false}
            maxLength={24}
            autoFocus
            required
            placeholder="xxxx-xxxx-xxxx-xxxx"
            value={recoveryCode}
            onChange={(event) => setRecoveryCode(event.target.value)}
            className="w-full rounded-md border border-surface-border px-3 py-2 text-center font-mono tracking-wider outline-none focus:border-brand-500"
          />
        </div>
      ) : (
        <div className="space-y-1">
          <label htmlFor="code" className="block text-xs font-medium text-slate-700">
            Verification code
          </label>
          <input
            id="code"
            name="code"
            type="text"
            inputMode="numeric"
            autoComplete="one-time-code"
            pattern="[0-9]{6}"
            maxLength={6}
            autoFocus
            required
            value={code}
            onChange={(event) => setCode(event.target.value.replace(/\D/g, ''))}
            className="w-full rounded-md border border-surface-border px-3 py-2 text-center font-mono text-lg tracking-[0.4em] outline-none focus:border-brand-500"
          />
        </div>
      )}

      {verify.isError && (
        <Alert tone="danger">{errorMessage(verify.error, 'Verification failed.')}</Alert>
      )}

      <Button
        type="submit"
        variant="primary"
        loading={verify.isPending}
        disabled={useRecovery ? !recoveryReady : code.length !== 6}
        className="w-full"
      >
        {verify.isPending ? 'Verifying…' : 'Verify'}
      </Button>

      <Button variant="ghost" onClick={switchMethod} className="w-full">
        {useRecovery ? 'Use your authenticator app' : 'Use a recovery code'}
      </Button>

      <Button variant="ghost" onClick={() => setMfaToken(null)} className="w-full">
        Back to sign in
      </Button>
    </form>
  );
}
