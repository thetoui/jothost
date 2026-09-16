import { useState } from 'react';
import { Download } from 'lucide-react';

import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { SkeletonRows } from '@/components/ui/Loading';
import { Modal } from '@/components/ui/Modal';
import { Tabs } from '@/components/ui/Tabs';
import { websiteLogsApi } from '@/features/logs/api';
import { useWebsiteLogTail, useWebsiteLogs } from '@/features/logs/hooks';
import { ApiError } from '@/services/apiClient';
import type { Website } from '@/types/api';

/** The two logs a site has. */
const kinds = [
  { id: 'access', label: 'Access' },
  { id: 'error', label: 'Errors' },
] as const;

type Kind = (typeof kinds)[number]['id'];

/**
 * SiteLogsDialog shows one website's own logs.
 *
 * On the site, not on the Logs page. The host-wide log viewer needs
 * server.view — the permission for reading the whole machine, every site's
 * traffic and the authentication log with it — and somebody who looks after
 * one website should be able to read that website's logs without being handed
 * all of that.
 */
export function SiteLogsDialog({
  site,
  open,
  onClose,
}: {
  site: Website;
  open: boolean;
  onClose: () => void;
}) {
  const [kind, setKind] = useState<Kind>('access');
  const available = useWebsiteLogs(site.id, open);
  const tail = useWebsiteLogTail(site.id, kind, { limit: 200 }, open);

  const present = available.data?.logs.find((log) => log.key.endsWith(`.${kind}`));
  const lines = tail.data?.lines ?? [];

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={`Logs for ${site.primary_domain}`}
      description="This site's own requests and errors, read from its log directory beside the site."
      size="lg"
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            Close
          </Button>
          {/* A plain link: the browser saves it, and the endpoint sends it as
              an attachment with nosniff. A log line is attacker-influenced
              content, so it must never render on the panel's origin. */}
          <Button
            variant="primary"
            icon={<Download aria-hidden="true" className="h-4 w-4" />}
            disabled={!present?.present}
            onClick={() => {
              window.location.href = websiteLogsApi.downloadUrl(site.id, kind);
            }}
          >
            Download
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <Tabs
          items={kinds.map((entry) => ({ value: entry.id, label: entry.label }))}
          value={kind}
          onChange={setKind}
          label="Which log to show"
        />

        {tail.error instanceof ApiError && (
          <Alert tone="danger" title="The log could not be read">
            {tail.error.message}
          </Alert>
        )}

        {available.isPending || tail.isPending ? (
          <SkeletonRows rows={6} />
        ) : present && !present.present ? (
          <p className="text-sm text-ink">
            This site has no {kind} log yet. One appears the first time the web server writes to
            it.
          </p>
        ) : lines.length === 0 ? (
          <p className="text-sm text-ink">Nothing in it yet.</p>
        ) : (
          <div className="max-h-96 overflow-auto rounded-md border border-surface-border bg-console">
            {/* Rendered as text, never as markup. Every line here was written
                by whoever made the request it describes. */}
            <pre className="p-3 font-mono text-xs leading-relaxed text-console">
              {lines.map((line) => line.text).join('\n')}
            </pre>
          </div>
        )}

        {present?.path && (
          <p className="font-mono text-xs text-ink-muted">{present.path}</p>
        )}
      </div>
    </Modal>
  );
}
