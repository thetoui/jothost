import { useState } from 'react';
import { KeyRound, Network, Plus } from 'lucide-react';

import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { SkeletonRows } from '@/components/ui/Loading';
import { focusRingTight } from '@/components/ui/focus';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import { ZoneEditor } from '@/features/dns/components/ZoneEditor';
import { useCreateDNSZone, useDNSOverview, useWebsiteDNSZones } from '@/features/dns/hooks';
import { ApiError } from '@/services/apiClient';

interface WebsiteDNSPanelProps {
  websiteId: string;
  domain: string;
}

/**
 * WebsiteDNSPanel is the site's own DNS.
 *
 * A website does not need this panel's DNS to work — it is reached through
 * whatever already resolves its name — so the panel says what is true rather
 * than implying a zone is required: either this host answers for the domain, or
 * it does not, and creating one is offered rather than assumed.
 */
export function WebsiteDNSPanel({ websiteId, domain }: WebsiteDNSPanelProps) {
  const overview = useDNSOverview();
  const available = overview.data?.available ?? false;
  const { data, isPending } = useWebsiteDNSZones(websiteId, available);
  const create = useCreateDNSZone();
  const [open, setOpen] = useState<string | null>(null);

  const zones = data?.zones ?? [];
  const failure = create.error instanceof ApiError ? create.error.message : null;
  const noNameservers = (overview.data?.settings.default_ns ?? []).length === 0;

  return (
    <Card>
      <CardHeader
        title="DNS"
        description="What this host tells the internet about this domain."
        icon={<TintedIcon tone="brand" icon={<Network className="h-4 w-4" />} />}
        action={
          available && zones.length === 0 ? (
            <RequirePermission permission={Permission.DNSManage}>
              <Button
                variant="ghost"
                loading={create.isPending}
                onClick={() => create.mutate({ name: domain, website_id: websiteId })}
                icon={<Plus aria-hidden="true" className="h-4 w-4" />}
              >
                Add zone
              </Button>
            </RequirePermission>
          ) : null
        }
      />
      <CardBody>
        {!available ? (
          <p className="text-sm text-slate-500">
            This host has no name server, so DNS for {domain} is served somewhere else.
          </p>
        ) : isPending ? (
          <SkeletonRows rows={2} />
        ) : zones.length === 0 ? (
          <div className="space-y-3">
            <p className="text-sm text-slate-500">
              This host does not answer for {domain}. It does not have to — the site is reached
              through whatever already resolves the name — but a zone here is what lets the panel
              publish records for it.
            </p>
            {noNameservers && (
              <Alert tone="warning" title="No default name servers are set">
                A zone needs name servers to be delegated to, and the panel will not invent them.
                Set them on the DNS page first.
              </Alert>
            )}
            {failure && (
              <Alert tone="danger" title="The zone could not be created">
                {failure}
              </Alert>
            )}
          </div>
        ) : (
          <ul className="space-y-2">
            {zones.map((zone) => (
              <li key={zone.id}>
                <button
                  type="button"
                  onClick={() => setOpen(zone.id)}
                  className={`w-full rounded border border-surface-border px-3 py-2 text-left transition-colors hover:border-surface-strong ${focusRingTight}`}
                >
                  <span className="flex items-center gap-2 font-medium text-slate-900">
                    {zone.name}
                    {zone.dnssec && (
                      <span className="inline-flex items-center gap-1 rounded bg-ok-50 px-1.5 py-0.5 text-xs font-normal text-ok-700">
                        <KeyRound aria-hidden="true" className="h-3 w-3" />
                        Signed
                      </span>
                    )}
                  </span>
                  <span className="mt-0.5 block text-xs text-slate-500">
                    {zone.record_count} {zone.record_count === 1 ? 'record' : 'records'} · serial{' '}
                    {zone.serial}
                  </span>
                </button>
              </li>
            ))}
          </ul>
        )}
      </CardBody>

      {open && <ZoneEditor zoneId={open} onClose={() => setOpen(null)} />}
    </Card>
  );
}
