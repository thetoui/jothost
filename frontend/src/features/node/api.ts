import { request } from '@/services/apiClient';
import type { NodeApp, NodeAppList, NodeLogs, NodeVersions } from '@/types/api';

export interface CreateAppInput {
  website_id: string;
  /** Defaults to a name derived from the domain. */
  name?: string;
  /** Omitted on a host with one runtime, which is most of them. */
  node_version?: string;
  /** Defaults to server.js. */
  startup_file?: string;
  port: number;
}

export interface SetEnvInput {
  id: string;
  key: string;
  value: string;
}

/** Node.js API calls. */
export const nodeApi = {
  versions: (signal?: AbortSignal) =>
    request<NodeVersions>('/node/versions', signal ? { signal } : {}),

  list: (signal?: AbortSignal) =>
    request<NodeAppList>('/node/apps', signal ? { signal } : {}),

  get: (id: string, signal?: AbortSignal) =>
    request<{ application: NodeApp }>(
      `/node/apps/${encodeURIComponent(id)}`,
      signal ? { signal } : {},
    ),

  create: (body: CreateAppInput) =>
    request<{ application: NodeApp }>('/node/apps', { method: 'POST', body }),

  remove: (id: string) =>
    request<{ deleted: boolean }>(`/node/apps/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  start: (id: string) =>
    request<{ application: NodeApp }>(`/node/apps/${encodeURIComponent(id)}/start`, {
      method: 'POST',
    }),

  stop: (id: string) =>
    request<{ application: NodeApp }>(`/node/apps/${encodeURIComponent(id)}/stop`, {
      method: 'POST',
    }),

  restart: (id: string) =>
    request<{ application: NodeApp }>(`/node/apps/${encodeURIComponent(id)}/restart`, {
      method: 'POST',
    }),

  logs: (id: string, lines = 200, signal?: AbortSignal) =>
    request<NodeLogs>(
      `/node/apps/${encodeURIComponent(id)}/logs?lines=${lines}`,
      signal ? { signal } : {},
    ),

  /**
   * Installs the application's dependencies.
   *
   * The slowest thing here by far: npm downloads a whole dependency tree, and
   * the request holds until it finishes so the answer says whether it worked.
   */
  installDependencies: (id: string) =>
    request<{ installed: boolean }>(`/node/apps/${encodeURIComponent(id)}/dependencies`, {
      method: 'POST',
    }),

  setEnv: ({ id, key, value }: SetEnvInput) =>
    request<{ updated: boolean; detail: string }>(
      `/node/apps/${encodeURIComponent(id)}/environment`,
      { method: 'PUT', body: { key, value } },
    ),

  removeEnv: (id: string, key: string) =>
    request<{ removed: boolean; detail: string }>(
      `/node/apps/${encodeURIComponent(id)}/environment/${encodeURIComponent(key)}`,
      { method: 'DELETE' },
    ),

  /**
   * Reads back the whole environment, values included.
   *
   * A separate call rather than a field on the listing, and audited: these are
   * the application's credentials, and "who read them" has to stay answerable.
   */
  revealEnv: (id: string) =>
    request<{ environment: Record<string, string> }>(
      `/node/apps/${encodeURIComponent(id)}/environment`,
    ),
};
