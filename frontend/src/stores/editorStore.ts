import { create } from 'zustand';

/** One open file. */
export interface EditorTab {
  path: string;
  name: string;
  /** What is in the editor right now. */
  content: string;
  /** What was last read from or written to the server. */
  saved: string;
  /** Identifies the server's copy, for the stale-write check. */
  checksum: string;
  language: string;
  endOfLine: 'lf' | 'crlf';
  mode: string;
  owner: string;
}

interface EditorState {
  tabs: EditorTab[];
  activePath: string | null;
  /**
   * Auto-save is off by default, and deliberately so.
   *
   * This editor opens the live configuration of running websites. Saving a
   * half-typed wp-config.php or nginx include the moment someone stops typing
   * takes the site down between two keystrokes. Opting in is a decision the
   * person editing gets to make per session, not a default they discover.
   */
  autoSave: boolean;

  open: (tab: EditorTab) => void;
  close: (path: string) => void;
  closeAll: () => void;
  activate: (path: string) => void;
  update: (path: string, content: string) => void;
  markSaved: (path: string, checksum: string, content: string) => void;
  reload: (path: string, content: string, checksum: string) => void;
  setAutoSave: (enabled: boolean) => void;
}

/** isDirty reports whether a tab has unsaved changes. */
export function isDirty(tab: EditorTab): boolean {
  return tab.content !== tab.saved;
}

export const useEditorStore = create<EditorState>()((set) => ({
  tabs: [],
  activePath: null,
  autoSave: false,

  open: (tab) =>
    set((state) => {
      const existing = state.tabs.find((candidate) => candidate.path === tab.path);
      if (existing) {
        // Reopening a file that is already open focuses it rather than
        // replacing it: the second open would discard unsaved edits.
        return { activePath: tab.path };
      }
      return { tabs: [...state.tabs, tab], activePath: tab.path };
    }),

  close: (path) =>
    set((state) => {
      const index = state.tabs.findIndex((tab) => tab.path === path);
      if (index < 0) {
        return state;
      }
      const tabs = state.tabs.filter((tab) => tab.path !== path);

      let activePath = state.activePath;
      if (activePath === path) {
        // Focus the neighbour rather than nothing, so closing a tab does not
        // drop the person back to an empty screen mid-task.
        const next = tabs[index] ?? tabs[index - 1] ?? null;
        activePath = next ? next.path : null;
      }
      return { tabs, activePath };
    }),

  closeAll: () => set({ tabs: [], activePath: null }),

  activate: (path) => set({ activePath: path }),

  update: (path, content) =>
    set((state) => ({
      tabs: state.tabs.map((tab) => (tab.path === path ? { ...tab, content } : tab)),
    })),

  markSaved: (path, checksum, content) =>
    set((state) => ({
      tabs: state.tabs.map((tab) =>
        tab.path === path ? { ...tab, checksum, saved: content } : tab,
      ),
    })),

  reload: (path, content, checksum) =>
    set((state) => ({
      tabs: state.tabs.map((tab) =>
        tab.path === path ? { ...tab, content, saved: content, checksum } : tab,
      ),
    })),

  setAutoSave: (enabled) => set({ autoSave: enabled }),
}));
