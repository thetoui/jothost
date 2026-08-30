import { request } from '@/services/apiClient';
import type {
  Database,
  DatabaseCreated,
  DatabaseDetail,
  DatabaseEngineName,
  DatabaseEngines,
  DatabaseList,
  DatabasePassword,
  DatabasePrivilege,
  DatabaseUserCreated,
  DatabaseUserList,
} from '@/types/api';

export interface CreateDatabaseInput {
  name: string;
  /** Omitted on a host running one engine, which is most of them. */
  engine?: DatabaseEngineName;
  website_id?: string;
  create_user?: boolean;
  username?: string;
  host?: string;
  /** Left empty so the host generates one. */
  password?: string;
}

export interface AddUserInput {
  databaseId: string;
  username?: string;
  host?: string;
  password?: string;
  privilege?: DatabasePrivilege;
}

export interface SetGrantInput {
  databaseId: string;
  userId: string;
  /** An empty privilege revokes. */
  privilege: DatabasePrivilege | '';
}

export interface SetPasswordInput {
  userId: string;
  /** Empty asks the host to generate one. */
  password?: string;
}

/**
 * Database API calls.
 *
 * Unlike websites and certificates, none of these return a job: the work is a
 * single statement on the host, so the response arrives when it is done.
 */
export const databasesApi = {
  list: (signal?: AbortSignal) =>
    request<DatabaseList>('/databases', signal ? { signal } : {}),

  engines: (signal?: AbortSignal) =>
    request<DatabaseEngines>('/databases/engines', signal ? { signal } : {}),

  get: (id: string, signal?: AbortSignal) =>
    request<DatabaseDetail>(`/databases/${encodeURIComponent(id)}`, signal ? { signal } : {}),

  create: (body: CreateDatabaseInput) =>
    request<DatabaseCreated>('/databases', { method: 'POST', body }),

  remove: (id: string) =>
    request<{ deleted: boolean }>(`/databases/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  refreshSize: (id: string) =>
    request<{ database: Database }>(`/databases/${encodeURIComponent(id)}/size`, {
      method: 'POST',
    }),

  users: (signal?: AbortSignal) =>
    request<DatabaseUserList>('/database-users', signal ? { signal } : {}),

  addUser: ({ databaseId, ...body }: AddUserInput) =>
    request<DatabaseUserCreated>(`/databases/${encodeURIComponent(databaseId)}/users`, {
      method: 'POST',
      body,
    }),

  removeUser: (userId: string) =>
    request<{ deleted: boolean }>(`/database-users/${encodeURIComponent(userId)}`, {
      method: 'DELETE',
    }),

  setGrant: ({ databaseId, userId, privilege }: SetGrantInput) =>
    request<{ privilege: string; updated: boolean }>(
      `/databases/${encodeURIComponent(databaseId)}/users/${encodeURIComponent(userId)}`,
      { method: 'PATCH', body: { privilege } },
    ),

  setPassword: ({ userId, password }: SetPasswordInput) =>
    request<DatabasePassword & { changed: boolean }>(
      `/database-users/${encodeURIComponent(userId)}/password`,
      { method: 'PATCH', body: { password: password ?? '' } },
    ),

  /**
   * Reads back a stored password.
   *
   * A separate call rather than a field on the user listing: this is the one
   * request in the panel that returns a working credential, and every call is
   * audited. Folding it into a listing would make "who looked at this" an
   * unanswerable question.
   */
  revealPassword: (userId: string) =>
    request<DatabasePassword>(`/database-users/${encodeURIComponent(userId)}/password`),
};
