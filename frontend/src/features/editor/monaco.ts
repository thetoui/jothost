import { loader } from '@monaco-editor/react';
import * as monaco from 'monaco-editor';

import editorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker';
import cssWorker from 'monaco-editor/esm/vs/language/css/css.worker?worker';
import htmlWorker from 'monaco-editor/esm/vs/language/html/html.worker?worker';
import jsonWorker from 'monaco-editor/esm/vs/language/json/json.worker?worker';
import tsWorker from 'monaco-editor/esm/vs/language/typescript/ts.worker?worker';

/**
 * Monaco is bundled, never fetched.
 *
 * @monaco-editor/react loads the editor from a CDN by default. For a control
 * panel that is unacceptable twice over: the panel manages a server as root and
 * must not execute third-party script delivered at page load, and a panel is
 * routinely reached on a private network where that fetch simply fails. Pointing
 * the loader at the bundled copy keeps everything on the panel's own origin and
 * makes the editor work offline.
 */
let configured = false;

export function configureMonaco(): void {
  if (configured) {
    return;
  }
  configured = true;

  // Workers are wired the same way and for the same reason: Monaco spawns them
  // by URL, and the default URL is the CDN's.
  window.MonacoEnvironment = {
    getWorker(_workerId: string, label: string) {
      switch (label) {
        case 'json':
          return new jsonWorker();
        case 'css':
        case 'scss':
        case 'less':
          return new cssWorker();
        case 'html':
        case 'handlebars':
        case 'razor':
          return new htmlWorker();
        case 'typescript':
        case 'javascript':
          return new tsWorker();
        default:
          return new editorWorker();
      }
    },
  };

  loader.config({ monaco });
}
