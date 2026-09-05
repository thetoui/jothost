import { UserCog } from 'lucide-react';

import { Button } from '@/components/ui/Button';
import { useEndImpersonation, useImpersonation } from '@/features/tenancy/hooks';

/**
 * ImpersonationBanner says whose account this session is using.
 *
 * It is always visible rather than tucked into a menu, and that is the point:
 * the failure mode of impersonation is forgetting you are in it and then doing
 * something to the wrong account. It names both people, because "you are
 * acme_ltd" without saying who by is not enough to act on.
 */
export function ImpersonationBanner() {
  const { data } = useImpersonation();
  const end = useEndImpersonation();

  const record = data?.impersonation;
  if (!record) {
    return null;
  }

  return (
    <div
      role="status"
      className="flex flex-wrap items-center justify-between gap-3 border-b border-warn-600/20 bg-warn-50 px-4 py-2 text-sm text-warn-800"
    >
      <span className="flex items-center gap-2">
        <UserCog aria-hidden="true" className="h-4 w-4 shrink-0" />
        <span>
          Signed in as <strong>{record.subject_username}</strong>
          {record.actor_username ? (
            <>
              {' '}
              by <strong>{record.actor_username}</strong>
            </>
          ) : null}
          . Everything you do here is recorded against both accounts.
        </span>
      </span>
      <Button
        size="sm"
        variant="secondary"
        onClick={() => end.mutate()}
        disabled={end.isPending}
      >
        Stop
      </Button>
    </div>
  );
}
