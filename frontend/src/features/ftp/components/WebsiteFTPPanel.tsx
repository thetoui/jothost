import { useState } from 'react';
import { Copy, FolderKey, Plus } from 'lucide-react';

import { StatusPill } from '@/components/StatusPill';
import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { SelectField, TextField } from '@/components/ui/Field';
import { Modal } from '@/components/ui/Modal';
import { SkeletonRows } from '@/components/ui/Loading';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import { useCreateFTPUser, useFTPOverview, useWebsiteFTPUsers } from '@/features/ftp/hooks';
import { ApiError } from '@/services/apiClient';
import type { FTPUser } from '@/types/api';

interface WebsiteFTPPanelProps {
  websiteId: string;
  domain: string;
}

/**
 * WebsiteFTPPanel manages one website's FTP accounts.
 *
 * Accounts are created here rather than on the FTP page because an account
 * belongs to a site: it maps to that site's system account and is confined to
 * that site's directory. A form elsewhere would have to ask which website, which
 * is a question this page has already answered.
 */
export function WebsiteFTPPanel({ websiteId, domain }: WebsiteFTPPanelProps) {
  const overview = useFTPOverview();
  const { data, isPending } = useWebsiteFTPUsers(websiteId, overview.data?.available !== false);
  const [adding, setAdding] = useState(false);

  const users = data?.users ?? [];
  const available = overview.data?.available ?? false;

  return (
    <Card>
      <CardHeader
        title="FTP"
        description="Accounts that can upload to this website, without a login to the server."
        icon={<TintedIcon tone="brand" icon={<FolderKey className="h-4 w-4" />} />}
        action={
          available ? (
            <RequirePermission permission={Permission.FTPManage}>
              <Button
                variant="ghost"
                onClick={() => setAdding(true)}
                icon={<Plus aria-hidden="true" className="h-4 w-4" />}
              >
                Add account
              </Button>
            </RequirePermission>
          ) : undefined
        }
      />
      <CardBody className="p-0">
        {!available ? (
          <p className="px-5 py-4 text-sm text-slate-500">
            No FTP server is installed on this host.
          </p>
        ) : isPending ? (
          <SkeletonRows rows={2} />
        ) : users.length === 0 ? (
          <p className="px-5 py-4 text-sm text-slate-500">
            No FTP accounts for this website yet.
          </p>
        ) : (
          <ul className="divide-y divide-surface-border">
            {users.map((user) => (
              <li key={user.id} className="px-5 py-3">
                <AccountSummary user={user} />
              </li>
            ))}
          </ul>
        )}
      </CardBody>

      <AddAccountDialog
        open={adding}
        onClose={() => setAdding(false)}
        websiteId={websiteId}
        domain={domain}
      />
    </Card>
  );
}

function AccountSummary({ user }: { user: FTPUser }) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      <span className="font-medium text-slate-900">{user.username}</span>
      {user.suspended ? (
        <StatusPill label="Suspended" tone="warn" dot />
      ) : user.access_level === 'readonly' ? (
        <StatusPill label="Read-only" tone="neutral" dot />
      ) : (
        <StatusPill label="Full access" tone="ok" dot />
      )}
      <span className="ml-auto font-mono text-xs text-slate-400">{user.home}</span>
    </div>
  );
}

function AddAccountDialog({
  open,
  onClose,
  websiteId,
  domain,
}: {
  open: boolean;
  onClose: () => void;
  websiteId: string;
  domain: string;
}) {
  const create = useCreateFTPUser();
  const [username, setUsername] = useState('');
  const [subpath, setSubpath] = useState('');
  const [access, setAccess] = useState('full');
  const [quota, setQuota] = useState('0');
  const [password, setPassword] = useState<string | null>(null);

  const message = create.error instanceof ApiError ? create.error.message : null;

  function reset() {
    setUsername('');
    setSubpath('');
    setAccess('full');
    setQuota('0');
    setPassword(null);
    create.reset();
  }

  // The password is shown once and nothing stores it, so the dialog stays open
  // on success rather than closing over the one chance to read it.
  if (password) {
    return (
      <Modal
        open={open}
        onClose={() => {
          reset();
          onClose();
        }}
        title="FTP account created"
      >
        <div className="space-y-3">
          <Alert tone="warning" title="Copy this password now">
            Nothing stores it. Once this dialog is closed the only way to get a working
            password again is to set a new one.
          </Alert>
          <div className="flex items-center gap-2 rounded-lg border border-surface-border bg-surface-subtle p-3">
            <code className="flex-1 break-all font-mono text-sm">{password}</code>
            <Button
              variant="ghost"
              onClick={() => void navigator.clipboard?.writeText(password)}
              icon={<Copy aria-hidden="true" className="h-4 w-4" />}
            >
              Copy
            </Button>
          </div>
          <Button
            onClick={() => {
              reset();
              onClose();
            }}
          >
            Done
          </Button>
        </div>
      </Modal>
    );
  }

  return (
    <Modal open={open} onClose={onClose} title={`New FTP account for ${domain}`}>
      <form
        className="space-y-4"
        onSubmit={(event) => {
          event.preventDefault();
          create.mutate(
            {
              website_id: websiteId,
              username: username.trim(),
              home_subpath: subpath.trim(),
              access_level: access,
              quota_mb: Number(quota) || 0,
            },
            {
              onSuccess: (result) => {
                // A password only comes back when the panel generated one.
                setPassword(result.password ?? null);
                if (!result.password) {
                  reset();
                  onClose();
                }
              },
            },
          );
        }}
      >
        <TextField
          id="ftp-username"
          label="Account name"
          value={username}
          required
          onChange={(event) => setUsername(event.target.value)}
          hint="What the FTP client logs in with. It is not a login to the server itself."
        />

        <TextField
          id="ftp-subpath"
          label="Folder"
          value={subpath}
          onChange={(event) => setSubpath(event.target.value)}
          hint="Leave empty for the whole website. A folder here confines the account to it and nothing above it."
        />

        <SelectField
          id="ftp-access"
          label="Access"
          value={access}
          onChange={(event) => setAccess(event.target.value)}
        >
          <option value="full">Full — upload, change and delete</option>
          <option value="readonly">Read-only — list and download</option>
        </SelectField>

        <TextField
          id="ftp-quota"
          label="Disk limit (MB)"
          value={quota}
          inputMode="numeric"
          onChange={(event) => setQuota(event.target.value)}
          hint="0 for no limit. An upload that would exceed the limit is refused."
        />

        {message && (
          <p className="text-sm text-danger-600" role="alert">
            {message}
          </p>
        )}

        <div className="flex gap-2">
          <Button type="submit" disabled={create.isPending}>
            {create.isPending ? 'Creating…' : 'Create account'}
          </Button>
          <Button type="button" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
        </div>
      </form>
    </Modal>
  );
}
