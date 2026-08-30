import { useMemo, useState } from 'react';

import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { SelectField, TextField } from '@/components/ui/Field';
import { useCreateNodeApp, useNodeApps, useNodeVersions } from '@/features/node/hooks';
import { useWebsites } from '@/features/websites/hooks';

interface CreateAppFormProps {
  onCreated: () => void;
  onCancel: () => void;
  /** Pre-selects a website when opened from that site's page. */
  websiteId?: string;
}

/** The port bounds the API and the Agent both enforce. */
const MIN_PORT = 1024;
const EPHEMERAL_FLOOR = 32768;

/** The first port the panel suggests, and the step it walks by. */
const FIRST_PORT = 3000;

export function CreateAppForm({ onCreated, onCancel, websiteId }: CreateAppFormProps) {
  const websites = useWebsites();
  const versions = useNodeVersions();
  const existing = useNodeApps();
  const create = useCreateNodeApp();

  // A site that already runs an application, or serves PHP, cannot take one.
  // Filtering here means the form never offers a choice the API would refuse.
  const taken = useMemo(
    () => new Set((existing.data?.applications ?? []).map((app) => app.website_id)),
    [existing.data],
  );
  const usedPorts = useMemo(
    () => new Set((existing.data?.applications ?? []).map((app) => app.port)),
    [existing.data],
  );

  const available = useMemo(
    () =>
      (websites.data?.websites ?? []).filter(
        (site) => !taken.has(site.id) && !site.php_version && site.status === 'active',
      ),
    [websites.data, taken],
  );

  const suggestedPort = useMemo(() => {
    let port = FIRST_PORT;
    while (usedPorts.has(port) && port < EPHEMERAL_FLOOR) {
      port += 1;
    }
    return port;
  }, [usedPorts]);

  const [site, setSite] = useState(websiteId ?? '');
  const [startup, setStartup] = useState('server.js');
  const [port, setPort] = useState(String(suggestedPort));
  const [touched, setTouched] = useState(false);

  const portError = validatePort(port, usedPorts);
  const startupError = validateStartup(startup);

  if (versions.data && !versions.data.available) {
    return (
      <div className="space-y-3">
        <Alert tone="warning" title="No Node.js runtime is installed">
          Install one before adding an application. The panel can install it from this host&rsquo;s
          package manager.
        </Alert>
        <div className="flex justify-end">
          <Button onClick={onCancel}>Close</Button>
        </div>
      </div>
    );
  }

  if (available.length === 0 && !websites.isPending) {
    return (
      <div className="space-y-3">
        <Alert tone="info" title="No website can take an application">
          {/* Saying which of the three reasons applies is more use than a bare
              "none available". */}
          A website can run one application, must not also serve PHP, and has to have finished
          being created. None of yours currently qualifies.
        </Alert>
        <div className="flex justify-end">
          <Button onClick={onCancel}>Close</Button>
        </div>
      </div>
    );
  }

  return (
    <form
      className="space-y-4"
      onSubmit={(event) => {
        event.preventDefault();
        setTouched(true);
        if (portError || startupError || !site) {
          return;
        }
        create.mutate(
          {
            website_id: site,
            startup_file: startup.trim(),
            port: Number(port),
          },
          { onSuccess: onCreated },
        );
      }}
    >
      {create.isError && (
        <Alert tone="danger" title="The application could not be created">
          {create.error instanceof Error ? create.error.message : 'Try again in a moment.'}
        </Alert>
      )}

      <SelectField
        id="node-website"
        label="Website"
        value={site}
        onChange={(event) => setSite(event.target.value)}
        error={touched && !site ? 'Choose the website this application serves.' : null}
        hint="The application runs under this site's account, and nginx sends the domain to it."
      >
        <option value="">Choose a website</option>
        {available.map((website) => (
          <option key={website.id} value={website.id}>
            {website.primary_domain}
          </option>
        ))}
      </SelectField>

      <TextField
        id="node-startup"
        label="Startup file"
        value={startup}
        onChange={(event) => setStartup(event.target.value)}
        placeholder="server.js"
        autoComplete="off"
        spellCheck={false}
        error={touched ? startupError : null}
        hint="Relative to the site's document root — the file that calls listen()."
      />

      <TextField
        id="node-port"
        label="Port"
        value={port}
        onChange={(event) => setPort(event.target.value)}
        inputMode="numeric"
        autoComplete="off"
        error={touched ? portError : null}
        hint={`The application listens here on 127.0.0.1 and nginx proxies to it. Nothing outside the server reaches it directly.`}
      />

      <div className="flex justify-end gap-2 border-t border-surface-border pt-3">
        <Button type="button" onClick={onCancel} disabled={create.isPending}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" loading={create.isPending}>
          Create application
        </Button>
      </div>
    </form>
  );
}

/** validatePort applies the same rule the server does, with its reasons. */
function validatePort(raw: string, used: Set<number>): string | null {
  const value = Number(raw.trim());
  if (!raw.trim() || !Number.isInteger(value)) {
    return 'Enter a port number.';
  }
  if (value < MIN_PORT) {
    return `Ports below ${MIN_PORT} need root, and an application never runs as root.`;
  }
  if (value >= EPHEMERAL_FLOOR) {
    return `Ports from ${EPHEMERAL_FLOOR} up are handed out by the kernel to outgoing connections, so this one will eventually be taken already.`;
  }
  if (used.has(value)) {
    return 'Another application is already using this port.';
  }
  return null;
}

/** validateStartup applies the same rule the server does. */
function validateStartup(raw: string): string | null {
  const value = raw.trim();
  if (!value) {
    return 'A startup file is required.';
  }
  if (value.startsWith('/')) {
    return 'The path is relative to the document root.';
  }
  if (value.split('/').some((segment) => segment === '..' || segment === '.')) {
    return 'The path must stay inside the application directory.';
  }
  if (!/^[A-Za-z0-9._-]+(\/[A-Za-z0-9._-]+)*$/.test(value)) {
    return 'Only letters, digits, dots, dashes, underscores, and slashes.';
  }
  return null;
}
