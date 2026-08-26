import { useState, type FormEvent } from 'react';

import { useCreateWebsite } from '@/features/websites/hooks';
import { domainError, normalizeDomain } from '@/features/websites/status';
import { ApiError } from '@/services/apiClient';

interface CreateWebsiteFormProps {
  onCreated?: (websiteId: string) => void;
  onCancel?: () => void;
}

/** CreateWebsiteForm queues a new website. */
export function CreateWebsiteForm({ onCreated, onCancel }: CreateWebsiteFormProps) {
  const [domain, setDomain] = useState('');
  const [name, setName] = useState('');
  const [validationError, setValidationError] = useState<string | null>(null);

  const createWebsite = useCreateWebsite();

  function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();

    const problem = domainError(domain);
    if (problem) {
      setValidationError(problem);
      return;
    }
    setValidationError(null);

    createWebsite.mutate(
      { domain: normalizeDomain(domain), name: name.trim() },
      {
        onSuccess: (result) => {
          setDomain('');
          setName('');
          onCreated?.(result.website.id);
        },
      },
    );
  }

  // The server's message is shown as-is: it distinguishes a taken domain from
  // an invalid one, which a generic "could not create" would hide.
  const serverError =
    createWebsite.error instanceof ApiError
      ? createWebsite.error.message
      : createWebsite.error
        ? 'The website could not be created.'
        : null;

  const error = validationError ?? serverError;

  return (
    <form onSubmit={handleSubmit} noValidate className="space-y-4">
      <div>
        <label htmlFor="website-domain" className="block text-sm font-medium text-slate-700">
          Domain
        </label>
        <input
          id="website-domain"
          name="domain"
          type="text"
          autoComplete="off"
          spellCheck={false}
          placeholder="example.com"
          value={domain}
          onChange={(event) => setDomain(event.target.value)}
          aria-invalid={error ? true : undefined}
          aria-describedby={error ? 'website-form-error' : 'website-domain-hint'}
          className="mt-1 w-full rounded-md border border-surface-border px-3 py-2 text-sm shadow-sm focus:border-brand-500 focus:outline-none focus:ring-1 focus:ring-brand-500"
        />
        <p id="website-domain-hint" className="mt-1 text-xs text-slate-500">
          The site is served at this name. Files live in /var/www/&lt;domain&gt;/public.
        </p>
      </div>

      <div>
        <label htmlFor="website-name" className="block text-sm font-medium text-slate-700">
          Display name <span className="font-normal text-slate-500">(optional)</span>
        </label>
        <input
          id="website-name"
          name="name"
          type="text"
          autoComplete="off"
          value={name}
          onChange={(event) => setName(event.target.value)}
          className="mt-1 w-full rounded-md border border-surface-border px-3 py-2 text-sm shadow-sm focus:border-brand-500 focus:outline-none focus:ring-1 focus:ring-brand-500"
        />
      </div>

      {error && (
        <p id="website-form-error" role="alert" className="text-sm text-rose-600">
          {error}
        </p>
      )}

      <p className="text-xs text-slate-500">
        HTTPS is not available yet. New sites are served over HTTP until certificate
        management arrives.
      </p>

      <div className="flex items-center gap-2">
        <button
          type="submit"
          disabled={createWebsite.isPending}
          className="rounded-md bg-brand-600 px-3 py-2 text-sm font-medium text-white shadow-sm hover:bg-brand-700 disabled:cursor-not-allowed disabled:opacity-60"
        >
          {createWebsite.isPending ? 'Creating…' : 'Create website'}
        </button>
        {onCancel && (
          <button
            type="button"
            onClick={onCancel}
            className="rounded-md px-3 py-2 text-sm font-medium text-slate-600 hover:bg-surface-muted"
          >
            Cancel
          </button>
        )}
      </div>
    </form>
  );
}
