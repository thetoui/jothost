import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { mailApi } from '@/features/mail/api';
import type {
  MailAliasInput,
  MailAutoresponderInput,
  MailDomainInput,
  MailSettings,
  MailboxInput,
} from '@/types/api';

export const mailKeys = {
  all: ['mail'] as const,
  overview: () => [...mailKeys.all, 'overview'] as const,
  mailboxes: (domainID: string) => [...mailKeys.all, 'mailboxes', domainID] as const,
  aliases: (domainID: string) => [...mailKeys.all, 'aliases', domainID] as const,
};

/** useMailOverview reads the settings, the daemons, and what DNS publishes. */
export function useMailOverview() {
  return useQuery({
    queryKey: mailKeys.overview(),
    queryFn: ({ signal }) => mailApi.overview(signal),
    placeholderData: (previous) => previous,
  });
}

/** useMailboxes reads a domain's mailboxes and how full each one is. */
export function useMailboxes(domainID: string | undefined) {
  return useQuery({
    queryKey: mailKeys.mailboxes(domainID ?? ''),
    queryFn: ({ signal }) => mailApi.mailboxes(domainID ?? '', signal),
    enabled: Boolean(domainID),
  });
}

/** useAliases reads a domain's forwarders. */
export function useAliases(domainID: string | undefined) {
  return useQuery({
    queryKey: mailKeys.aliases(domainID ?? ''),
    queryFn: ({ signal }) => mailApi.aliases(domainID ?? '', signal),
    enabled: Boolean(domainID),
  });
}

/**
 * invalidateAll refreshes everything after a change.
 *
 * Everything, rather than the one list that changed, and the reason is the
 * shape of this phase: nearly every change reconciles the whole host, so a
 * mailbox added to one domain can change the warnings shown against another —
 * the certificate, the signing state, the ports that are offered.
 */
function useInvalidateMail() {
  const queryClient = useQueryClient();
  return () => {
    void queryClient.invalidateQueries({ queryKey: mailKeys.all });
  };
}

/** useSaveMailSettings writes the mail server's settings and applies them. */
export function useSaveMailSettings() {
  const invalidate = useInvalidateMail();
  return useMutation({
    mutationFn: (body: Partial<MailSettings>) => mailApi.saveSettings(body),
    onSuccess: invalidate,
  });
}

/** useInstallMail puts a mail server on the host. */
export function useInstallMail() {
  const invalidate = useInvalidateMail();
  return useMutation({
    mutationFn: (body: { filtering: boolean; antivirus: boolean }) => mailApi.install(body),
    onSuccess: invalidate,
  });
}

/** useCreateMailDomain adds a domain and gives it a signing key. */
export function useCreateMailDomain() {
  const invalidate = useInvalidateMail();
  return useMutation({
    mutationFn: (body: MailDomainInput) => mailApi.createDomain(body),
    onSuccess: invalidate,
  });
}

/** useUpdateMailDomain changes a domain's policies. */
export function useUpdateMailDomain() {
  const invalidate = useInvalidateMail();
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: MailDomainInput }) =>
      mailApi.updateDomain(id, body),
    onSuccess: invalidate,
  });
}

/** useDeleteMailDomain removes a domain, its mailboxes and its forwarders. */
export function useDeleteMailDomain() {
  const invalidate = useInvalidateMail();
  return useMutation({
    mutationFn: (id: string) => mailApi.removeDomain(id),
    onSuccess: invalidate,
  });
}

/** useRotateDKIM gives a domain a new signing key. */
export function useRotateDKIM() {
  const invalidate = useInvalidateMail();
  return useMutation({
    mutationFn: (id: string) => mailApi.rotateDKIM(id),
    onSuccess: invalidate,
  });
}

/** useCreateMailbox adds a mailbox. */
export function useCreateMailbox() {
  const invalidate = useInvalidateMail();
  return useMutation({
    mutationFn: ({ domainID, body }: { domainID: string; body: MailboxInput }) =>
      mailApi.createMailbox(domainID, body),
    onSuccess: invalidate,
  });
}

/** useUpdateMailbox changes a mailbox's quota or suspends it. */
export function useUpdateMailbox() {
  const invalidate = useInvalidateMail();
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: MailboxInput }) =>
      mailApi.updateMailbox(id, body),
    onSuccess: invalidate,
  });
}

/** useDeleteMailbox removes a mailbox. Its messages stay on the host. */
export function useDeleteMailbox() {
  const invalidate = useInvalidateMail();
  return useMutation({
    mutationFn: (id: string) => mailApi.removeMailbox(id),
    onSuccess: invalidate,
  });
}

/** useSetMailboxPassword replaces a mailbox's password. */
export function useSetMailboxPassword() {
  const invalidate = useInvalidateMail();
  return useMutation({
    mutationFn: ({ id, password }: { id: string; password: string }) =>
      mailApi.setPassword(id, password),
    onSuccess: invalidate,
  });
}

/** useSetAutoresponder writes a mailbox's vacation reply. */
export function useSetAutoresponder() {
  const invalidate = useInvalidateMail();
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: MailAutoresponderInput }) =>
      mailApi.setAutoresponder(id, body),
    onSuccess: invalidate,
  });
}

/** useClearAutoresponder removes a vacation reply. */
export function useClearAutoresponder() {
  const invalidate = useInvalidateMail();
  return useMutation({
    mutationFn: (id: string) => mailApi.clearAutoresponder(id),
    onSuccess: invalidate,
  });
}

/** useCreateAlias adds a forwarder. */
export function useCreateAlias() {
  const invalidate = useInvalidateMail();
  return useMutation({
    mutationFn: ({ domainID, body }: { domainID: string; body: MailAliasInput }) =>
      mailApi.createAlias(domainID, body),
    onSuccess: invalidate,
  });
}

/** useDeleteAlias removes a forwarder. */
export function useDeleteAlias() {
  const invalidate = useInvalidateMail();
  return useMutation({
    mutationFn: (id: string) => mailApi.removeAlias(id),
    onSuccess: invalidate,
  });
}

/** useInstallWebmail unpacks webmail into a website. */
export function useInstallWebmail() {
  const invalidate = useInvalidateMail();
  return useMutation({
    mutationFn: (websiteID: string) => mailApi.installWebmail(websiteID),
    onSuccess: invalidate,
  });
}

/** useRemoveWebmail deletes webmail from its document root. */
export function useRemoveWebmail() {
  const invalidate = useInvalidateMail();
  return useMutation({
    mutationFn: () => mailApi.removeWebmail(),
    onSuccess: invalidate,
  });
}
