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

/** Where a subdomain's files live. */
export type DocumentRootMode = 'nested' | 'isolated';

/** Whether a subdomain shares its parent's PHP pool or has its own. */
export type PHPPoolMode = 'inherit' | 'dedicated';

/** Whether a subdomain's files belong to its parent's account or its own. */
export type SystemUserMode = 'inherit' | 'dedicated';

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
  /** Set on a subdomain; null on a top-level site. */
  parent_website_id: string | null;
  /**
   * The loopback port Apache serves this site on in the hybrid arrangement.
   * Kept when the host goes back to nginx alone, so it stays this site's.
   */
  apache_port: number | null;
  /** Whether Apache reads .htaccess for this site. Only meaningful in hybrid. */
  allow_override: boolean;
  /** The three modes are set together on a subdomain, or all null. */
  document_root_mode: DocumentRootMode | null;
  php_pool_mode: PHPPoolMode | null;
  system_user_mode: SystemUserMode | null;
  created_at: string;
  updated_at: string;
  domains?: WebsiteDomain[];
  /** Present on a top-level site loaded on its own. */
  subdomains?: Website[];
}

/** What a firewall rule does. */
export type FirewallAction = 'allow' | 'deny' | 'reject' | 'limit';

export interface FirewallRule {
  action: FirewallAction;
  direction: 'in' | 'out';
  protocol: 'tcp' | 'udp' | 'any';
  /** A port, an inclusive range ("7080:7090"), or empty for every port. */
  port: string;
  /** "any", an address, or a CIDR block. */
  source: string;
  comment?: string;
}

/** What the panel sends. The API fills in the defaults for what is left out. */
export interface FirewallRuleInput {
  action: FirewallAction;
  direction?: 'in' | 'out';
  protocol?: 'tcp' | 'udp' | 'any';
  port?: string;
  source?: string;
  comment?: string;
  /** How long before the change is undone unless it is confirmed. */
  window_seconds?: number;
}

/**
 * A change that has been applied and not yet confirmed.
 *
 * It is live on the host right now. If nothing confirms it before the deadline
 * — which is what happens when the change cuts the panel off — the Agent puts
 * the rules back on its own.
 */
export interface FirewallPending {
  id: string;
  kind: 'rule.add' | 'rule.delete' | 'enable' | 'disable' | 'default';
  rule: FirewallRule;
  policy?: string;
  deadline: string;
}

export interface FirewallStatus {
  available: boolean;
  enabled: boolean;
  default_incoming: string;
  default_outgoing: string;
  rules: FirewallRule[];
  reason?: string;
  /** Ports no change may close: how the host is administered. */
  guarded_ports: number[];
  pending?: FirewallPending;
}

/** The verbs a service may be given. */
export type ServiceAction = 'start' | 'stop' | 'restart' | 'enable' | 'disable';

/** One service as the host actually has it. */
export interface HostService {
  key: string;
  label: string;
  role: 'web' | 'runtime' | 'database' | 'cache' | 'system';
  summary: string;
  units: string[];
  /** Refuses stop and disable: SSH is how the host is administered. */
  protected: boolean;
  installed: boolean;
  running: boolean;
  pid: number;
  unit: string;
  /** Null when nothing can say whether it starts at boot. */
  enabled: boolean | null;
  active_state: string;
  sub_state: string;
  controllable: boolean;
}

export interface ServiceList {
  services: HostService[];
  count: number;
  /** False on a host with no service manager: states are true, nothing can be changed. */
  controllable: boolean;
}

export interface ServiceActionResult {
  service: string;
  unit: string;
  action: ServiceAction;
  running: boolean;
  enabled: boolean | null;
  state: string;
}

/** Which web server arrangement a host runs. */
export type WebserverMode = 'nginx' | 'hybrid';

export interface ApacheStatus {
  available: boolean;
  running: boolean;
  version?: string;
  sites: number;
  can_install: boolean;
}

export interface WebserverStatus {
  mode: WebserverMode;
  apache: ApacheStatus;
  /** How many websites a mode change would rewrite. */
  sites: number;
}

export interface WebserverModeChanged {
  mode: WebserverMode;
  jobs: Job[];
}

export interface SubdomainList {
  subdomains: Website[];
  count: number;
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

// --------------------------------------------------------------------- ssl

export type SSLProvider = 'letsencrypt' | 'selfsigned';

export type SSLStatus =
  | 'pending'
  | 'issuing'
  | 'valid'
  | 'expiring'
  | 'expired'
  | 'revoked'
  | 'failed';

export interface SSLCertificate {
  id: string;
  website_id: string;
  /** Present on the list endpoint, which joins the website. */
  primary_domain: string;
  provider: SSLProvider;
  domains: string[];
  issuer: string | null;
  fingerprint: string | null;
  issued_at: string | null;
  expires_at: string | null;
  auto_renew: boolean;
  status: SSLStatus;
  last_error: string | null;
  certificate_path: string | null;
  /** Computed by the API so every client agrees on what "12 days" means. */
  days_remaining: number | null;
}

export interface SSLCertificateList {
  certificates: SSLCertificate[];
  count: number;
  /** Certificates expiring or already expired. */
  needing_attention: number;
}

/** A website's certificate state. No certificate is `enabled: false`. */
export interface WebsiteSSL {
  enabled: boolean;
  certificate?: SSLCertificate;
}

export interface SSLProviders {
  selfsigned: boolean;
  letsencrypt: boolean;
  /** Explains a reduced set, e.g. when the agent could not be reached. */
  detail?: string;
}

// ------------------------------------------------------------------- files

/** What a directory entry is. Symlinks are their own kind, never followed. */
export type FileEntryType = 'file' | 'directory' | 'symlink' | 'other';

export interface FileEntry {
  name: string;
  path: string;
  type: FileEntryType;
  size: number;
  /** Octal permission string such as "0644". */
  mode: string;
  modified: string;
  owner: string;
  group: string;
  uid: number;
  gid: number;
  /** A symlink's destination. Absent for everything else. */
  target?: string;
  /** Whether a symlink points somewhere the panel is allowed to follow. */
  target_inside_root?: boolean;
  /** Small enough to open in an editor rather than only download. */
  editable: boolean;
}

/**
 * One page of a directory.
 *
 * Listings are paged because a single Agent response has to fit its socket's
 * 1 MiB limit, and a document root can hold far more entries than that.
 */
export interface FileListing {
  path: string;
  /** Empty at the root, which has no parent the caller may navigate to. */
  parent: string;
  entries: FileEntry[];
  total: number;
  offset: number;
  limit: number;
  truncated: boolean;
}

export interface FileMatch {
  entry: FileEntry;
  line?: string;
  line_number?: number;
}

export interface FileSearchResult {
  path: string;
  query: string;
  matches: FileMatch[];
  /** The search stopped at a limit rather than at the end of the tree. */
  truncated: boolean;
  scanned: number;
}

export interface FileArchiveResult {
  path: string;
  entries: number;
  size: number;
}

// ------------------------------------------------------------------ editor

/** A file as the code editor sees it. */
export interface FileContent {
  path: string;
  content: string;
  size: number;
  mode: string;
  modified: string;
  owner: string;
  /**
   * Identifies exactly this content. Sent back on save so a second editor
   * cannot silently overwrite the first one's work.
   */
  checksum: string;
  /** Syntax highlighting hint, derived from the file name. */
  language: string;
  /** What the file already uses, so saving does not convert every line. */
  end_of_line: 'lf' | 'crlf';
}

/** What a save returns: the same metadata, without the content echoed back. */
export interface FileContentSaved {
  path: string;
  size: number;
  mode: string;
  modified: string;
  owner: string;
  checksum: string;
  language: string;
}

// --------------------------------------------------------------- databases

/** Which database servers the host runs. */
export type DatabaseEngineName = 'mysql' | 'mariadb' | 'postgres';

/** How much a database account may do. Weakest first. */
export type DatabasePrivilege = 'readonly' | 'readwrite' | 'full';

/** Where a MySQL account may connect from. PostgreSQL roles have no host. */
export type DatabaseHost = 'localhost' | '%';

export interface DatabaseEngine {
  engine: DatabaseEngineName;
  available: boolean;
  version?: string;
  /** Why an unavailable engine is unavailable, so the panel can say so. */
  detail?: string;
  /**
   * Whether accounts are identified by a user and host pair. False on
   * PostgreSQL, where a role is global — the panel hides the host control
   * rather than offering one the server would ignore.
   */
  supports_host_patterns: boolean;
}

export interface DatabaseEngines {
  engines: DatabaseEngine[];
  available: boolean;
  privileges: DatabasePrivilege[];
  detail?: string;
}

export type DatabaseStatus = 'creating' | 'active' | 'deleting' | 'failed';

export interface Database {
  id: string;
  server_id: string;
  website_id: string | null;
  name: string;
  engine: DatabaseEngineName;
  status: DatabaseStatus;
  charset: string | null;
  collation: string | null;
  /** Null until the size has been measured; not the same as an empty database. */
  size_bytes: number | null;
  size_checked_at: string | null;
  created_at: string;
  updated_at: string;
  website_domain: string | null;
  user_count: number;
}

export interface DatabaseGrant {
  database_id: string;
  database_name: string;
  privilege: DatabasePrivilege;
}

export interface DatabaseUser {
  id: string;
  server_id: string;
  engine: DatabaseEngineName;
  username: string;
  /** Empty on PostgreSQL. */
  host: string;
  created_at: string;
  updated_at: string;
  password_updated_at: string;
  grants?: DatabaseGrant[];
}

export interface DatabaseList {
  databases: Database[];
  count: number;
  total_size_bytes: number;
}

export interface DatabaseDetail {
  database: Database;
  users: DatabaseUser[];
}

export interface DatabaseUserList {
  users: DatabaseUser[];
  count: number;
}

/**
 * What creating a database returns.
 *
 * The password is present exactly once, in this response. The server keeps only
 * a hash, so the panel showing it here is the only chance anyone has to copy it
 * before it has to be looked up deliberately.
 */
export interface DatabaseCreated {
  database: Database;
  user?: DatabaseUser;
  password?: string;
}

export interface DatabaseUserCreated {
  user: DatabaseUser;
  password: string;
}

export interface DatabasePassword {
  password: string;
}

/** phpMyAdmin as the managed host has it. */
export interface DatabaseConsole {
  installed: boolean;
  /** True once the panel has written its vhost, which is what makes it reachable. */
  served: boolean;
  webroot?: string;
  server_name?: string;
  url?: string;
  php_version?: string;
  /** Whether the host has what installation needs. */
  can_install: boolean;
  /** Why it cannot be installed, when it cannot. */
  detail?: string;
}

// ----------------------------------------------------------------- Node.js

/** A Node.js runtime the host has. */
export interface NodeVersion {
  version: string;
  full_version: string;
  binary_path: string;
  npm_version: string;
}

/** A release line the host could install. */
export interface NodeOffer {
  version: string;
  package: string;
  label: string;
}

export interface NodeVersions {
  versions: NodeVersion[];
  count: number;
  available: boolean;
  offers: NodeOffer[];
  can_install: boolean;
  package_manager: string;
  /**
   * "systemd" or "agent" — which mechanism runs applications on this host. It
   * changes what the logs endpoint can return, so the panel says so.
   */
  managed_by: string;
  detail?: string;
}

export type NodeAppStatus = 'stopped' | 'starting' | 'running' | 'failed';

/** What an application is doing on the host right now. */
export interface NodeRuntime {
  state: string;
  pid: number;
  port: number;
  uptime_seconds: number;
  managed_by: string;
  /** Separates "the process is up" from "it is answering". */
  listening: boolean;
  detail: string;
}

export interface NodeApp {
  id: string;
  server_id: string;
  website_id: string;
  name: string;
  node_version: string;
  application_root: string;
  startup_file: string;
  port: number;
  status: NodeAppStatus;
  systemd_service: string;
  autostart: boolean;
  last_error: string | null;
  created_at: string;
  updated_at: string;
  website_domain?: string;
  system_user?: string;
  runtime?: NodeRuntime;
  /** The names that are set. The values are a separate, audited read. */
  environment?: string[];
}

export interface NodeAppList {
  applications: NodeApp[];
  count: number;
}

export interface NodeLogs {
  source: string;
  lines: string[];
  error_lines: string[];
  out_path: string;
  error_path: string;
  detail: string;
}
