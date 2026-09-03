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
  /** True when another part of the panel owns the daemon's lifecycle. */
  self_managed: boolean;
  /** What owns it, when it is self-managed. */
  self_managed_by: string;
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
  /** The init system driving the host: "systemd", "openrc", or empty for neither. */
  manager: string;
}

export interface ServiceActionResult {
  service: string;
  unit: string;
  action: ServiceAction;
  running: boolean;
  enabled: boolean | null;
  state: string;
}

// -------------------------------------------------------------- fail2ban

export interface Fail2BanJail {
  name: string;
  label: string;
  summary: string;
  /** False for a jail somebody configured by hand: listed because it is banning
   *  people, and left alone. */
  managed: boolean;
  enabled: boolean;

  max_retry: number;
  find_time: number;
  ban_time: number;
  currently_banned: number;
  total_banned: number;
  currently_failed: number;
  total_failed: number;

  log_paths: string[];
  banned: string[];
  /** False where this host has none of the logs the jail watches. */
  available: boolean;
  reason: string;
}

export interface Fail2BanStatus {
  available: boolean;
  running: boolean;
  can_install: boolean;
  version: string;
  reason: string;
  jails: Fail2BanJail[];
  /** Addresses no jail may ban. Loopback is always in it. */
  ignored: string[];
  drop_in_path: string;
  banned: number;
}

export interface Fail2BanBanned {
  address: string;
  jail: string;
}

export interface Fail2BanBannedList {
  banned: Fail2BanBanned[];
  count: number;
}

/** A change to one jail. An omitted field is left alone. */
export interface Fail2BanChange {
  enabled?: boolean;
  max_retry?: number;
  find_time?: number;
  ban_time?: number;
}

export interface Fail2BanApplyResult {
  jail: string;
  enabled: boolean;
  policy: { max_retry: number; find_time: number; ban_time: number };
  backup: string;
  reloaded: boolean;
}

// -------------------------------------------------------------------- ssh

export interface SSHConfig {
  available: boolean;
  /** False where the panel will not write: a host whose sshd_config has no
   *  Include would silently ignore the file, so it is read-only instead. */
  managed: boolean;
  reason: string;

  ports: number[];
  /** sshd's own vocabulary: yes | without-password | forced-commands-only | no.
   *  "without-password" is what it prints for "prohibit-password". */
  root_login: string;
  password_authentication: boolean;
  pubkey_authentication: boolean;
  permit_empty_passwords: boolean;
  x11_forwarding: boolean;
  max_auth_tries: number;
  login_grace_time: number;

  config_path: string;
  drop_in_path: string;
  running: boolean;
}

export interface SSHAccount {
  name: string;
  uid: number;
  home: string;
  shell: string;
  keys: number;
}

export interface SSHFinding {
  id: string;
  severity: 'high' | 'warn' | 'info';
  title: string;
  detail: string;
  action: string;
}

export interface SSHStatus {
  config: SSHConfig;
  accounts: SSHAccount[];
  findings: SSHFinding[];
}

export interface SSHKey {
  /** The SHA256 form OpenSSH prints. It identifies a key across removals, which
   *  a line number would not. */
  fingerprint: string;
  type: string;
  comment: string;
  bits: number;
  account: string;
}

export interface SSHKeyList {
  account: string;
  keys: SSHKey[];
  count: number;
}

/** A change to the server's settings. An omitted field is left alone. */
export interface SSHChange {
  port?: number;
  root_login?: string;
  password_authentication?: boolean;
  pubkey_authentication?: boolean;
  permit_empty_passwords?: boolean;
  x11_forwarding?: boolean;
  max_auth_tries?: number;
}

export interface SSHApplyResult {
  config: SSHConfig;
  changed: string[];
  backup: string;
  /** False means the change is on disk and takes effect at the next restart. */
  reloaded: boolean;
}

// ----------------------------------------------------------- scheduled jobs

/** The kinds of job the panel offers. */
export type CronJobType = 'php' | 'url' | 'command';

export interface CronJob {
  id: string;
  server_id: string;
  website_id: string;
  name: string;
  job_type: CronJobType;
  /** A five-field expression; the @-shorthands are expanded before storage. */
  schedule: string;
  /** What the operator entered: a script path, a URL, or a command line. */
  target: string;
  /** What is actually written into the crontab. */
  command: string;
  enabled: boolean;

  last_run_at: string | null;
  last_status: 'success' | 'failed' | null;
  last_exit_code: number | null;
  last_duration_ms: number | null;

  created_at: string;
  updated_at: string;

  website_domain?: string;
  /** The account the job runs as. Shown so it is visible that it is not root. */
  system_user?: string;
  /** Computed from the schedule. Null when the schedule can never fire, and
   *  for a disabled job. */
  next_run_at: string | null;
}

export interface CronJobList {
  jobs: CronJob[];
  count: number;
  types: CronJobType[];
}

/** What one manual run did. */
export interface CronRunResult {
  job_id: string;
  exit_code: number;
  output: string;
  truncated: boolean;
  duration_ms: number;
  timed_out: boolean;
  status: 'success' | 'failed';
}

// ---------------------------------------------------------------------- logs

/** The severity vocabulary the panel filters on, across every log format. */
export type LogLevel = 'error' | 'warn' | 'info' | 'debug';

export interface LogSource {
  /** The catalogue key. Requests name this, never a path. */
  key: string;
  label: string;
  summary: string;
  group: 'web' | 'runtime' | 'system' | 'panel';
  format: string;
  /** Where the Agent found it, so an operator can look at the same file over
   *  SSH. Empty when this host does not have it. */
  path: string;
  /** False for a catalogued log this host does not have — which is normal:
   *  nginx writes its error log on the first error. */
  present: boolean;
  size: number;
  modified: string | null;
}

export interface LogSourceList {
  sources: LogSource[];
  count: number;
  levels: LogLevel[];
}

export interface LogLine {
  /** Byte offset in the file, which is what identifies a line across polls. */
  offset: number;
  text: string;
  /** Empty where the format has no level to read. */
  level: string;
  truncated: boolean;
}

export interface LogTail {
  key: string;
  path: string;
  lines: LogLine[];
  /** Where a follower continues from. */
  offset: number;
  size: number;
  /** The file was replaced or truncated under us. */
  rotated: boolean;
  scanned: number;
  /** More exists than one response can carry. */
  partial: boolean;
  /** How many lines the search or level filter removed. */
  filtered: number;
  format: string;
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

/** One FTP account, as the panel records it and the host reports it. */
export interface FTPUser {
  id: string;
  server_id: string;
  website_id: string;
  username: string;
  /** Relative to the website's document root. Empty means the root itself. */
  home_subpath: string;
  access_level: 'full' | 'readonly';
  quota_mb: number;
  suspended: boolean;
  created_at: string;
  updated_at: string;
  website_domain?: string;
  system_user?: string;
  document_root?: string;
  /** The absolute directory the account is confined to. */
  home: string;
  /**
   * What proftpd counted this account uploading. Not a directory size: files
   * removed through the file manager were never seen by the FTP server.
   */
  used_mb: number;
  /** The host has this account disabled. */
  locked: boolean;
  /**
   * The panel has this account recorded and the FTP server does not have it —
   * a rebuilt host, or a restore. It cannot be repaired automatically, because
   * nothing holds the password. Setting a new one puts it back.
   */
  missing_on_host: boolean;
}

/** One connected FTP client. */
export interface FTPSession {
  pid: number;
  user: string;
  elapsed: string;
  activity: string;
  client: string;
  /** "ftp" or "ftps" — whether the password crossed the network encrypted. */
  protocol: string;
  location: string;
}

/** An FTP account as the host has it, with its usage. */
export interface FTPAccount {
  name: string;
  uid: number;
  gid: number;
  home: string;
  locked: boolean;
  quota_mb: number;
  used_mb: number;
}

/** A setting another configuration file also sets. */
export interface FTPConflict {
  directive: string;
  file: string;
  panel_wins: boolean;
}

/** The FTP server's own settings. */
export interface FTPSettings {
  server_id: string;
  passive_from: number;
  passive_to: number;
  tls_website_id: string;
  require_tls: boolean;
  masquerade_address: string;
  max_clients: number;
  tls_domain?: string;
}

/** Everything the FTP page shows. */
export interface FTPOverview {
  available: boolean;
  running: boolean;
  can_install: boolean;
  version: string;
  reason: string;
  /** What this build of the server can do. */
  supports_tls: boolean;
  supports_quota: boolean;
  accounts: FTPAccount[];
  sessions: FTPSession[];
  config_path: string;
  conflicts: FTPConflict[] | null;
  /**
   * Whether the ports FTP needs are actually admitted by the firewall. A
   * blocked passive range is a server that accepts the login and then hangs on
   * the first directory listing.
   */
  firewall_open: boolean;
  firewall_reason: string;
  users: FTPUser[];
  settings: FTPSettings;
}

/** The result of creating an account. */
export interface FTPCreateResult {
  user: FTPUser;
  /**
   * Returned exactly once, and only when the panel generated it. Nothing
   * stores it, so this response is the only chance to see it.
   */
  password?: string;
}

/** A change to an account. Omitted fields are left alone. */
export interface FTPUserChange {
  home_subpath?: string;
  access_level?: 'full' | 'readonly';
  quota_mb?: number;
  suspended?: boolean;
  password?: string;
}

/** A change to the server's settings. Omitted fields are left alone. */
export interface FTPSettingsChange {
  passive_from?: number;
  passive_to?: number;
  tls_website_id?: string;
  require_tls?: boolean;
  masquerade_address?: string;
  max_clients?: number;
}

/* -------------------------------------------------------------------- DNS */

/** The record types the panel writes. */
export type DNSRecordType =
  | 'A'
  | 'AAAA'
  | 'CNAME'
  | 'MX'
  | 'TXT'
  | 'NS'
  | 'CAA'
  | 'SRV'
  | 'PTR';

/** One resource record. */
export interface DNSRecord {
  id: string;
  zone_id: string;
  /** Relative to the zone, or "@" for the zone itself. */
  name: string;
  type: DNSRecordType;
  /** Zero means the zone's default. */
  ttl: number;
  value: string;
  priority: number;
  weight: number;
  port: number;
  flags: number;
  tag: string;
  provider: string;
  external_id?: string;
  /**
   * A record the panel maintains for itself — a subdomain's address record in
   * its parent's zone. Shown, and not editable by hand.
   */
  managed: boolean;
  created_at: string;
  updated_at: string;
}

/** One zone the panel serves. */
export interface DNSZone {
  id: string;
  server_id: string;
  website_id?: string;
  name: string;
  kind: 'master' | 'slave';
  reverse_network?: string;
  primary_ns: string;
  hostmaster: string;
  /**
   * The panel's own serial, written into the zone file.
   *
   * Not the one the server is answering with: on a signed zone named keeps a
   * second serial on the signed copy and it runs ahead. The served one is in
   * DNSZoneState.
   */
  serial: number;
  refresh: number;
  retry: number;
  expire: number;
  minimum: number;
  ttl: number;
  nameservers: string[];
  dnssec: boolean;
  allow_transfer: string[];
  also_notify: string[];
  masters: string[];
  created_at: string;
  updated_at: string;
  website_domain?: string;
  record_count: number;
  records?: DNSRecord[];
}

/** What the running server says about one zone. */
export interface DNSZoneState {
  zone: string;
  type: string;
  serial: number;
  signed_serial: number;
  secure: boolean;
  last_loaded: string;
  last_transfer: string;
  loaded: boolean;
  reason: string;
}

/** One DNSSEC key. */
export interface DNSKey {
  id: number;
  algorithm: string;
  role: string;
  published: boolean;
  key_signing: boolean;
  zone_signing: boolean;
  rollover: string;
}

/** The record a parent zone's registrar needs. */
export interface DNSDelegationSigner {
  key_tag: number;
  algorithm: number;
  digest_type: number;
  digest: string;
  record: string;
}

/** A zone's signing state. */
export interface DNSSigningStatus {
  zone: string;
  policy: string;
  keys: DNSKey[] | null;
  ds: DNSDelegationSigner[] | null;
  reason: string;
}

/** A zone with everything the editor shows. */
export interface DNSZoneDetail {
  zone: DNSZone;
  state: DNSZoneState;
  signing: DNSSigningStatus;
}

/** The name server's settings. */
export interface DNSSettings {
  server_id: string;
  listen_on: string[];
  allow_transfer: string[];
  dnssec_policy: string;
  default_ns: string[];
  default_ttl: number;
  hostmaster: string;
}

/** A remote DNS provider the panel can publish to. */
export interface DNSProvider {
  id: string;
  server_id: string;
  kind: string;
  label: string;
  account_id?: string;
  last_sync_at?: string;
  last_sync_status?: string;
  last_sync_error?: string;
  created_at: string;
}

/** What a push to a provider did. */
export interface DNSSyncResult {
  remote_zone_id: string;
  created: number;
  updated: number;
  deleted: number;
  unchanged: number;
  skipped?: string[];
}

/** Everything the DNS page shows. */
export interface DNSOverview {
  available: boolean;
  running: boolean;
  can_install: boolean;
  supports_dnssec: boolean;
  version: string;
  reason: string;
  config_path: string;
  include_path: string;
  zone_dir: string;
  /** Whether named.conf is the panel's own, and whether it reads the panel's zones at all. */
  managed_config: boolean;
  config_included: boolean;
  recursion: boolean;
  listen_on: string[] | null;
  /** The zones the panel has recorded. */
  zones: DNSZone[] | null;
  /**
   * The zones the *host* is configured to serve.
   *
   * Not the same list: a name here and not in `zones` is a zone the next
   * reconcile removes.
   */
  host_zones: string[] | null;
  firewall_open: boolean;
  firewall_reason: string;
  warnings: string[] | null;
  settings: DNSSettings;
  providers: DNSProvider[] | null;
}

/** A record to create or replace. */
export interface DNSRecordInput {
  name: string;
  type: DNSRecordType;
  ttl?: number;
  value: string;
  priority?: number;
  weight?: number;
  port?: number;
  flags?: number;
  tag?: string;
}

/** A change to a zone. Omitted fields are left alone. */
export interface DNSZoneChange {
  primary_ns?: string;
  hostmaster?: string;
  refresh?: number;
  retry?: number;
  expire?: number;
  minimum?: number;
  ttl?: number;
  nameservers?: string[];
  dnssec?: boolean;
  allow_transfer?: string[];
  also_notify?: string[];
  masters?: string[];
  website_id?: string;
}
