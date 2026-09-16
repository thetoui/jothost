import { BarChart3, ExternalLink } from 'lucide-react';

import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { LinkButton } from '@/components/ui/Link';
import { SkeletonRows } from '@/components/ui/Loading';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import { useGrafana, useInstallGrafana } from '@/features/monitoring/hooks';
import { ApiError } from '@/services/apiClient';

/**
 * The panels this page embeds, by their id in the dashboard the Agent writes.
 *
 * Ids rather than titles: Grafana addresses a panel by id, and a dashboard
 * whose titles were translated or edited would still embed correctly. They
 * match agent/internal/grafana/provision.go, which is the only other place
 * they appear.
 */
const panels = [
  { id: 1, title: 'CPU' },
  { id: 2, title: 'Memory' },
  { id: 3, title: 'Disk' },
  { id: 4, title: 'Load average' },
  { id: 5, title: 'Network' },
];

/**
 * GrafanaPanels embeds the host's charts.
 *
 * Grafana draws and this panel's own alert engine still decides: the rules,
 * thresholds and acknowledgements above are unaffected by whether Grafana is
 * installed, and the page works without it.
 *
 * What it cannot do is authenticate the reader. Grafana is served on its own
 * hostname and decides who its visitor is, exactly as phpMyAdmin does — so a
 * frame here shows a chart to somebody with a Grafana session and Grafana's
 * own login to everybody else. That is said on the page rather than left for
 * somebody to discover from an unexpected login box.
 */
export function GrafanaPanels() {
  const { data, isPending } = useGrafana();
  const install = useInstallGrafana();

  if (isPending) {
    return (
      <Card label="Charts">
        <CardBody>
          <SkeletonRows rows={3} />
        </CardBody>
      </Card>
    );
  }

  const state = data;
  if (!state) {
    return null;
  }

  // Three separate conditions, and the status says which one is missing. A
  // single "not ready" would leave the reader guessing between an install, a
  // provision and a start.
  const usable = state.installed && state.provisioned && state.running && Boolean(state.embed_base);

  if (!usable) {
    return (
      <Card label="Charts">
        <CardHeader
          icon={<TintedIcon icon={<BarChart3 className="h-4 w-4" />} />}
          title="Charts"
          description="Grafana draws the history behind these alerts. The alert engine does not need it."
        />
        <CardBody className="space-y-3">
          <p className="text-sm text-ink">
            {state.detail ?? 'Grafana is not available on this host.'}
          </p>

          {install.error instanceof ApiError && (
            <Alert tone="danger" title="Grafana could not be installed">
              {install.error.message}
            </Alert>
          )}

          {install.isSuccess && (
            <Alert tone="info" title="Installing">
              The download is several hundred megabytes and continues in the background. This
              card will show the charts once it finishes.
            </Alert>
          )}

          {state.can_install && !install.isSuccess && (
            <RequirePermission permission={Permission.MonitorManage}>
              <div className="space-y-2">
                <Button
                  variant="primary"
                  loading={install.isPending}
                  onClick={() => install.mutate()}
                >
                  Install Grafana
                </Button>
                {/* Said before the install, not after. Somebody who would
                    rather not run a second web application on the host should
                    find that out here. */}
                <p className="text-xs text-ink-muted">
                  Grafana is installed listening on loopback only, with a read-only database
                  role of its own, and it authenticates its own visitors — the panel does not
                  sign you in to it.
                </p>
              </div>
            </RequirePermission>
          )}
        </CardBody>
      </Card>
    );
  }

  const base = state.embed_base ?? '';
  const uid = state.dashboard_uid ?? 'jothost-host';

  return (
    <Card label="Charts">
      <CardHeader
        icon={<TintedIcon tone="brand" icon={<BarChart3 className="h-4 w-4" />} />}
        title="Charts"
        description="Drawn by Grafana from the same metrics the alert rules above are evaluated against."
        action={
          <LinkButton href={`${base}/d/${uid}`} size="sm" icon={<ExternalLink className="h-3.5 w-3.5" />}>
            Open in Grafana
          </LinkButton>
        }
      />
      <CardBody className="grid gap-4 xl:grid-cols-2">
        {panels.map((panel) => (
          <figure key={panel.id} className="min-w-0">
            <figcaption className="mb-1 text-xs font-medium uppercase tracking-wide text-ink-muted">
              {panel.title}
            </figcaption>
            <iframe
              // kiosk hides Grafana's own chrome, so the frame is the chart
              // rather than a small copy of Grafana inside the panel.
              src={`${base}/d-solo/${uid}?panelId=${panel.id}&from=now-6h&to=now&kiosk`}
              title={`${panel.title} over the last six hours`}
              className="h-56 w-full rounded-md border border-surface-border bg-surface"
              // The frame is another application on another origin. It gets
              // nothing from this one: no scripts reaching out, no forms, no
              // navigation of the page around it.
              sandbox="allow-scripts allow-same-origin"
              referrerPolicy="no-referrer"
              loading="lazy"
            />
          </figure>
        ))}
      </CardBody>
    </Card>
  );
}
