import { request } from '@/services/apiClient';
import type { SSHApplyResult, SSHChange, SSHKey, SSHKeyList, SSHStatus } from '@/types/api';

/** SSH server calls. */
export const sshApi = {
  status: (signal?: AbortSignal) =>
    request<SSHStatus>('/security/ssh', signal ? { signal } : {}),

  configure: (change: SSHChange) =>
    request<SSHApplyResult>('/security/ssh', { method: 'PATCH', body: change }),

  keys: (account: string, signal?: AbortSignal) =>
    request<SSHKeyList>(
      `/security/ssh/keys?account=${encodeURIComponent(account)}`,
      signal ? { signal } : {},
    ),

  addKey: (account: string, key: string) =>
    request<SSHKey>('/security/ssh/keys', { method: 'POST', body: { account, key } }),

  removeKey: (account: string, fingerprint: string) =>
    request<SSHKey>(
      // The fingerprint contains a "+" and a "/", which are path-legal and
      // query-ambiguous, so it is encoded rather than interpolated.
      `/security/ssh/keys/${encodeURIComponent(fingerprint)}?account=${encodeURIComponent(account)}`,
      { method: 'DELETE' },
    ),
};
