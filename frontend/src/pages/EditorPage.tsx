import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import Editor from '@monaco-editor/react';
import { Code2, RotateCcw, Save } from 'lucide-react';

import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader } from '@/components/ui/Card';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { EmptyState, Spinner } from '@/components/ui/Loading';
import { Toggle } from '@/components/ui/Field';
import { hasPermission, Permission } from '@/features/auth/permissions';
import { useProfile } from '@/features/auth/hooks';
import { editorApi } from '@/features/editor/api';
import { EditorTabs } from '@/features/editor/components/EditorTabs';
import { FileTree } from '@/features/editor/components/FileTree';
import {
  isConflict,
  useAutoSave,
  useEditorError,
  useOpenFile,
  useSaveFile,
  useSaveShortcut,
  useTabState,
  useUnsavedGuard,
} from '@/features/editor/hooks';
import { configureMonaco } from '@/features/editor/monaco';
import { isDirty, useEditorStore, type EditorTab } from '@/stores/editorStore';
import type { FileEntry } from '@/types/api';

/** ROOT matches the Agent's allowed root; see FilesPage for why it is fixed. */
const ROOT = '/var/www';

configureMonaco();

export function EditorPage() {
  const { data: profile } = useProfile();
  const canWrite = hasPermission(profile, Permission.FileWrite);

  const { tabs, active, unsaved } = useTabState();
  const activate = useEditorStore((state) => state.activate);
  const close = useEditorStore((state) => state.close);
  const update = useEditorStore((state) => state.update);
  const reload = useEditorStore((state) => state.reload);
  const autoSave = useEditorStore((state) => state.autoSave);
  const setAutoSave = useEditorStore((state) => state.setAutoSave);

  const openFile = useOpenFile();
  const saveFile = useSaveFile();
  const { error, report, clear } = useEditorError();

  const [conflict, setConflict] = useState<EditorTab | null>(null);
  const [closing, setClosing] = useState<EditorTab | null>(null);

  useUnsavedGuard(unsaved.length > 0);

  const open = useCallback(
    (entry: FileEntry) => {
      clear();
      openFile.mutate(entry.path, { onError: report });
    },
    [openFile, report, clear],
  );

  const save = useCallback(
    (tab: EditorTab, force = false) => {
      clear();
      saveFile.mutate(
        { path: tab.path, content: tab.content, checksum: tab.checksum, force },
        {
          onError: (thrown) => {
            if (isConflict(thrown)) {
              // Not reported as a generic failure: the person needs to decide
              // between their version and the one on disk, and that is a
              // question, not an error message.
              setConflict(tab);
              return;
            }
            report(thrown);
          },
          onSuccess: () => setConflict(null),
        },
      );
    },
    [saveFile, report, clear],
  );

  // The file manager hands a file over as ?path=. Opening it here rather than
  // making someone find it again in the tree is the whole point of the handoff.
  const [params] = useSearchParams();
  const requested = params.get('path');
  const requestedOnce = useRef<string | null>(null);

  useEffect(() => {
    if (!requested || requestedOnce.current === requested) {
      return;
    }
    requestedOnce.current = requested;
    clear();
    openFile.mutate(requested, { onError: report });
    // openFile is a stable mutation object; re-running on it would reopen the
    // file on every render and discard whatever had been typed.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [requested]);

  // Auto-save pauses while a conflict is unanswered: retrying on a timer would
  // spam a question the person has not had a chance to answer.
  useAutoSave(active, autoSave && canWrite, (tab) => save(tab), conflict !== null);
  useSaveShortcut(() => {
    if (active && canWrite && isDirty(active)) {
      save(active);
    }
  }, active !== null);

  const dirty = active !== null && isDirty(active);

  return (
    <div className="space-y-4">
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold text-slate-900">Code editor</h1>
          <p className="text-sm text-slate-500">
            Edit the files under {ROOT} in place.
          </p>
        </div>

        <div className="flex flex-wrap items-center gap-3">
          {canWrite && (
            <Toggle
              id="editor-autosave"
              label="Auto-save"
              checked={autoSave}
              onChange={setAutoSave}
            />
          )}
          <Button
            variant="primary"
            icon={<Save aria-hidden="true" className="h-4 w-4" />}
            disabled={!canWrite || !dirty}
            loading={saveFile.isPending}
            onClick={() => active && save(active)}
          >
            Save
          </Button>
        </div>
      </header>

      {!canWrite && (
        <Alert tone="info">
          You can read these files but not change them. Saving needs the
          <code className="mx-1 rounded bg-surface-sunken px-1 py-0.5 text-xs">file.write</code>
          permission.
        </Alert>
      )}

      {error && (
        <Alert tone="danger" title="That did not work">
          {error}
        </Alert>
      )}

      <div className="grid gap-4 lg:grid-cols-[18rem,1fr]">
        <Card label="File tree">
          <CardHeader
            icon={<Code2 aria-hidden="true" className="h-4 w-4" />}
            title="Files"
            description={ROOT}
          />
          <CardBody className="max-h-[32rem] overflow-y-auto p-2">
            <FileTree root={ROOT} activePath={active?.path ?? null} onOpen={open} />
          </CardBody>
        </Card>

        <Card label="Editor">
          <EditorTabs
            tabs={tabs}
            activePath={active?.path ?? null}
            onActivate={activate}
            onClose={(tab) => {
              if (isDirty(tab)) {
                setClosing(tab);
                return;
              }
              close(tab.path);
            }}
          />

          {openFile.isPending ? (
            <div className="flex h-[28rem] items-center justify-center">
              <Spinner label="Opening the file" />
            </div>
          ) : active ? (
            <ActiveEditor tab={active} readOnly={!canWrite} onChange={update} />
          ) : (
            <div className="flex h-[28rem] items-center justify-center">
              <EmptyState
                icon={<Code2 aria-hidden="true" className="h-6 w-6" />}
                title="No file open"
                description="Pick a file from the tree to start editing. Press Ctrl+F to search inside it, Ctrl+H to replace."
              />
            </div>
          )}

          {active && <StatusBar tab={active} saving={saveFile.isPending} />}
        </Card>
      </div>

      <ConfirmDialog
        open={closing !== null}
        onClose={() => setClosing(null)}
        destructive
        title={`Close ${closing?.name} without saving?`}
        description="The changes you made to this file will be lost."
        confirmLabel="Discard changes"
        cancelLabel="Keep editing"
        onConfirm={() => {
          if (closing) {
            close(closing.path);
          }
          setClosing(null);
        }}
      />

      <ConflictDialog
        tab={conflict}
        saving={saveFile.isPending}
        onClose={() => setConflict(null)}
        onOverwrite={(tab) => save(tab, true)}
        onReload={async (tab) => {
          const fresh = await editorApi.open(tab.path);
          reload(tab.path, fresh.content, fresh.checksum);
          setConflict(null);
        }}
      />
    </div>
  );
}

interface ActiveEditorProps {
  tab: EditorTab;
  readOnly: boolean;
  onChange: (path: string, content: string) => void;
}

function ActiveEditor({ tab, readOnly, onChange }: ActiveEditorProps) {
  return (
    <Editor
      height="28rem"
      path={tab.path}
      language={tab.language}
      value={tab.content}
      onChange={(value) => onChange(tab.path, value ?? '')}
      options={{
        readOnly,
        // Monaco's own find widget is what satisfies search and replace: it
        // handles regex, case sensitivity and whole-word already, and a
        // hand-rolled box beside it would be a worse version of the same thing.
        find: { addExtraSpaceOnTop: false, seedSearchStringFromSelection: 'selection' },
        minimap: { enabled: false },
        scrollBeyondLastLine: false,
        fontSize: 13,
        tabSize: 2,
        renderWhitespace: 'selection',
        // The file's own line endings are preserved, so saving a CRLF file does
        // not rewrite every line of it.
        automaticLayout: true,
      }}
    />
  );
}

function StatusBar({ tab, saving }: { tab: EditorTab; saving: boolean }) {
  const lines = useMemo(() => tab.content.split('\n').length, [tab.content]);

  return (
    <div className="flex flex-wrap items-center justify-between gap-2 border-t border-surface-border px-3 py-1.5 text-xs text-slate-500">
      <span className="truncate font-mono">{tab.path}</span>
      <span className="flex items-center gap-3">
        <span>{tab.language}</span>
        <span>{tab.endOfLine.toUpperCase()}</span>
        <span>{lines} lines</span>
        <span>
          {tab.mode} {tab.owner}
        </span>
        {saving ? (
          <span className="text-brand-700">Saving…</span>
        ) : isDirty(tab) ? (
          <span className="text-brand-700">Unsaved</span>
        ) : (
          <span>Saved</span>
        )}
      </span>
    </div>
  );
}

interface ConflictDialogProps {
  tab: EditorTab | null;
  saving: boolean;
  onClose: () => void;
  onOverwrite: (tab: EditorTab) => void;
  onReload: (tab: EditorTab) => void;
}

/**
 * ConflictDialog appears when the file changed underneath an open editor.
 *
 * Both options are destructive in opposite directions, so neither is the
 * default and both say plainly what is lost.
 */
function ConflictDialog({ tab, saving, onClose, onOverwrite, onReload }: ConflictDialogProps) {
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (tab === null) {
      setBusy(false);
    }
  }, [tab]);

  return (
    <ConfirmDialog
      open={tab !== null}
      onClose={onClose}
      loading={saving || busy}
      title="This file changed since you opened it"
      description="Someone or something else wrote to this file. Choose which version to keep."
      confirmLabel="Overwrite with my version"
      cancelLabel="Cancel"
      destructive
      onConfirm={() => {
        if (tab) {
          onOverwrite(tab);
        }
      }}
    >
      <div className="mt-3 space-y-2 text-sm text-slate-600">
        <p className="font-mono text-xs">{tab?.path}</p>
        <p>
          Overwriting discards whatever was written to the file after you opened it.
          Reloading discards your unsaved edits instead.
        </p>
        <Button
          icon={<RotateCcw aria-hidden="true" className="h-4 w-4" />}
          disabled={busy || saving}
          onClick={() => {
            if (tab) {
              setBusy(true);
              onReload(tab);
            }
          }}
        >
          Reload from disk
        </Button>
      </div>
    </ConfirmDialog>
  );
}
