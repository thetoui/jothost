import { useCallback, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { firewallApi } from '@/features/firewall/api';
import type { FirewallPending, FirewallRuleInput } from '@/types/api';

export const firewallKeys = {
  all: ['firewall'] as const,
  status: () => [...firewallKeys.all, 'status'] as const,
};

/**
 * useFirewall reads the host's firewall.
 *
 * Polled while a change is waiting to be confirmed, because that state has a
 * deadline: the Agent undoes the change when it passes, and a page still
 * showing "waiting" a minute later would be describing something that no
 * longer exists.
 */
export function useFirewall() {
  return useQuery({
    queryKey: firewallKeys.status(),
    queryFn: ({ signal }) => firewallApi.status(signal),
    refetchInterval: (query) => (query.state.data?.pending ? 2_000 : 30_000),
    placeholderData: (previous) => previous,
  });
}

/** What a change is doing, for a page that has to explain itself. */
export type ChangeStage = 'idle' | 'applying' | 'verifying' | 'done' | 'reverting';

/**
 * useFirewallChange applies a change and confirms it over the network.
 *
 * This is the client's half of the protocol in CLAUDE.md section 19, and the
 * order matters:
 *
 *  1. Apply. The change is live immediately, and the Agent has armed a timer to
 *     undo it.
 *  2. Re-read the firewall. This is an ordinary request, and that is the whole
 *     point: it has to cross the network the change just altered. If the rule
 *     cut the panel off, this is what fails.
 *  3. Confirm. Only a request that arrived proves the panel is still reachable,
 *     so only then is the change made permanent.
 *
 * If step 2 or 3 never happens — the browser cannot reach the panel, the tab is
 * closed, the machine sleeps — nothing confirms, and the Agent restores the
 * rules by itself. The user sees the change revert; they do not lose the host.
 */
export function useFirewallChange() {
  const queryClient = useQueryClient();
  const [stage, setStage] = useState<ChangeStage>('idle');
  const [pending, setPending] = useState<FirewallPending | null>(null);

  const reset = useCallback(() => {
    setStage('idle');
    setPending(null);
  }, []);

  const mutation = useMutation({
    mutationFn: async (input: {
      kind: 'add' | 'delete' | 'enable' | 'disable';
      rule?: FirewallRuleInput;
    }) => {
      setStage('applying');

      const applied = await (input.kind === 'add'
        ? firewallApi.addRule(input.rule as FirewallRuleInput)
        : input.kind === 'delete'
          ? firewallApi.deleteRule(input.rule as FirewallRuleInput)
          : input.kind === 'enable'
            ? firewallApi.enable()
            : firewallApi.disable());

      setPending(applied);
      setStage('verifying');

      // The proof. A request that comes back is a panel that is still
      // reachable through the rules just written.
      await firewallApi.status();

      await firewallApi.confirm(applied.id);
      setStage('done');
      return applied;
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: firewallKeys.status() });
    },
    onError: () => {
      // Reaching here means the apply itself failed — refused by the guard, or
      // rejected by ufw — or the verification did not come back. The first is
      // an error to show; the second means the Agent is about to undo it.
      setStage((current) => (current === 'verifying' ? 'reverting' : 'idle'));
    },
  });

  return { ...mutation, stage, pending, reset };
}

/** useRollbackChange undoes a change that is waiting, without waiting for it. */
export function useRollbackChange() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (id: string) => firewallApi.rollback(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: firewallKeys.status() });
    },
  });
}

/** useConfirmChange commits a change that is still waiting. */
export function useConfirmChange() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (id: string) => firewallApi.confirm(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: firewallKeys.status() });
    },
  });
}
