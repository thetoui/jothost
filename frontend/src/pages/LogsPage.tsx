import { useEffect, useMemo, useRef, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import {
  Download,
  FileCode2,
  Globe,
  ScrollText,
  Search,
  Server,
  ShieldCheck,
} from 'lucide-react';

import { StatusPill } from '@/components/StatusPill';
import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, TintedIcon } from '@/components/ui/Card';
import { SelectField, TextField, Toggle } from '@/components/ui/Field';
import { EmptyState, SkeletonRows } from '@/components/ui/Loading';
import { RequirePermission } from '@/features/auth/components/RequirePermission';
import { Permission } from '@/features/auth/permissions';
import { logsApi } from '@/features/logs/api';
import { useLogSources, useLogTail } from '@/features/logs/hooks';
import type { LogSource } from '@/types/api';

/** The groups, in the order an operator reads them. */
const groups: { key: LogSource['group']; title: string; icon: React.ReactNode }[] = [
  { key: 'web', title: 'Web', icon: <Globe className="h-4 w-4" /> },
  { key: 'runtime', title: 'Runtimes', icon: <FileCode2 className="h-4 w-4" /> },
  { key: 'system', title: 'System', icon: <Server className="h-4 w-4" /> },
  { key: 'panel', title: 'Panel', icon: <ShieldCheck className="h-4 w-4" /> },
];

/** How many lines to read at a time. */
const lineCounts = [100, 200, 500, 1000, 2000];

/**
 * LogsPage shows the host's logs.
 *
 * The picker on the left lists what this host has — including the logs it does
 * not have yet, greyed out, because "nginx has recorded no errors" and "this
 * panel does not offer that log" are different answers and an operator needs to
 * tell them apart.
 */
export function LogsPage() {
  const { data, isPending, isError, error } = useLogSources();
  const sources = useMemo(() => data?.sources ?? [], [data]);

  // A log can be linked to: the scheduled jobs page sends an operator here for
  // one job's output, and landing on whichever log happened to be first would
  // make that link useless.
  const [params, setParams] = useSearchParams();
  const requested = params.get('source') ?? '';

  const [selected, setSelected] = useState(requested);
  const [search, setSearch] = useState('');
  const [level, setLevel] = useState('');
  const [limit, setLimit] = useState(200);
  const [live, setLive] = useState(false);

  // The first log that exists is opened on arrival, so the page has something
  // on it rather than an empty frame and an instruction to click something.
  useEffect(() => {
    if (selected !== '' || sources.length === 0) return;
    const first = sources.find((source) => source.present);
    if (first) setSelected(first.key);
  }, [sources, selected]);

  // Changing the selection updates the address, so the page can be shared or
  // reloaded and show the same log.
  function choose(key: string) {
    setSelected(key);
    setParams(key === '' ? {} : { source: key }, { replace: true });
  }

  const source = sources.find((entry) => entry.key === selected);

  return (
    <div className="space-y-5">
      <header>
        <h1 className="text-xl font-semibold text-slate-900">Logs</h1>
        <p className="mt-1 text-sm text-slate-500">
          What this host has recorded, read straight from the files on it.
        </p>
      </header>

      {isError && (
        <Alert tone="danger" title="The logs could not be listed">
          {error instanceof Error ? error.message : 'Try again in a moment.'}
        </Alert>
      )}

      <div className="grid gap-5 lg:grid-cols-[18rem_minmax(0,1fr)]">
        <Card className="h-fit">
          <CardHeader
            title="Sources"
            icon={<TintedIcon tone="brand" icon={<ScrollText className="h-4 w-4" />} />}
          />
          <CardBody className="p-0">
            {isPending ? (
              <SkeletonRows rows={5} />
            ) : sources.length === 0 ? (
              <p className="px-5 py-4 text-sm text-slate-500">
                This host has no logs the panel can read.
              </p>
            ) : (
              groups
                .map((group) => ({
                  ...group,
                  entries: sources.filter((entry) => entry.group === group.key),
                }))
                .filter((group) => group.entries.length > 0)
                .map((group) => (
                  <div key={group.key} className="border-b border-surface-border last:border-0">
                    <p className="flex items-center gap-2 px-4 pb-1 pt-3 text-[0.6875rem] font-semibold uppercase tracking-wider text-slate-400">
                      {group.icon}
                      {group.title}
                    </p>
                    {group.entries.map((entry) => (
                      <SourceButton
                        key={entry.key}
                        source={entry}
                        selected={entry.key === selected}
                        onSelect={() => choose(entry.key)}
                      />
                    ))}
                  </div>
                ))
            )}
          </CardBody>
        </Card>

        {source ? (
          <LogViewer
            source={source}
            search={search}
            level={level}
            limit={limit}
            live={live}
            levels={data?.levels ?? []}
            onSearch={setSearch}
            onLevel={setLevel}
            onLimit={setLimit}
            onLive={setLive}
          />
        ) : (
          <Card>
            <EmptyState
              icon={<ScrollText className="h-6 w-6" />}
              title="Nothing selected"
              description="Choose a log on the left."
            />
          </Card>
        )}
      </div>
    </div>
  );
}

function SourceButton({
  source,
  selected,
  onSelect,
}: {
  source: LogSource;
  selected: boolean;
  onSelect: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onSelect}
      aria-current={selected ? 'true' : undefined}
      className={`flex w-full items-center justify-between gap-2 px-4 py-2 text-left text-sm transition-colors ${
        selected ? 'bg-brand-50 text-brand-700' : 'text-slate-700 hover:bg-slate-50'
      }`}
    >
      <span className="min-w-0">
        <span className="block truncate font-medium">{source.label}</span>
        <span className="block truncate text-xs text-slate-400">
          {source.present ? formatBytes(source.size) : 'not on this host'}
        </span>
      </span>
    </button>
  );
}

interface ViewerProps {
  source: LogSource;
  search: string;
  level: string;
  limit: number;
  live: boolean;
  levels: string[];
  onSearch: (value: string) => void;
  onLevel: (value: string) => void;
  onLimit: (value: number) => void;
  onLive: (value: boolean) => void;
}

function LogViewer({
  source,
  search,
  level,
  limit,
  live,
  levels,
  onSearch,
  onLevel,
  onLimit,
  onLive,
}: ViewerProps) {
  // The search box is typed into, and each keystroke would otherwise be a
  // request. The value sent is settled rather than in-progress.
  const [draft, setDraft] = useState(search);
  useEffect(() => setDraft(search), [source.key, search]);
  useEffect(() => {
    const timer = setTimeout(() => onSearch(draft.trim()), 350);
    return () => clearTimeout(timer);
  }, [draft, onSearch]);

  const tail = useLogTail({ key: source.key, search, level, limit, live });

  const viewport = useRef<HTMLDivElement>(null);
  // Following a log means watching the end of it, so new output scrolls into
  // view. Only while following: yanking the viewport to the bottom while
  // somebody is reading further up is the behaviour every log viewer gets
  // wrong.
  useEffect(() => {
    if (!live || !viewport.current) return;
    viewport.current.scrollTop = viewport.current.scrollHeight;
  }, [tail.lines, live]);

  return (
    <Card>
      <CardHeader
        title={source.label}
        description={source.present ? source.path : 'This host does not have this log.'}
        action={
          <RequirePermission permission={Permission.ServerView}>
            <Button
              variant="secondary"
              disabled={!source.present || source.size === 0}
              onClick={() => void download(source.key)}
              icon={<Download aria-hidden="true" className="h-4 w-4" />}
            >
              Download
            </Button>
          </RequirePermission>
        }
      />

      <CardBody className="space-y-3">
        <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_9rem_7rem]">
          <TextField
            id="log-search"
            label="Search"
            value={draft}
            adornment={<Search aria-hidden="true" className="h-4 w-4" />}
            placeholder="Text to look for"
            onChange={(event) => setDraft(event.target.value)}
          />
          <SelectField
            id="log-level"
            label="Level"
            value={level}
            onChange={(event) => onLevel(event.target.value)}
          >
            <option value="">Any level</option>
            {levels.map((value) => (
              <option key={value} value={value}>
                {value}
              </option>
            ))}
          </SelectField>
          <SelectField
            id="log-limit"
            label="Lines"
            value={String(limit)}
            onChange={(event) => onLimit(Number(event.target.value))}
          >
            {lineCounts.map((value) => (
              <option key={value} value={value}>
                {value}
              </option>
            ))}
          </SelectField>
        </div>

        <div className="flex flex-wrap items-center gap-3">
          <Toggle
            id="log-live"
            label="Follow"
            checked={live}
            onChange={onLive}
            disabled={!source.present}
          />
          {live && <StatusPill label="Live" tone="ok" dot pulse />}
          <span className="text-xs text-slate-500">
            {tail.lines.length} line{tail.lines.length === 1 ? '' : 's'}
            {tail.filtered > 0 && <> · {tail.filtered} hidden by the filters</>}
          </span>
        </div>

        {tail.error && (
          <Alert tone="danger" title="The log could not be read">
            {tail.error}
          </Alert>
        )}

        {tail.rotated && (
          <Alert tone="warning" title="The file was replaced while you were reading it">
            The log was rotated or truncated on the host, so what came before is in the
            previous file. What is shown below is the new one from its beginning.
          </Alert>
        )}

        {tail.partial && !tail.error && (
          <p className="text-xs text-slate-500">
            This log is larger than one read can carry, so this is its end. Download it for
            the whole file.
          </p>
        )}

        <div
          ref={viewport}
          role="log"
          aria-label={`${source.label} contents`}
          aria-live="off"
          className="max-h-[32rem] overflow-auto rounded-md border border-surface-border bg-slate-900 p-3 font-mono text-xs leading-relaxed text-slate-200"
        >
          {tail.loading ? (
            <p className="text-slate-400">Reading…</p>
          ) : tail.lines.length === 0 ? (
            <p className="text-slate-400">
              {!source.present
                ? 'This host does not have this log.'
                : search !== '' || level !== ''
                  ? 'Nothing in this log matches the filters.'
                  : 'This log is empty.'}
            </p>
          ) : (
            tail.lines.map((line) => (
              <div
                key={`${line.offset}-${line.text.slice(0, 24)}`}
                className={`whitespace-pre-wrap break-all ${toneFor(line.level)}`}
              >
                {line.text}
                {line.truncated && <span className="text-slate-500"> … (line truncated)</span>}
              </div>
            ))
          )}
        </div>
      </CardBody>
    </Card>
  );
}

/** toneFor colours a line by severity. */
function toneFor(level: string): string {
  switch (level) {
    case 'error':
      return 'text-rose-300';
    case 'warn':
      return 'text-amber-300';
    case 'debug':
      return 'text-slate-400';
    default:
      return 'text-slate-200';
  }
}

/**
 * download saves a log through the API client rather than by navigating.
 *
 * A plain link would carry no Authorization header and be refused; putting the
 * token in the URL instead would write it into every proxy log between the
 * browser and the panel.
 */
async function download(key: string): Promise<void> {
  const blob = await logsApi.download(key);
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = url;
  anchor.download = `${key}.log`;
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  URL.revokeObjectURL(url);
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}
