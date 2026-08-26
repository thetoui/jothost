import { useState, type FormEvent } from 'react';
import { Globe } from 'lucide-react';

import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { TextField } from '@/components/ui/Field';
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

  return (
    <form onSubmit={handleSubmit} noValidate className="space-y-4 pb-2">
      <TextField
        id="website-domain"
        label="Domain"
        type="text"
        autoComplete="off"
        spellCheck={false}
        placeholder="example.com"
        value={domain}
        onChange={(event) => setDomain(event.target.value)}
        error={validationError}
        hint="Files are served from /var/www/<domain>/public."
        adornment={<Globe className="h-4 w-4" />}
      />

      <TextField
        id="website-name"
        label="Display name"
        suffix="optional"
        type="text"
        autoComplete="off"
        value={name}
        onChange={(event) => setName(event.target.value)}
        hint="Shown in the panel only. The domain is what visitors use."
      />

      {serverError && <Alert tone="danger">{serverError}</Alert>}

      <Alert tone="info">
        HTTPS is not available yet, so new sites are served over HTTP until certificate
        management arrives.
      </Alert>

      <div className="flex items-center justify-end gap-2 border-t border-surface-border pt-4">
        {onCancel && (
          <Button variant="ghost" onClick={onCancel} disabled={createWebsite.isPending}>
            Cancel
          </Button>
        )}
        <Button type="submit" variant="primary" loading={createWebsite.isPending}>
          {createWebsite.isPending ? 'Creating…' : 'Create website'}
        </Button>
      </div>
    </form>
  );
}
