import { useCallback, useEffect, useRef, useState } from 'react';
import { useMutation } from '@tanstack/react-query';

import { editorApi, type SaveInput } from '@/features/editor/api';
import { isDirty, useEditorStore, type EditorTab } from '@/stores/editorStore';
import { ApiError } from '@/services/apiClient';

/**
 * AUTOSAVE_DELAY_MS is how long typing must stop before an auto-save fires.
 *
 * Three seconds rather than a few hundred milliseconds: this writes to the live
 * configuration of running websites, and a save every time someone pauses
 * mid-word means a site spends the afternoon flickering through half-written
 * states.
 */
const AUTOSAVE_DELAY_MS = 3_000;

/** useOpenFile loads a file into a tab. */
export function useOpenFile() {
  const open = useEditorStore((state) => state.open);

  return useMutation({
    mutationFn: (path: string) => editorApi.open(path),
    onSuccess: (content) => {
      const tab: EditorTab = {
        path: content.path,
        name: content.path.split('/').pop() ?? content.path,
        content: content.content,
        saved: content.content,
        checksum: content.checksum,
        language: content.language,
        endOfLine: content.end_of_line,
        mode: content.mode,
        owner: content.owner,
      };
      open(tab);
    },
  });
}

/**
 * useSaveFile writes a tab back, carrying the checksum so a save cannot
 * silently overwrite someone else's change.
 */
export function useSaveFile() {
  const markSaved = useEditorStore((state) => state.markSaved);

  return useMutation({
    mutationFn: (input: SaveInput) => editorApi.save(input),
    onSuccess: (saved, input) => {
      markSaved(saved.path, saved.checksum, input.content);
    },
  });
}

/** isConflict reports whether a save failed because the file changed. */
export function isConflict(error: unknown): boolean {
  return error instanceof ApiError && error.status === 409;
}

/**
 * useAutoSave saves the active tab a few seconds after typing stops.
 *
 * It deliberately does nothing while a save is already in flight or the last
 * one failed: retrying a rejected write on a timer would either spam a conflict
 * the person has not seen yet, or quietly re-attempt a save they were about to
 * be asked about.
 */
export function useAutoSave(
  tab: EditorTab | null,
  enabled: boolean,
  save: (tab: EditorTab) => void,
  blocked: boolean,
) {
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const latest = useRef(save);
  latest.current = save;

  useEffect(() => {
    if (timer.current) {
      clearTimeout(timer.current);
      timer.current = null;
    }
    if (!enabled || !tab || !isDirty(tab) || blocked) {
      return;
    }

    timer.current = setTimeout(() => {
      latest.current(tab);
    }, AUTOSAVE_DELAY_MS);

    return () => {
      if (timer.current) {
        clearTimeout(timer.current);
        timer.current = null;
      }
    };
    // tab.content is the trigger: every keystroke restarts the countdown.
  }, [enabled, tab, blocked]);
}

/**
 * useUnsavedGuard warns before the browser closes with unsaved work.
 *
 * The browser decides what to show, and it will not show anything unless the
 * page has been interacted with — so this is a safety net, never the only thing
 * standing between someone and their unsaved edits.
 */
export function useUnsavedGuard(hasUnsaved: boolean) {
  useEffect(() => {
    if (!hasUnsaved) {
      return;
    }

    const handler = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = '';
    };
    window.addEventListener('beforeunload', handler);
    return () => window.removeEventListener('beforeunload', handler);
  }, [hasUnsaved]);
}

/**
 * useSaveShortcut wires Ctrl+S / Cmd+S.
 *
 * Monaco owns most keystrokes inside the editor, but the browser's own save
 * dialog is never what someone means when they press it here.
 */
export function useSaveShortcut(onSave: () => void, enabled: boolean) {
  const latest = useRef(onSave);
  latest.current = onSave;

  useEffect(() => {
    if (!enabled) {
      return;
    }

    const handler = (event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 's') {
        event.preventDefault();
        latest.current();
      }
    };
    window.addEventListener('keydown', handler);
    return () => window.removeEventListener('keydown', handler);
  }, [enabled]);
}

/** useTabState bundles the store selectors a page needs. */
export function useTabState() {
  const tabs = useEditorStore((state) => state.tabs);
  const activePath = useEditorStore((state) => state.activePath);

  const active = tabs.find((tab) => tab.path === activePath) ?? null;
  const unsaved = tabs.filter(isDirty);

  return { tabs, active, unsaved };
}

/** useEditorError turns whatever a mutation threw into a readable message. */
export function useEditorError() {
  const [error, setError] = useState<string | null>(null);

  const report = useCallback((thrown: unknown) => {
    if (thrown === null || thrown === undefined) {
      setError(null);
      return;
    }
    if (thrown instanceof ApiError || thrown instanceof Error) {
      setError(thrown.message);
      return;
    }
    setError('Something went wrong');
  }, []);

  return { error, report, clear: () => setError(null) };
}
