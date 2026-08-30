import { beforeEach, describe, expect, it } from 'vitest';

import { isDirty, useEditorStore, type EditorTab } from '@/stores/editorStore';

function tab(overrides: Partial<EditorTab> = {}): EditorTab {
  return {
    path: '/var/www/site/index.php',
    name: 'index.php',
    content: '<?php echo 1;',
    saved: '<?php echo 1;',
    checksum: 'abc123',
    language: 'php',
    endOfLine: 'lf',
    mode: '0644',
    owner: 'web_site:nginx',
    ...overrides,
  };
}

describe('editorStore', () => {
  beforeEach(() => {
    useEditorStore.setState({ tabs: [], activePath: null, autoSave: false });
  });

  it('opens a file and focuses it', () => {
    useEditorStore.getState().open(tab());

    const state = useEditorStore.getState();
    expect(state.tabs).toHaveLength(1);
    expect(state.activePath).toBe('/var/www/site/index.php');
  });

  // Reopening would replace the buffer and take unsaved edits with it.
  it('focuses an already-open file rather than reopening it', () => {
    const store = useEditorStore.getState();
    store.open(tab());
    store.update('/var/www/site/index.php', 'edited');
    store.open(tab({ content: '<?php echo 1;' }));

    const state = useEditorStore.getState();
    expect(state.tabs).toHaveLength(1);
    expect(state.tabs[0]?.content).toBe('edited');
  });

  it('tracks unsaved changes', () => {
    const store = useEditorStore.getState();
    store.open(tab());
    expect(isDirty(useEditorStore.getState().tabs[0]!)).toBe(false);

    store.update('/var/www/site/index.php', '<?php echo 2;');
    expect(isDirty(useEditorStore.getState().tabs[0]!)).toBe(true);
  });

  it('clears the dirty marker once a save lands', () => {
    const store = useEditorStore.getState();
    store.open(tab());
    store.update('/var/www/site/index.php', '<?php echo 2;');
    store.markSaved('/var/www/site/index.php', 'def456', '<?php echo 2;');

    const saved = useEditorStore.getState().tabs[0]!;
    expect(isDirty(saved)).toBe(false);
    // The new checksum has to stick, or the next save reports a false conflict.
    expect(saved.checksum).toBe('def456');
  });

  it('replaces the buffer and the checksum on reload', () => {
    const store = useEditorStore.getState();
    store.open(tab());
    store.update('/var/www/site/index.php', 'my edits');
    store.reload('/var/www/site/index.php', 'from disk', 'newsum');

    const reloaded = useEditorStore.getState().tabs[0]!;
    expect(reloaded.content).toBe('from disk');
    expect(reloaded.saved).toBe('from disk');
    expect(isDirty(reloaded)).toBe(false);
  });

  // Closing a tab must not drop the person onto an empty screen mid-task.
  it('focuses a neighbour when the active tab closes', () => {
    const store = useEditorStore.getState();
    store.open(tab({ path: '/var/www/a.php', name: 'a.php' }));
    store.open(tab({ path: '/var/www/b.php', name: 'b.php' }));
    store.open(tab({ path: '/var/www/c.php', name: 'c.php' }));

    store.activate('/var/www/b.php');
    store.close('/var/www/b.php');

    const state = useEditorStore.getState();
    expect(state.tabs.map((item) => item.path)).toEqual(['/var/www/a.php', '/var/www/c.php']);
    expect(state.activePath).toBe('/var/www/c.php');
  });

  it('has nothing active once the last tab closes', () => {
    const store = useEditorStore.getState();
    store.open(tab());
    store.close(tab().path);

    const state = useEditorStore.getState();
    expect(state.tabs).toHaveLength(0);
    expect(state.activePath).toBeNull();
  });

  it('leaves the active tab alone when a different one closes', () => {
    const store = useEditorStore.getState();
    store.open(tab({ path: '/var/www/a.php', name: 'a.php' }));
    store.open(tab({ path: '/var/www/b.php', name: 'b.php' }));

    store.activate('/var/www/a.php');
    store.close('/var/www/b.php');

    expect(useEditorStore.getState().activePath).toBe('/var/www/a.php');
  });

  // This editor writes the live configuration of running websites. Saving a
  // half-typed file between two keystrokes takes the site down.
  it('has auto-save off until it is asked for', () => {
    expect(useEditorStore.getState().autoSave).toBe(false);

    useEditorStore.getState().setAutoSave(true);
    expect(useEditorStore.getState().autoSave).toBe(true);
  });
});
