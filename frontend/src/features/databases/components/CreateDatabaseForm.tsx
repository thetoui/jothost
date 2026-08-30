import { useMemo, useState } from 'react';

import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { SelectField, TextField, Toggle } from '@/components/ui/Field';
import { useCreateDatabase, useDatabaseEngines } from '@/features/databases/hooks';
import { engineLabel, engineVersion } from '@/features/databases/status';
import { useWebsites } from '@/features/websites/hooks';
import type { DatabaseCreated, DatabaseEngineName } from '@/types/api';

interface CreateDatabaseFormProps {
  onCreated: (result: DatabaseCreated) => void;
  onCancel: () => void;
  /** Pre-selects a website when the form is opened from that site's page. */
  websiteId?: string;
}

/** The rule the API and the Agent both enforce, checked here so a typo is
 *  caught before a round trip rather than after one. */
const NAME_PATTERN = /^[a-z][a-z0-9_]*$/;
const MAX_NAME_LENGTH = 63;
const MAX_USER_LENGTH = 32;

export function CreateDatabaseForm({ onCreated, onCancel, websiteId }: CreateDatabaseFormProps) {
  const engines = useDatabaseEngines();
  const websites = useWebsites();
  const create = useCreateDatabase();

  const available = useMemo(
    () => (engines.data?.engines ?? []).filter((engine) => engine.available),
    [engines.data],
  );

  const [name, setName] = useState('');
  const [engine, setEngine] = useState<DatabaseEngineName | ''>('');
  const [site, setSite] = useState(websiteId ?? '');
  const [createUser, setCreateUser] = useState(true);
  const [username, setUsername] = useState('');
  const [host, setHost] = useState('localhost');
  const [touched, setTouched] = useState(false);

  const selected = available.find((item) => item.engine === engine) ?? available[0];
  const usesHostPatterns = selected?.supports_host_patterns ?? false;

  const nameError = validateIdentifier(name, MAX_NAME_LENGTH, 'database name');
  const userError = createUser && username ? validateIdentifier(username, MAX_USER_LENGTH, 'user name') : null;

  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    setTouched(true);
    if (nameError || userError) {
      return;
    }

    create.mutate(
      {
        name: name.trim().toLowerCase(),
        ...(engine ? { engine } : {}),
        ...(site ? { website_id: site } : {}),
        create_user: createUser,
        ...(createUser && username ? { username: username.trim().toLowerCase() } : {}),
        ...(createUser && usesHostPatterns ? { host } : {}),
      },
      { onSuccess: onCreated },
    );
  };

  if (engines.isPending) {
    return <p className="text-sm text-slate-500">Checking what this server runs…</p>;
  }

  if (available.length === 0) {
    return (
      <div className="space-y-3">
        <Alert tone="warning" title="No database server is available">
          {engines.data?.engines.find((item) => item.detail)?.detail ??
            'This host runs no database server the panel can manage. Install MariaDB, MySQL, or PostgreSQL and restart the agent.'}
        </Alert>
        <div className="flex justify-end">
          <Button onClick={onCancel}>Close</Button>
        </div>
      </div>
    );
  }

  return (
    <form onSubmit={submit} className="space-y-4">
      {create.isError && (
        <Alert tone="danger" title="The database could not be created">
          {create.error instanceof Error ? create.error.message : 'Try again in a moment.'}
        </Alert>
      )}

      <TextField
        id="database-name"
        label="Database name"
        value={name}
        onChange={(event) => setName(event.target.value)}
        placeholder="shop"
        autoComplete="off"
        spellCheck={false}
        error={touched ? nameError : null}
        hint="Lowercase letters, digits, and underscores. It must start with a letter."
      />

      {/* Only asked when there is a choice to make. A host running one server
          should not make anybody name it. */}
      {available.length > 1 && (
        <SelectField
          id="database-engine"
          label="Engine"
          value={engine}
          onChange={(event) => setEngine(event.target.value as DatabaseEngineName | '')}
        >
          {available.map((item) => (
            <option key={item.engine} value={item.engine}>
              {engineLabel(item.engine)}
              {item.version ? ` (${engineVersion(item.version)})` : ''}
            </option>
          ))}
        </SelectField>
      )}

      <SelectField
        id="database-website"
        label="Website"
        value={site}
        onChange={(event) => setSite(event.target.value)}
        hint="Optional. Linking a database to a site shows it on that site's page. Deleting the site never deletes the database."
      >
        <option value="">Not linked to a website</option>
        {(websites.data?.websites ?? []).map((website) => (
          <option key={website.id} value={website.id}>
            {website.primary_domain}
          </option>
        ))}
      </SelectField>

      <Toggle
        id="database-create-user"
        label="Create a user for this database"
        description="A database with no account cannot be reached by anything. The password is generated on the server and shown once."
        checked={createUser}
        onChange={setCreateUser}
      />

      {createUser && (
        <div className="space-y-4 rounded-md border border-surface-border bg-surface-sunken/40 p-3">
          <TextField
            id="database-username"
            label="User name"
            value={username}
            onChange={(event) => setUsername(event.target.value)}
            placeholder={name || 'shop'}
            autoComplete="off"
            spellCheck={false}
            suffix="optional"
            error={touched ? userError : null}
            hint="Defaults to the database name."
          />

          {usesHostPatterns ? (
            <SelectField
              id="database-host"
              label="Connect from"
              value={host}
              onChange={(event) => setHost(event.target.value)}
              hint="Only this server, unless something outside it needs to connect."
            >
              <option value="localhost">This server only (localhost)</option>
              <option value="%">Any host (%)</option>
            </SelectField>
          ) : (
            <p className="text-xs text-slate-500">
              PostgreSQL roles are global; where they may connect from is decided by the
              server&rsquo;s own configuration, not by the panel.
            </p>
          )}
        </div>
      )}

      <div className="flex justify-end gap-2 border-t border-surface-border pt-3">
        <Button type="button" onClick={onCancel} disabled={create.isPending}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" loading={create.isPending}>
          Create database
        </Button>
      </div>
    </form>
  );
}

/** validateIdentifier applies the same rule the server does. */
function validateIdentifier(value: string, max: number, what: string): string | null {
  const trimmed = value.trim().toLowerCase();
  if (!trimmed) {
    return `A ${what} is required.`;
  }
  if (trimmed.length > max) {
    return `A ${what} must be at most ${max} characters.`;
  }
  if (!NAME_PATTERN.test(trimmed)) {
    return `A ${what} must start with a letter and contain only lowercase letters, digits, and underscores.`;
  }
  return null;
}
