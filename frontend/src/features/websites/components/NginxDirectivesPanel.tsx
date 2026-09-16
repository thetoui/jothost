import { useState } from 'react';
import { FileCode2 } from 'lucide-react';

import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { focusRing } from '@/components/ui/focus';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import { useSetNginxDirectives } from '@/features/websites/hooks';
import { ApiError } from '@/services/apiClient';
import type { Website } from '@/types/api';

/** The same bound the API and the database enforce. */
const maxLength = 16 * 1024;

/**
 * NginxDirectivesPanel edits a site's additional nginx configuration.
 *
 * Behind server.manage rather than website.update, which is why it is a panel
 * of its own rather than another field on the settings form: writing nginx
 * configuration is server administration whoever's website it is attached to,
 * and a customer who could do it for their own site could serve any file the
 * web server can read.
 *
 * The panel is not shown at all to somebody without the permission — not shown
 * disabled. A greyed-out box of server configuration is an invitation to ask
 * for access to it.
 */
export function NginxDirectivesPanel({ site }: { site: Website }) {
  return (
    <RequirePermission permission={Permission.ServerManage}>
      <Editor site={site} />
    </RequirePermission>
  );
}

function Editor({ site }: { site: Website }) {
  const save = useSetNginxDirectives(site.id);
  const [value, setValue] = useState(site.nginx_directives ?? '');

  const dirty = value !== (site.nginx_directives ?? '');
  const tooLong = value.length > maxLength;

  const error =
    save.error instanceof ApiError
      ? save.error.message
      : save.error
        ? 'The directives could not be saved.'
        : null;

  return (
    <Card>
      <CardHeader
        icon={<TintedIcon tone="brand" icon={<FileCode2 className="h-4 w-4" />} />}
        title="Additional nginx directives"
        description="Written into this site's server block, after everything the panel generates."
      />
      <CardBody className="space-y-3">
        <label htmlFor={`directives-${site.id}`} className="sr-only">
          Additional nginx directives for {site.primary_domain}
        </label>
        <textarea
          id={`directives-${site.id}`}
          value={value}
          onChange={(event) => setValue(event.target.value)}
          disabled={save.isPending}
          rows={10}
          spellCheck={false}
          placeholder={'location /assets/ {\n    expires 30d;\n}'}
          className={`w-full rounded-md border border-surface-border bg-surface p-3 font-mono text-xs leading-relaxed text-ink-strong shadow-card placeholder:text-ink-dim disabled:bg-surface-muted ${focusRing}`}
        />

        {/* What the panel can and cannot promise, said once and plainly. It is
            the difference between an operator who knows this is nginx and one
            who assumes the panel is checking their configuration for them. */}
        <p className="text-xs text-ink-muted">
          Checked for structure — braces must balance, and{' '}
          <span className="font-mono">include</span> is refused because it points at a file the
          panel cannot show you. What the directives <em>do</em> is not checked: this is nginx
          configuration, and it can serve anything the web server can read. nginx validates the
          result before it is applied, and the previous configuration is restored if it is
          rejected.
        </p>

        {tooLong && (
          <Alert tone="danger" title="Too long">
            {value.length.toLocaleString()} characters, and the limit is{' '}
            {maxLength.toLocaleString()}.
          </Alert>
        )}

        {error && (
          <Alert tone="danger" title="The host refused these directives">
            {error}
          </Alert>
        )}

        {save.isSuccess && !dirty && (
          <Alert tone="success" title="Saved">
            The vhost is being rewritten. Watch the activity list for the result — nginx has the
            last word, and a configuration it rejects is rolled back.
          </Alert>
        )}

        <div className="flex items-center gap-2">
          <Button
            variant="primary"
            disabled={!dirty || tooLong}
            loading={save.isPending}
            onClick={() => save.mutate(value)}
          >
            Save and apply
          </Button>
          {dirty && (
            <Button
              variant="ghost"
              disabled={save.isPending}
              onClick={() => setValue(site.nginx_directives ?? '')}
            >
              Discard
            </Button>
          )}
        </div>
      </CardBody>
    </Card>
  );
}
