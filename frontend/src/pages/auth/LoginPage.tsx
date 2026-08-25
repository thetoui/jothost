import { useState, type FormEvent } from 'react';
import { Navigate, useLocation } from 'react-router-dom';
import { Loader2, ShieldCheck } from 'lucide-react';

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
    <div className="flex min-h-full items-center justify-center bg-surface-muted p-6">
      <div className="w-full max-w-sm">
        <div className="mb-8 flex flex-col items-center gap-3">
          <span className="grid h-12 w-12 place-items-center rounded-xl bg-brand-600 text-lg font-bold text-white">
            J
          </span>
          <h1 className="text-lg font-semibold text-slate-900">JotHost Panel</h1>
        </div>

        <div className="rounded-lg border border-surface-border bg-surface p-6 shadow-sm">
          {mfaToken ? <TwoFactorStep /> : <PasswordStep />}
        </div>
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
        <h2 className="text-sm font-semibold text-slate-900">Sign in</h2>
        <p className="mt-1 text-xs text-slate-500">Use your panel administrator account.</p>
      </div>

      <div className="space-y-1">
        <label htmlFor="username" className="block text-xs font-medium text-slate-700">
          Username
        </label>
        <input
          id="username"
          name="username"
          type="text"
          autoComplete="username"
          autoFocus
          required
          value={username}
          onChange={(event) => setUsername(event.target.value)}
          className="w-full rounded-md border border-surface-border px-3 py-2 text-sm outline-none focus:border-brand-500"
        />
      </div>

      <div className="space-y-1">
        <label htmlFor="password" className="block text-xs font-medium text-slate-700">
          Password
        </label>
        <input
          id="password"
          name="password"
          type="password"
          autoComplete="current-password"
          required
          value={password}
          onChange={(event) => setPassword(event.target.value)}
          className="w-full rounded-md border border-surface-border px-3 py-2 text-sm outline-none focus:border-brand-500"
        />
      </div>

      {login.isError && (
        <p role="alert" className="text-sm text-rose-700">
          {errorMessage(login.error, 'Sign in failed.')}
        </p>
      )}

      <button
        type="submit"
        disabled={login.isPending}
        className="flex w-full items-center justify-center gap-2 rounded-md bg-brand-600 px-4 py-2 text-sm font-medium text-white hover:bg-brand-700 disabled:opacity-60"
      >
        {login.isPending && <Loader2 aria-hidden="true" className="h-4 w-4 animate-spin" />}
        Sign in
      </button>
    </form>
  );
}

function TwoFactorStep() {
  const [code, setCode] = useState('');
  const verify = useVerifyTwoFactor();
  const setMfaToken = useAuthStore((state) => state.setMfaToken);

  function handleSubmit(event: FormEvent) {
    event.preventDefault();
    verify.mutate(code);
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      <div className="flex items-start gap-3">
        <ShieldCheck aria-hidden="true" className="mt-0.5 h-5 w-5 shrink-0 text-brand-600" />
        <div>
          <h2 className="text-sm font-semibold text-slate-900">Two-factor verification</h2>
          <p className="mt-1 text-xs text-slate-500">
            Enter the 6-digit code from your authenticator app.
          </p>
        </div>
      </div>

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

      {verify.isError && (
        <p role="alert" className="text-sm text-rose-700">
          {errorMessage(verify.error, 'Verification failed.')}
        </p>
      )}

      <button
        type="submit"
        disabled={verify.isPending || code.length !== 6}
        className="flex w-full items-center justify-center gap-2 rounded-md bg-brand-600 px-4 py-2 text-sm font-medium text-white hover:bg-brand-700 disabled:opacity-60"
      >
        {verify.isPending && <Loader2 aria-hidden="true" className="h-4 w-4 animate-spin" />}
        Verify
      </button>

      <button
        type="button"
        onClick={() => setMfaToken(null)}
        className="w-full text-xs text-slate-500 hover:text-slate-800"
      >
        Back to sign in
      </button>
    </form>
  );
}
