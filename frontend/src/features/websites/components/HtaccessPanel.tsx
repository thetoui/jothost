import { FileCog } from 'lucide-react';

import { Alert } from '@/components/ui/Alert';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { Toggle } from '@/components/ui/Field';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import { useWebserver } from '@/features/webserver/hooks';
import { useSetAllowOverride } from '@/features/websites/hooks';
import { ApiError } from '@/services/apiClient';
import type { Website } from '@/types/api';

interface HtaccessPanelProps {
  site: Website;
}

/**
 * HtaccessPanel turns .htaccess on or off for one site.
 *
 * It only appears when the host runs Apache behind nginx. On an nginx-only
 * host the file is never read by anything, and a switch that changes nothing
 * is worse than no switch at all: someone would set it, deploy rewrites that
 * depend on it, and find out at the worst moment.
 */
export function HtaccessPanel({ site }: HtaccessPanelProps) {
  const { data: webserver } = useWebserver();
  const setAllow = useSetAllowOverride(site.id);

  if (webserver?.mode !== 'hybrid') {
    return null;
  }

  const error =
    setAllow.error instanceof ApiError
      ? setAllow.error.message
      : setAllow.error
        ? 'The setting could not be changed.'
        : null;

  return (
    <Card>
      <CardHeader
        title="Apache & .htaccess"
        description="This host serves sites through Apache, behind nginx."
        icon={<TintedIcon tone="brand" icon={<FileCog className="h-4 w-4" />} />}
      />
      <CardBody className="space-y-3">
        <RequirePermission permission={Permission.WebsiteUpdate}>
          <Toggle
            id={`allow-override-${site.id}`}
            label="Read .htaccess files"
            description="Apache applies the rewrite and access rules it finds in the site's directories. Turning it off is faster, because Apache stops looking for the file on every request."
            checked={site.allow_override}
            disabled={setAllow.isPending}
            onChange={(checked) => setAllow.mutate(checked)}
          />
        </RequirePermission>

        {site.apache_port !== null && (
          <p className="text-xs text-slate-500">
            nginx proxies this site to Apache on{' '}
            <span className="font-mono">127.0.0.1:{site.apache_port}</span>. The backend is
            reachable from this host only.
          </p>
        )}

        {error && (
          <Alert tone="danger" title="The setting could not be changed">
            {error}
          </Alert>
        )}
      </CardBody>
    </Card>
  );
}
