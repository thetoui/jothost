/**
 * Client-side mirror of the API response envelope defined in
 * API_SPEC.md section 1. Keep this in sync with api/internal/httpx.
 */
export interface ApiEnvelope<T> {
  success: boolean;
  data?: T;
  error?: ApiErrorDetail;
  request_id: string;
}

export interface ApiErrorDetail {
  code: string;
  message: string;
}

/** Stable error codes the UI branches on. */
export const ApiErrorCode = {
  Unauthorized: 'UNAUTHORIZED',
  Forbidden: 'FORBIDDEN',
  NotFound: 'RESOURCE_NOT_FOUND',
  ValidationFailed: 'VALIDATION_FAILED',
  RateLimited: 'RATE_LIMITED',
  Conflict: 'CONFLICT',
  Internal: 'INTERNAL_ERROR',
} as const;

export interface HealthResponse {
  status: string;
  service: string;
  version: string;
}

export type DependencyStatus = 'up' | 'down';

export interface ReadinessCheck {
  status: DependencyStatus;
  error?: string;
}

export interface ReadinessResponse {
  ready: boolean;
  checks: Record<string, ReadinessCheck>;
}

/* ------------------------------------------------------------------ auth */

export interface TokenPair {
  access_token: string;
  refresh_token: string;
  token_type: string;
  expires_in: number;
}

/**
 * A login either returns tokens or demands a second factor. The two shapes
 * arrive on the same endpoint, so `mfa_required` is the discriminator.
 */
export interface LoginResponse extends Partial<TokenPair> {
  mfa_required?: boolean;
  mfa_token?: string;
}

export interface UserProfile {
  id: string;
  username: string;
  email: string | null;
  status: string;
  roles: string[];
  permissions: string[];
  two_factor_enabled: boolean;
  last_login_at: string | null;
  created_at: string;
}

export interface TwoFactorSetupResponse {
  /** Shown once at enrolment. Never persist or log this. */
  secret: string;
  otpauth_uri: string;
}

/* ------------------------------------------------------------- dashboard */

/**
 * A dashboard panel and its availability.
 *
 * The three states are distinct: data present, unavailable for a stated
 * reason, or unsupported by this host. Rendering "unsupported" as an error
 * would tell an operator to fix something that is not broken.
 */
export interface Widget<T> {
  available: boolean;
  data?: T;
  unsupported?: boolean;
  error?: string;
}

export interface ServerRecord {
  id: string;
  hostname: string;
  os_name: string | null;
  os_version: string | null;
  kernel: string | null;
  architecture: string | null;
  ipv4: string | null;
  ipv6: string | null;
  status: 'online' | 'offline' | 'unknown';
  agent_version: string | null;
  created_at: string;
  updated_at: string;
}

export interface SystemInfo {
  hostname: string;
  os_name: string;
  os_version: string;
  kernel_version: string;
  architecture: string;
  uptime_seconds: number;
  boot_time: string;
  cores: number;
}

export interface CPUStats {
  usage_percent: number;
  user_percent: number;
  system_percent: number;
  iowait_percent: number;
  idle_percent: number;
  cores: number;
  sample_window: string;
}

export interface MemoryStats {
  total_bytes: number;
  available_bytes: number;
  used_bytes: number;
  free_bytes: number;
  buffers_bytes: number;
  cached_bytes: number;
  swap_total_bytes: number;
  swap_used_bytes: number;
  swap_free_bytes: number;
  used_percent: number;
  swap_used_percent: number;
}

export interface Filesystem {
  device: string;
  mount_point: string;
  type: string;
  total_bytes: number;
  used_bytes: number;
  free_bytes: number;
  available_bytes: number;
  used_percent: number;
  inodes_used_percent: number;
  read_only: boolean;
}

export interface DiskStats {
  filesystems: Filesystem[];
  total_bytes: number;
  used_bytes: number;
}

export interface NetworkInterface {
  name: string;
  rx_bytes: number;
  tx_bytes: number;
  rx_errors: number;
  tx_errors: number;
  rx_bytes_per_second: number;
  tx_bytes_per_second: number;
}

export interface NetworkStats {
  interfaces: NetworkInterface[];
  total_rx_bytes: number;
  total_tx_bytes: number;
  sample_window: string;
}

export interface LoadStats {
  load_1: number;
  load_5: number;
  load_15: number;
  running_processes: number;
  total_processes: number;
  cores: number;
  load_per_core: number;
}

export interface ServiceState {
  name: string;
  kind: 'systemd' | 'dependency';
  running: boolean;
  status: string;
  enabled?: boolean | null;
}

export type AlertSeverity = 'critical' | 'warning';

export interface DashboardAlert {
  severity: AlertSeverity;
  category: string;
  message: string;
}

export interface DashboardSnapshot {
  server: ServerRecord;
  system: Widget<SystemInfo>;
  cpu: Widget<CPUStats>;
  memory: Widget<MemoryStats>;
  disk: Widget<DiskStats>;
  network: Widget<NetworkStats>;
  load: Widget<LoadStats>;
  services: Widget<ServiceState[]>;
  alerts: DashboardAlert[];
  generated_at: string;
}

export type MetricRange = '1h' | '24h' | '7d' | '30d';

export interface MetricPoint {
  timestamp: string;
  cpu_percent: number | null;
  memory_percent: number | null;
  disk_percent: number | null;
  load_1: number | null;
  network_rx_per_second: number | null;
  network_tx_per_second: number | null;
}

export interface MetricSeries {
  range: MetricRange;
  bucket: string;
  from: string;
  to: string;
  points: MetricPoint[];
}

// ---------------------------------------------------------------- websites

export type WebsiteStatus = 'creating' | 'active' | 'suspended' | 'failed' | 'deleting';

export type DomainType = 'primary' | 'alias' | 'subdomain' | 'redirect';

export interface WebsiteDomain {
  id: string;
  website_id: string;
  domain: string;
  type: DomainType;
  status: string;
  redirect_to: string | null;
  created_at: string;
}

export interface Website {
  id: string;
  server_id: string;
  name: string | null;
  primary_domain: string;
  document_root: string;
  system_user: string;
  php_version: string | null;
  status: WebsiteStatus;
  ssl_enabled: boolean;
  https_redirect: boolean;
  created_at: string;
  updated_at: string;
  domains?: WebsiteDomain[];
}

export interface WebsiteList {
  websites: Website[];
  count: number;
}

export interface DomainList {
  domains: WebsiteDomain[];
  count: number;
}

// -------------------------------------------------------------------- jobs

export type JobStatus = 'PENDING' | 'RUNNING' | 'SUCCESS' | 'FAILED' | 'CANCELLED';

export interface Job {
  id: string;
  type: string;
  status: JobStatus;
  payload?: Record<string, unknown>;
  result?: Record<string, unknown>;
  error?: string;
  progress: number;
  message?: string;
  created_by: string | null;
  resource_type: string | null;
  resource_id: string | null;
  created_at: string;
  started_at: string | null;
  completed_at: string | null;
}

export interface JobList {
  jobs: Job[];
  count: number;
}

/** A queued website creation: the row exists, the site does not yet. */
export interface WebsiteCreated {
  website: Website;
  job: Job;
}

/** A queued change that only returns the job following it. */
export interface JobAccepted {
  job: Job;
}

export interface DomainCreated {
  domain: WebsiteDomain;
  job: Job;
}

// --------------------------------------------------------------------- php

export type PHPVersionStatus = 'available' | 'installing' | 'removing' | 'failed';

export interface PHPVersion {
  id: string;
  version: string;
  binary_path: string | null;
  fpm_service: string | null;
  status: PHPVersionStatus;
  installed: boolean;
  /** Websites currently running this version; it cannot be removed above zero. */
  in_use: number;
  created_at: string;
  updated_at: string;
}

export interface PHPVersionList {
  versions: PHPVersion[];
  count: number;
}

export interface PHPPool {
  id: string;
  website_id: string;
  php_version: string;
  pool_name: string;
  socket_path: string;
  memory_limit: string | null;
  max_children: number | null;
  upload_max_filesize: string | null;
  max_execution_time: number | null;
  opcache_enabled: boolean;
  created_at: string;
  updated_at: string;
}

/** A website's PHP state. A static site is `enabled: false`, not an error. */
export interface WebsitePHP {
  enabled: boolean;
  pool?: PHPPool;
}

export interface PHPConfig {
  memory_limit: string;
  upload_max_filesize: string;
  max_execution_time: number;
  opcache: boolean;
  max_children: number;
  version: string;
}
