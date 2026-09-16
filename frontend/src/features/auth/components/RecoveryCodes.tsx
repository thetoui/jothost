import { useState } from 'react';
import { Check, Copy } from 'lucide-react';

import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';

interface RecoveryCodesProps {
  codes: string[];
  /** Called when the user says they have kept the codes. */
  onDone: () => void;
}

/**
 * RecoveryCodes shows a freshly issued set of recovery codes, once.
 *
 * The server never returns them again, so this is the only chance to keep
 * them. They are held in component state only: never in a store, local
 * storage or a query cache, where they would outlive the page.
 */
export function RecoveryCodes({ codes, onDone }: RecoveryCodesProps) {
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(codes.join('\n'));
      setCopied(true);
    } catch {
      // A browser that refuses the clipboard still shows the codes to copy by
      // hand, which is what the button is a shortcut for.
      setCopied(false);
    }
  }

  return (
    <div className="space-y-4">
      <Alert tone="warning" title="Save your recovery codes now">
        Each code signs you in once if you lose your authenticator. They are shown only this
        once. Keep them somewhere other than this device, such as a password manager.
      </Alert>

      <ol
        aria-label="Recovery codes"
        className="grid grid-cols-1 gap-x-6 gap-y-1.5 rounded-md bg-surface-muted px-4 py-3 font-mono text-sm text-ink-strong sm:grid-cols-2"
      >
        {codes.map((code) => (
          <li key={code} className="tabular-nums">
            {code}
          </li>
        ))}
      </ol>

      <div className="flex flex-wrap gap-2">
        <Button
          variant="secondary"
          onClick={() => void copy()}
          icon={
            copied ? (
              <Check aria-hidden="true" className="h-4 w-4" />
            ) : (
              <Copy aria-hidden="true" className="h-4 w-4" />
            )
          }
        >
          {copied ? 'Copied' : 'Copy codes'}
        </Button>
        <Button variant="primary" onClick={onDone}>
          I have saved these codes
        </Button>
      </div>
    </div>
  );
}
