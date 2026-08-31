import { request } from '@/services/apiClient';
import type {
  Fail2BanApplyResult,
  Fail2BanBannedList,
  Fail2BanChange,
  Fail2BanStatus,
} from '@/types/api';

/** Intrusion prevention calls. */
export const fail2banApi = {
  status: (signal?: AbortSignal) =>
    request<Fail2BanStatus>('/security/fail2ban', signal ? { signal } : {}),

  install: () => request<Fail2BanStatus>('/security/fail2ban/install', { method: 'POST' }),

  banned: (signal?: AbortSignal) =>
    request<Fail2BanBannedList>('/security/fail2ban/banned', signal ? { signal } : {}),

  configure: (jail: string, change: Fail2BanChange) =>
    request<Fail2BanApplyResult>(`/security/fail2ban/jails/${encodeURIComponent(jail)}`, {
      method: 'PATCH',
      body: change,
    }),

  setIgnored: (ignored: string[]) =>
    request<{ ignored: string[]; count: number }>('/security/fail2ban/ignored', {
      method: 'PUT',
      body: { ignored },
    }),

  unban: (jail: string, address: string) =>
    request<{ jail: string; address: string; banned: boolean }>('/security/fail2ban/unban', {
      method: 'POST',
      body: { jail, address },
    }),

  ban: (jail: string, address: string) =>
    request<{ jail: string; address: string; banned: boolean }>('/security/fail2ban/ban', {
      method: 'POST',
      body: { jail, address },
    }),
};
