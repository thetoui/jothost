import { request } from '@/services/apiClient';
import type {
  DeployActionKind,
  Deployment,
  GitRepository,
  GitRepositoryInput,
} from '@/types/api';

/** Deployment calls. */
export const deploymentsApi = {
  overview: (signal?: AbortSignal) =>
    request<{ repositories: GitRepository[] }>('/deployments', signal ? { signal } : {}),

  detail: (id: string, signal?: AbortSignal) =>
    request<GitRepository>(`/deployments/repositories/${id}`, signal ? { signal } : {}),

  configure: (body: GitRepositoryInput) =>
    request<GitRepository>('/deployments/repositories', { method: 'POST', body }),

  remove: (id: string) =>
    request<void>(`/deployments/repositories/${id}`, { method: 'DELETE' }),

  setActions: (id: string, steps: DeployActionKind[]) =>
    request<{ actions: unknown[] }>(`/deployments/repositories/${id}/actions`, {
      method: 'PUT',
      body: { steps },
    }),

  /**
   * A deploy key for one website.
   *
   * The private half never comes back — it is written on the host that
   * authenticates with it and stays there. What returns is the public half,
   * which is what goes into a forge's deploy key box.
   */
  generateKey: (id: string) =>
    request<GitRepository>(`/deployments/repositories/${id}/key`, { method: 'POST' }),

  deploy: (id: string, commit?: string) =>
    request<Deployment>(`/deployments/repositories/${id}/deploy`, {
      method: 'POST',
      body: { commit: commit ?? '' },
    }),

  rollback: (id: string, commit: string) =>
    request<Deployment>(`/deployments/repositories/${id}/rollback`, {
      method: 'POST',
      body: { commit },
    }),

  run: (id: string, signal?: AbortSignal) =>
    request<Deployment>(`/deployments/runs/${id}`, signal ? { signal } : {}),

  /** The build's output. Behind deploy.manage, because a build prints
   * whatever the build printed. */
  log: (id: string, signal?: AbortSignal) =>
    request<{ id: string; status: string; log: string; truncated: boolean }>(
      `/deployments/runs/${id}/log`,
      signal ? { signal } : {},
    ),
};
