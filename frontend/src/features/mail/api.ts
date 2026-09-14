import { request } from '@/services/apiClient';
import type {
  JobAccepted,
  Mailbox,
  MailAlias,
  MailAliasInput,
  MailAutoresponder,
  MailAutoresponderInput,
  MailDomain,
  MailDomainInput,
  MailOverview,
  MailSettings,
  MailboxInput,
} from '@/types/api';

/** Mail calls. */
export const mailApi = {
  overview: (signal?: AbortSignal) =>
    request<MailOverview>('/mail', signal ? { signal } : {}),

  saveSettings: (body: Partial<MailSettings>) =>
    request<MailSettings>('/mail/settings', { method: 'PUT', body }),

  /**
   * Queues installation. Returns the job, not the finished server.
   *
   * The packages take minutes and the virus scanner's signature database is
   * several hundred megabytes, so this cannot be the request that waits for
   * it: the API closes a connection long before either finishes.
   */
  install: (body: { filtering: boolean; antivirus: boolean }) =>
    request<JobAccepted>('/mail/install', { method: 'POST', body }),

  createDomain: (body: MailDomainInput) =>
    request<MailDomain>('/mail/domains', { method: 'POST', body }),

  updateDomain: (id: string, body: MailDomainInput) =>
    request<MailDomain>(`/mail/domains/${id}`, { method: 'PATCH', body }),

  removeDomain: (id: string) =>
    request<void>(`/mail/domains/${id}`, { method: 'DELETE' }),

  /**
   * A new signing key.
   *
   * The private half never comes back — it is written on the host that signs
   * with it and stays there. What returns is the public half, which the panel
   * publishes and then compares against what DNS is really serving.
   */
  rotateDKIM: (id: string) =>
    request<MailDomain>(`/mail/domains/${id}/dkim`, { method: 'POST' }),

  mailboxes: (domainID: string, signal?: AbortSignal) =>
    request<{ mailboxes: Mailbox[] }>(
      `/mail/domains/${domainID}/mailboxes`,
      signal ? { signal } : {},
    ),

  createMailbox: (domainID: string, body: MailboxInput) =>
    request<Mailbox>(`/mail/domains/${domainID}/mailboxes`, { method: 'POST', body }),

  updateMailbox: (id: string, body: MailboxInput) =>
    request<Mailbox>(`/mail/mailboxes/${id}`, { method: 'PATCH', body }),

  removeMailbox: (id: string) =>
    request<void>(`/mail/mailboxes/${id}`, { method: 'DELETE' }),

  setPassword: (id: string, password: string) =>
    request<void>(`/mail/mailboxes/${id}/password`, {
      method: 'PUT',
      body: { password },
    }),

  setAutoresponder: (id: string, body: MailAutoresponderInput) =>
    request<MailAutoresponder>(`/mail/mailboxes/${id}/autoresponder`, {
      method: 'PUT',
      body,
    }),

  clearAutoresponder: (id: string) =>
    request<void>(`/mail/mailboxes/${id}/autoresponder`, { method: 'DELETE' }),

  aliases: (domainID: string, signal?: AbortSignal) =>
    request<{ aliases: MailAlias[] }>(
      `/mail/domains/${domainID}/aliases`,
      signal ? { signal } : {},
    ),

  createAlias: (domainID: string, body: MailAliasInput) =>
    request<MailAlias>(`/mail/domains/${domainID}/aliases`, { method: 'POST', body }),

  removeAlias: (id: string) => request<void>(`/mail/aliases/${id}`, { method: 'DELETE' }),

  installWebmail: (websiteID: string) =>
    request<unknown>('/mail/webmail', { method: 'POST', body: { website_id: websiteID } }),

  removeWebmail: () => request<void>('/mail/webmail', { method: 'DELETE' }),
};
