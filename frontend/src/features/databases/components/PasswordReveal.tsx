import { useEffect, useState } from 'react';
import { Check, Copy, Eye, EyeOff } from 'lucide-react';

import { Button } from '@/components/ui/Button';
import { useRevealDatabasePassword } from '@/features/databases/hooks';

interface PasswordRevealProps {
  userId: string;
  /**
   * A password already in hand — the one returned by the request that created
   * or rotated the account. When present it is shown without a round trip,
   * because this is the moment it is meant to be copied.
   */
  initial?: string | undefined;
}

/**
 * PasswordReveal shows a database account's password on request.
 *
 * It is hidden by default and fetched only when asked. The fetch is what writes
 * the audit record, so a page that displayed the password on load would make
 * "who read this credential" a question nobody could answer.
 */
export function PasswordReveal({ userId, initial }: PasswordRevealProps) {
  const [password, setPassword] = useState<string | null>(initial ?? null);
  const [visible, setVisible] = useState(Boolean(initial));
  const [copied, setCopied] = useState(false);
  const reveal = useRevealDatabasePassword();

  // A different account's row must not keep showing the previous one's
  // password, which is what happens when this component is reused by key.
  useEffect(() => {
    setPassword(initial ?? null);
    setVisible(Boolean(initial));
    setCopied(false);
  }, [userId, initial]);

  useEffect(() => {
    if (!copied) {
      return undefined;
    }
    const timer = window.setTimeout(() => setCopied(false), 2000);
    return () => window.clearTimeout(timer);
  }, [copied]);

  const show = () => {
    if (password !== null) {
      setVisible(true);
      return;
    }
    reveal.mutate(userId, {
      onSuccess: (result) => {
        setPassword(result.password);
        setVisible(true);
      },
    });
  };

  const copy = async () => {
    if (password === null) {
      return;
    }
    try {
      await navigator.clipboard.writeText(password);
      setCopied(true);
    } catch {
      // Clipboard access is denied outside a secure context and in some
      // browsers. The password is on screen either way, so this is a missing
      // convenience rather than a failure worth interrupting anyone over.
      setVisible(true);
    }
  };

  if (!visible) {
    return (
      <div className="flex items-center gap-2">
        <code className="font-mono text-xs text-ink-dim">••••••••••••</code>
        <Button
          size="sm"
          variant="ghost"
          onClick={show}
          loading={reveal.isPending}
          icon={<Eye aria-hidden="true" className="h-3.5 w-3.5" />}
        >
          Show
        </Button>
        {reveal.isError && (
          <span className="text-xs text-danger-600">
            {reveal.error instanceof Error ? reveal.error.message : 'Could not read it'}
          </span>
        )}
      </div>
    );
  }

  return (
    <div className="flex items-center gap-1.5">
      <code className="select-all break-all rounded bg-surface-sunken px-1.5 py-0.5 font-mono text-xs text-ink-strong">
        {password}
      </code>
      <Button
        size="sm"
        variant="ghost"
        onClick={() => void copy()}
        aria-label="Copy password"
        icon={
          copied ? (
            <Check aria-hidden="true" className="h-3.5 w-3.5 text-ok-600" />
          ) : (
            <Copy aria-hidden="true" className="h-3.5 w-3.5" />
          )
        }
      >
        {copied ? 'Copied' : 'Copy'}
      </Button>
      <Button
        size="sm"
        variant="ghost"
        onClick={() => setVisible(false)}
        aria-label="Hide password"
        icon={<EyeOff aria-hidden="true" className="h-3.5 w-3.5" />}
      >
        Hide
      </Button>
    </div>
  );
}
