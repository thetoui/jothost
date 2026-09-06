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
  /** The operator's own nginx configuration for this site's server block. */
  nginx_directives: string;
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
  group: 'web' | 'runtime' | 'system' | 'panel' | 'mail';
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

/* ---------------------------------------------------------------- Updates */

/** One package update the host has waiting. */
export interface UpdatePackage {
  name: string;
  installed: string;
  available: string;
  /** Only meaningful when the check's `security_known` is true. */
  security: boolean;
  origin?: string;
}

/**
 * A package with something newer available that the host will not upgrade.
 *
 * Separate from the pending list on purpose: somebody pinned it, and listing it
 * as outstanding work would be a queue that never empties.
 */
export interface UpdateHeld {
  name: string;
  installed: string;
  available: string;
  reason: string;
}

/** One reading of what the host has waiting. */
export interface UpdateCheck {
  id: string;
  server_id: string;
  manager: string;
  /**
   * Whether the check actually worked.
   *
   * False means the lists below are *not known*, not empty — a check that could
   * not reach the repositories looks exactly like a host with nothing to do.
   */
  succeeded: boolean;
  reason?: string;
  /** Whether this host can distinguish security updates at all. */
  security_known: boolean;
  package_count: number;
  security_count: number;
  held_count: number;
  unavailable_repositories: number;
  stale_repositories: number;
  reboot_required: boolean;
  packages: UpdatePackage[] | null;
  held: UpdateHeld[] | null;
  checked_at: string;
}

/** One package an update moved. */
export interface UpdateChange {
  name: string;
  from: string;
  to: string;
}

/** One application of updates. */
export interface UpdateRun {
  id: string;
  server_id: string;
  trigger: 'manual' | 'scheduled' | 'revert';
  status: 'running' | 'succeeded' | 'failed';
  requested: string[];
  /** What actually moved, which is routinely more than was requested. */
  changes: UpdateChange[];
  output?: string;
  error?: string;
  reboot_required: boolean;
  started_at: string;
  finished_at?: string;
  requested_by?: string;
}

/** When and whether the panel applies updates by itself. */
export interface UpdateSettings {
  server_id: string;
  policy: 'off' | 'security' | 'all';
  check_interval_hours: number;
  /** -1 means every day. */
  day_of_week: number;
  hour: number;
  minute: number;
  excluded: string[];
  last_checked_at?: string;
  last_run_at?: string;
}

/** The pending list, filtered to the runtimes the panel manages. */
export interface RuntimeUpdates {
  php: UpdatePackage[] | null;
  node: UpdatePackage[] | null;
}

/** Everything the updates page shows. */
export interface UpdateOverview {
  check: UpdateCheck;
  /** False on a host nobody has checked yet, which is not "nothing to do". */
  has_check: boolean;
  settings: UpdateSettings;
  running?: UpdateRun;
  runtime: RuntimeUpdates;
  recent: UpdateRun[] | null;
}

/** A change to the automatic-update settings. */
export interface UpdateSettingsChange {
  policy?: string;
  check_interval_hours?: number;
  day_of_week?: number;
  hour?: number;
  minute?: number;
  excluded?: string[];
}

/* ------------------------------------------------------------- Monitoring */

/** The metrics an alert rule can watch. */
export type AlertMetric =
  | 'cpu'
  | 'memory'
  | 'disk'
  | 'swap'
  | 'load'
  | 'network_rx'
  | 'network_tx'
  | 'service';

/** One condition the panel watches. */
export interface AlertRule {
  id: string;
  server_id: string;
  name: string;
  metric: AlertMetric;
  /** Which instance: a mount point for disk, a service key for service. Empty means any. */
  target: string;
  comparison: 'above' | 'below';
  threshold: number;
  /**
   * How long the breach must last before it becomes an alert.
   *
   * Zero fires on the first reading, which is right for a service being down
   * and wrong for almost everything else.
   */
  for_seconds: number;
  severity: 'warning' | 'critical';
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

/** A condition that has been true for long enough to matter. */
export interface Alert {
  id: string;
  server_id: string;
  rule_id?: string;
  metric: AlertMetric;
  target: string;
  severity: 'warning' | 'critical';
  threshold?: number;
  status: 'open' | 'resolved';
  message: string;
  /** The reading that opened it, the worst since, and the most recent. */
  value?: number;
  worst?: number;
  last_value?: number;
  opened_at: string;
  last_seen_at: string;
  resolved_at?: string;
  /**
   * Acknowledging says "I know". It does not resolve the alert — the monitor
   * decides that, when the condition clears.
   */
  acknowledged_at?: string;
  acknowledged_by?: string;
}

/** One stretch of time a service spent in one state. */
export interface ServiceStateRecord {
  id: string;
  server_id: string;
  service: string;
  running: boolean;
  status: string;
  started_at: string;
  ended_at?: string;
}

/** A service and how long it has been as it is. */
export interface MonitoredService {
  service: string;
  running: boolean;
  status: string;
  since: string;
  for_seconds: number;
}

/** How many alerts are open. */
export interface AlertCounts {
  critical: number;
  warning: number;
  /** What nobody has said they are dealing with. */
  unacknowledged: number;
}

/** Everything the monitoring page shows. */
export interface MonitoringOverview {
  open: Alert[] | null;
  recent: Alert[] | null;
  rules: AlertRule[] | null;
  services: MonitoredService[] | null;
  counts: AlertCounts;
}

/** A rule to create or change. */
export interface AlertRuleInput {
  name?: string;
  metric?: AlertMetric;
  target?: string;
  comparison?: 'above' | 'below';
  threshold?: number;
  for_seconds?: number;
  severity?: 'warning' | 'critical';
  enabled?: boolean;
}

// ---------------------------------------------------------------- backups

/** What a backup is of. */
export type BackupType = 'website' | 'database' | 'full';

/** Where a backup is written. */
export type DestinationKind = 'local' | 's3' | 'sftp';

/** What the host can actually do, which is not what the panel offers. */
export interface BackupCapabilities {
  available: boolean;
  reason?: string;
  local: boolean;
  s3: boolean;
  sftp: boolean;
  mysql_dump: boolean;
  postgres_dump: boolean;
  engines: string[] | null;
  work_dir?: string;
}

/** Where backups go. It never carries the credential, only whether one is set. */
export interface BackupDestination {
  id: string;
  server_id: string;
  name: string;
  kind: DestinationKind;
  config: Record<string, unknown>;
  has_credentials: boolean;
  last_check_at: string | null;
  last_check_ok: boolean | null;
  last_check_detail: string | null;
  created_at: string;
  updated_at: string;
}

/** One archive the panel has taken. */
export interface Backup {
  id: string;
  server_id: string;
  website_id: string | null;
  database_id: string | null;
  subject: string;
  type: BackupType;
  destination_id: string | null;
  destination: string;
  path: string | null;
  size_bytes: number | null;
  checksum: string | null;
  status: 'pending' | 'running' | 'completed' | 'failed' | 'deleting';
  /**
   * When the panel last read the archive back and found it intact.
   *
   * Null on a completed backup means the bytes were written and could not be
   * confirmed, which is not the same as a backup.
   */
  verified_at: string | null;
  verify_detail: string | null;
  manifest?: Record<string, unknown>;
  error: string | null;
  job_id: string | null;
  schedule_id: string | null;
  created_by: string | null;
  started_at: string | null;
  completed_at: string | null;
  created_at: string;
}

/** A standing instruction to take a backup. */
export interface BackupSchedule {
  id: string;
  server_id: string;
  name: string;
  type: BackupType;
  website_id: string | null;
  database_id: string | null;
  destination_id: string;
  destination_name: string;
  hour: number;
  minute: number;
  /** 0-6 with Sunday first; -1 means every day. */
  day_of_week: number;
  retention_days: number;
  keep_last: number;
  enabled: boolean;
  last_run_at: string | null;
  last_status: string | null;
  last_backup_id: string | null;
  created_at: string;
  updated_at: string;
}

/** How many backups a server has, and how many are known to be intact. */
export interface BackupStats {
  total: number;
  completed: number;
  verified: number;
  failed: number;
  running: number;
  bytes: number;
  latest_at: string | null;
}

/** Everything the backups page shows. */
export interface BackupOverview {
  capabilities: BackupCapabilities;
  backups: Backup[] | null;
  destinations: BackupDestination[] | null;
  schedules: BackupSchedule[] | null;
  stats: BackupStats;
  types: BackupType[];
  destination_kinds: DestinationKind[];
}

/** The outcome of reading a stored backup back. */
export interface BackupVerifyResult {
  key: string;
  ok: boolean;
  size: number;
  bytes_read: number;
  checksum: string;
  expected?: string;
  detail?: string;
  members: number;
}

/** A destination to create or change. */
export interface BackupDestinationInput {
  name: string;
  kind: DestinationKind;
  directory?: string;
  endpoint?: string;
  region?: string;
  bucket?: string;
  prefix?: string;
  access_key?: string;
  secret_key?: string;
  path_style?: boolean;
  /** Accepts a plain-http endpoint that is not on this machine. */
  allow_insecure?: boolean;
  host?: string;
  port?: number;
  user?: string;
  path?: string;
  private_key?: string;
  host_key?: string;
}

/** A schedule to create or change. */
export interface BackupScheduleInput {
  name: string;
  type: BackupType;
  website_id?: string;
  database_id?: string;
  destination_id: string;
  hour: number;
  minute: number;
  day_of_week: number;
  retention_days: number;
  keep_last: number;
  enabled?: boolean;
}

// -------------------------------------------------------------- security

/** How bad a finding is. */
export type FindingSeverity = 'critical' | 'high' | 'medium' | 'low' | 'info';

/** What the panel checks. */
export type SecurityScanner =
  | 'ssh'
  | 'firewall'
  | 'ports'
  | 'permissions'
  | 'ssl'
  | 'updates'
  | 'fail2ban';

/** One thing wrong with the host. */
export interface SecurityFinding {
  id: string;
  server_id: string;
  scanner: SecurityScanner;
  severity: FindingSeverity;
  category: string;
  title: string;
  description: string;
  /** What to do about it. A finding without a next step is a nag. */
  remediation: string;
  fingerprint: string;
  status: 'open' | 'accepted' | 'resolved';
  metadata?: Record<string, unknown>;
  /** How long this host has been wrong about this. */
  first_seen_at: string;
  last_seen_at: string;
  resolved_at: string | null;
  accepted_at: string | null;
  accepted_by: string | null;
  accepted_reason: string | null;
  accepted_severity: string | null;
  created_at: string;
}

/** What one scanner did. */
export interface ScannerOutcome {
  scanner: SecurityScanner;
  /**
   * False when the scanner could not answer.
   *
   * Its findings are then left alone rather than resolved, and it counts
   * towards neither a pass nor a failure in the score.
   */
  ran: boolean;
  reason?: string;
  findings: number;
  duration_ms: number;
}

/** One run of the scanners. */
export interface SecurityScan {
  id: string;
  server_id: string;
  score: number;
  checks_run: number;
  checks_total: number;
  critical: number;
  high: number;
  medium: number;
  low: number;
  info: number;
  accepted: number;
  resolved: number;
  scanners: ScannerOutcome[] | null;
  duration_ms: number;
  triggered_by: string | null;
  created_at: string;
}

/** The computed posture of the host. */
export interface SecurityScore {
  value: number;
  /** A word rather than a letter: it says what to do with the number. */
  grade: string;
  checks_run: number;
  checks_total: number;
  complete: boolean;
  summary: string;
}

/** How many live findings there are, by severity. */
export interface SecurityCounts {
  critical: number;
  high: number;
  medium: number;
  low: number;
  info: number;
  open: number;
  /** Never hidden: a score carried by accepted risk is not a clean one. */
  accepted: number;
}

/** Everything the Security Center shows. */
export interface SecurityOverview {
  score: SecurityScore;
  counts: SecurityCounts;
  findings: SecurityFinding[] | null;
  accepted: SecurityFinding[] | null;
  /** Null when this host has never been scanned, which is its own fact. */
  last_scan: SecurityScan | null;
  scanners: ScannerOutcome[] | null;
  severities: FindingSeverity[];
}

// --------------------------------------------------------- notifications

/** Where notifications go. */
export type ChannelKind = 'email' | 'telegram' | 'line';

/** How bad something has to be before a channel hears about it. */
export type NotifySeverity = 'critical' | 'high' | 'warning' | 'info';

/** What the panel raises. */
export type NotificationEventKind =
  | 'alert.opened'
  | 'alert.resolved'
  | 'backup.failed'
  | 'ssl.expiring'
  | 'security.finding'
  | 'test';

/**
 * A channel. It never carries the credential, only what the panel knows about
 * whether it works.
 */
export interface NotificationChannel {
  id: string;
  server_id: string;
  name: string;
  kind: ChannelKind;
  config: Record<string, unknown>;
  enabled: boolean;
  min_severity: NotifySeverity;
  /** Empty means every kind. */
  kinds: string[] | null;
  /** Null means the panel has never got a message through this. */
  last_success_at: string | null;
  last_failure_at: string | null;
  last_error: string | null;
  /** Consecutive failures since the last success. */
  failure_streak: number;
  created_at: string;
  updated_at: string;
}

/** Something that happened worth telling somebody about. */
export interface NotificationEvent {
  id: string;
  server_id: string;
  source: string;
  kind: NotificationEventKind;
  severity: NotifySeverity;
  title: string;
  body: string;
  link?: string;
  dedupe_key: string;
  metadata?: Record<string, unknown>;
  created_at: string;
}

/** One attempt to get one event to one channel. */
export interface NotificationDelivery {
  id: string;
  event_id: string;
  channel_id: string;
  status: 'pending' | 'sent' | 'failed';
  attempts: number;
  next_attempt_at: string;
  last_error: string | null;
  sent_at: string | null;
  created_at: string;
  updated_at: string;
  channel_name?: string;
  channel_kind?: ChannelKind;
  event_title?: string;
  event_kind?: NotificationEventKind;
  severity?: NotifySeverity;
}

/** What has been getting through. */
export interface DeliveryStats {
  sent: number;
  failed: number;
  pending: number;
  /**
   * Channels currently failing. The number the page leads with, because it is
   * the only way the panel can say that notifications are not arriving.
   */
  broken_channels: number;
  /** Channels nobody has ever got a message through. */
  untested_channels: number;
}

/** Everything the notifications page shows. */
export interface NotificationOverview {
  channels: NotificationChannel[] | null;
  deliveries: NotificationDelivery[] | null;
  events: NotificationEvent[] | null;
  stats: DeliveryStats;
  channel_kinds: ChannelKind[];
  event_kinds: NotificationEventKind[];
  severities: NotifySeverity[];
}

/** A channel to create or change. */
export interface NotificationChannelInput {
  name: string;
  kind: ChannelKind;
  host?: string;
  port?: number;
  security?: string;
  username?: string;
  password?: string;
  from?: string;
  to?: string[];
  /** Accepts a plain-SMTP relay that is not on this machine. */
  allow_insecure?: boolean;
  token?: string;
  chat_id?: string;
  recipient?: string;
  min_severity?: NotifySeverity;
  kinds?: string[];
  enabled?: boolean;
}

// ---------------------------------------------------------------------- mail

/** How much of a domain's mail the world is asked to believe. */
export type SPFPolicy = 'none' | 'soft' | 'strict';
export type DMARCPolicy = 'off' | 'none' | 'quarantine' | 'reject';

/** The mail server's own settings. */
export interface MailSettings {
  server_id: string;
  enabled: boolean;
  hostname: string;
  tls_website_id?: string;
  require_tls: boolean;
  spam_enabled: boolean;
  spam_reject_score: number;
  virus_enabled: boolean;
  max_message_mb: number;
  webmail_website_id?: string;
  webmail_version?: string;
  created_at: string;
  updated_at: string;
}

/** One piece of the mail server. */
export interface MailDaemon {
  installed: boolean;
  running: boolean;
  version?: string;
  detail?: string;
}

/** One SMTP or IMAP service. */
export interface MailPort {
  name: string;
  port: number;
  configured: boolean;
  /** Whether anything is actually bound to it. Differs from `configured` when a
   * daemon failed to start, which is exactly what a status page has to show. */
  listening: boolean;
  requires_tls: boolean;
}

/** Whether this server will carry a stranger's mail. */
export interface MailRelayStatus {
  checked: boolean;
  open: boolean;
  detail?: string;
}

/** What the mail server is actually doing, read from the host. */
export interface MailStatus {
  available: boolean;
  reason?: string;
  can_install: boolean;
  postfix: MailDaemon;
  dovecot: MailDaemon;
  rspamd: MailDaemon;
  antivirus: MailDaemon;
  hostname: string;
  tls: {
    configured: boolean;
    certificate_path?: string;
    not_after?: string;
    detail?: string;
  };
  ports: MailPort[] | null;
  queue_length: number;
  queue_oldest_seconds: number;
  open_relay: MailRelayStatus;
  signing: string[] | null;
  map_type: string;
  warnings?: string[] | null;
  webmail?: {
    installed: boolean;
    version?: string;
    path?: string;
    detail?: string;
  };
}

/**
 * A mail domain, together with what the world can actually see of it.
 *
 * The second half is the point. A policy in the panel is a setting; the record
 * a receiving server fetches is the only thing with any effect, and the two can
 * disagree.
 */
export interface MailDomain {
  id: string;
  server_id: string;
  website_id?: string;
  domain: string;
  active: boolean;
  catch_all: string;
  dkim_selector: string;
  dkim_public_key?: string;
  dkim_created_at?: string;
  spf_policy: SPFPolicy;
  dmarc_policy: DMARCPolicy;
  dmarc_rua: string;
  created_at: string;
  updated_at: string;
  mailboxes: number;
  aliases: number;
  /** Whether this host holds the private signing key. */
  signing: boolean;
  /** Whether this host serves the domain's zone. When it does not, the four
   * checks below cannot be answered at all. */
  dns_managed: boolean;
  mx_published: boolean;
  spf_published: boolean;
  dkim_published: boolean;
  dmarc_published: boolean;
  problems?: string[] | null;
}

/** A mailbox with an autoresponder and how full it is. */
export interface Mailbox {
  id: string;
  domain_id: string;
  local_part: string;
  address: string;
  quota_mb: number;
  active: boolean;
  used_mb: number;
  /** False when the panel could not ask. Shown as unknown rather than as
   * empty: telling a customer their full mailbox has room is worse than
   * saying nothing. */
  quota_known: boolean;
  autoresponder?: MailAutoresponder;
  created_at: string;
  updated_at: string;
}

/** A forwarder. */
export interface MailAlias {
  id: string;
  domain_id: string;
  source: string;
  destination: string;
  active: boolean;
  created_at: string;
  updated_at: string;
}

/** A vacation reply. */
export interface MailAutoresponder {
  mailbox_id: string;
  subject: string;
  body: string;
  starts_at?: string;
  ends_at?: string;
  interval_days: number;
  active: boolean;
  created_at: string;
  updated_at: string;
}

/** Everything the mail page shows. */
export interface MailOverview {
  settings: MailSettings;
  status: MailStatus;
  domains: MailDomain[] | null;
  mailboxes: number;
  aliases: number;
}

/** A domain to create or change. */
export interface MailDomainInput {
  domain?: string;
  website_id?: string;
  active?: boolean;
  catch_all?: string;
  spf_policy?: SPFPolicy;
  dmarc_policy?: DMARCPolicy;
  dmarc_rua?: string;
}

/** A mailbox to create or change. */
export interface MailboxInput {
  local_part?: string;
  password?: string;
  quota_mb?: number;
  active?: boolean;
}

/** A forwarder to create. */
export interface MailAliasInput {
  source: string;
  destination: string;
  active?: boolean;
}

/** A vacation reply to set. */
export interface MailAutoresponderInput {
  subject: string;
  body: string;
  interval_days?: number;
  active?: boolean;
}

// ---------------------------------------------------------------- deployment

/** How a push reaches the panel. */
export type DeployProvider = 'none' | 'github' | 'gitlab' | 'generic';

/** How a deployment was started. */
export type DeployTrigger = 'manual' | 'webhook' | 'rollback';

/** What a deployment is doing, or did. */
export type DeployStatus = 'pending' | 'running' | 'success' | 'failed' | 'cancelled';

/** One step of a deployment. Every kind but `script` is a fixed command line. */
export type DeployActionKind =
  | 'composer.install'
  | 'npm.ci'
  | 'npm.install'
  | 'npm.build'
  | 'artisan.migrate'
  | 'artisan.optimise'
  | 'script';

export interface DeployAction {
  id: string;
  repository_id: string;
  kind: DeployActionKind;
  position: number;
  enabled: boolean;
  created_at: string;
}

/** One deployment. */
export interface Deployment {
  id: string;
  repository_id: string;
  website_id: string;
  trigger: DeployTrigger;
  branch: string;
  commit_sha: string;
  commit_message: string;
  commit_author: string;
  previous_commit: string;
  status: DeployStatus;
  exit_code?: number;
  /** Omitted from lists: a build prints whatever the build printed, which
   * regularly includes a token in a URL. Reading it needs deploy.manage. */
  log?: string;
  log_truncated: boolean;
  rolled_back: boolean;
  rollback_error?: string;
  job_id?: string;
  started_at?: string;
  finished_at?: string;
  duration_ms?: number;
  created_at: string;
}

/** What the host says about a working tree. */
export interface DeployHostStatus {
  available: boolean;
  reason?: string;
  git_version?: string;
  cloned: boolean;
  commit?: string;
  branch?: string;
  message?: string;
  author?: string;
  /** Uncommitted changes a deployment would destroy. */
  dirty: boolean;
  dirty_files?: string[] | null;
  remote?: string;
  has_key: boolean;
  fingerprint?: string;
  /** Which template steps this host can actually run. */
  tools?: Record<string, boolean> | null;
  warnings?: string[] | null;
}

/** A website's source. */
export interface GitRepository {
  id: string;
  server_id: string;
  website_id: string;
  remote_url: string;
  branch: string;
  deploy_key_public?: string;
  deploy_key_fingerprint?: string;
  provider: DeployProvider;
  /** The address of the webhook, not a credential: it selects which repository
   * a push is about. The signature is what authenticates. */
  webhook_token?: string;
  auto_deploy: boolean;
  deploy_script: string;
  script_timeout_seconds: number;
  current_commit: string;
  current_branch: string;
  last_deployed_at?: string;
  created_at: string;
  updated_at: string;

  website: string;
  actions?: DeployAction[] | null;
  status?: DeployHostStatus;
  recent?: Deployment[] | null;
  webhook_url?: string;
}

/** A repository to create or change. */
export interface GitRepositoryInput {
  website_id: string;
  remote_url: string;
  branch?: string;
  provider?: DeployProvider;
  auto_deploy?: boolean;
  deploy_script?: string;
  script_timeout_seconds?: number;
  /** Set once and never returned. Empty on an update leaves the existing one. */
  webhook_secret?: string;
}

// ------------------------------------------------------------------ tenancy

/** Where an account sits in the hierarchy. */
export type AccountTier = 'admin' | 'reseller' | 'customer';

/** What happens when a limit is reached. */
export type Enforcement = 'hard' | 'soft';

/** Whether a plan is a plan or an add-on. */
export type PlanKind = 'plan' | 'addon';

/**
 * Whether the host is enforcing a subscription's resource limits.
 *
 * "declared" is the state worth reading carefully: the limits are recorded and
 * this host is not applying them. It is not a failure and it is not success.
 */
export type IsolationState = 'none' | 'applied' | 'declared' | 'failed';

/**
 * One set of quota limits.
 *
 * `null` means unlimited and `0` means none at all. They are opposite promises
 * and the UI must never render one as the other.
 */
export interface QuotaLimits {
  disk_mb: number | null;
  bandwidth_mb: number | null;
  max_websites: number | null;
  max_databases: number | null;
  max_mailboxes: number | null;
  max_ftp_users: number | null;
  max_cron_jobs: number | null;
  max_subdomains: number | null;
}

/** The resource caps a plan carries. */
export interface PlanIsolation {
  cpu_percent: number | null;
  memory_mb: number | null;
  io_weight: number | null;
}

/** A panel account seen through the hierarchy. */
export interface TenantAccount {
  id: string;
  username: string;
  email: string | null;
  tier: AccountTier;
  parent_id: string | null;
  parent_username: string | null;
  full_name: string | null;
  company: string | null;
  status: string;
  roles: string[] | null;
  created_at: string;
  last_login_at: string | null;
  subscriptions: number;
}

/** A service plan or an add-on. */
export interface ServicePlan {
  id: string;
  owner_user_id: string | null;
  owner_username: string | null;
  name: string;
  description: string;
  kind: PlanKind;
  limits: QuotaLimits;
  enforcement: Enforcement;
  isolation: PlanIsolation;
  subscriptions: number;
  created_at: string;
  updated_at: string;
}

/** An add-on attached to a subscription. */
export interface SubscriptionAddon {
  plan_id: string;
  name: string;
  quantity: number;
  limits: QuotaLimits;
}

/**
 * What a subscription is using.
 *
 * The counted figures are plain numbers because the panel wrote every row. The
 * measured ones are nullable because the answer may be "not measured", which
 * is not zero.
 */
export interface SubscriptionUsage {
  websites: number;
  databases: number;
  mailboxes: number;
  ftp_users: number;
  cron_jobs: number;
  subdomains: number;
  disk_bytes: number | null;
  bandwidth_bytes: number | null;
  period_start: string | null;
  measured_at: string | null;
  measure_error: string;
}

/** A website a subscription owns. */
export interface SubscriptionWebsite {
  id: string;
  primary_domain: string;
  status: string;
  document_root: string;
}

/** One customer's instance of a plan. */
export interface Subscription {
  id: string;
  owner_user_id: string;
  owner_username: string;
  plan_id: string;
  plan_name: string;
  name: string;
  status: 'active' | 'suspended';
  suspended_reason: string;
  suspended_at: string | null;
  slice_name: string;
  isolation_state: IsolationState;
  isolation_detail: string;
  isolation_applied_at: string | null;
  enforcement: Enforcement;
  limits: QuotaLimits;
  isolation: PlanIsolation;
  addons: SubscriptionAddon[];
  usage: SubscriptionUsage;
  websites: SubscriptionWebsite[];
  created_at: string;
  updated_at: string;
}

/** One record of somebody using the panel as somebody else. */
export interface ImpersonationRecord {
  id: string;
  actor_user_id: string;
  actor_username: string;
  subject_user_id: string;
  subject_username: string;
  reason: string;
  started_at: string;
  ended_at: string | null;
}

/** What the host can do about resource limits. */
export interface TenancyHostStatus {
  isolation_available: boolean;
  isolation_detail: string;
  /** Whether anything actually runs inside a subscription's slice. */
  placement: boolean;
}

/** The tenancy page's opening payload. */
export interface TenancyOverview {
  accounts: TenantAccount[];
  plans: ServicePlan[];
  subscriptions: Subscription[];
  host: TenancyHostStatus;
  actor: { tier: AccountTier; user_id: string; impersonated: boolean };
}

/** A plan to create or replace. */
export interface ServicePlanInput {
  name: string;
  description?: string;
  kind?: PlanKind;
  enforcement?: Enforcement;
  limits: Partial<QuotaLimits>;
  isolation: Partial<PlanIsolation>;
}

/** An account to create. */
export interface TenantAccountInput {
  username: string;
  password: string;
  tier: AccountTier;
  email?: string;
  full_name?: string;
  company?: string;
  parent_id?: string;
}

// ------------------------------------------------------------------- audit

/**
 * One recorded action.
 *
 * Almost everything is nullable, and each for its own reason rather than out
 * of caution: an action can have no actor (a failed login against a username
 * that does not exist), no name for the actor it had (the account was deleted
 * afterwards, and the trail keeps the row but cannot keep the name), and no
 * resource (a login belongs to nothing).
 */
export interface AuditEntry {
  id: string;
  action: string;
  user_id: string | null;
  username: string | null;
  resource_type: string | null;
  resource_id: string | null;
  ip_address: string | null;
  user_agent: string | null;
  status: string | null;
  metadata?: Record<string, unknown>;
  created_at: string;
}

export interface AuditEntryList {
  entries: AuditEntry[];
  /** How many are on this page. */
  count: number;
  /** How many matched the filter, which is the number worth showing. */
  total: number;
  limit: number;
  offset: number;
}

export interface AuditActionList {
  actions: string[];
  count: number;
}

// ------------------------------------------------------------------ server

/** One process on the host. */
export interface HostProcess {
  pid: number;
  ppid: number;
  name: string;
  /** The single-letter kernel state: R, S, D, Z, T. */
  state: string;
  /** The account name, or the numeric uid when it will not resolve. */
  user: string;
  uid: number;
  memory_rss_bytes: number;
  memory_percent: number;
  /**
   * Cumulative CPU time, not a rate. A per-process percentage needs two
   * samples and a one-shot listing cannot provide one, so this is seconds of
   * CPU used since the process started rather than how busy it is now.
   */
  cpu_time_seconds: number;
  threads: number;
  /** The full command line, truncated. Host data: render it as untrusted. */
  command: string;
}

export interface ProcessList {
  processes: HostProcess[];
  count: number;
}
