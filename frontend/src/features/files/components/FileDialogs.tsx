import { useEffect, useState } from 'react';

import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { Modal } from '@/components/ui/Modal';
import { TextField, Toggle } from '@/components/ui/Field';
import { describeMode } from '@/features/files/format';
import type { FileEntry } from '@/types/api';

interface NameDialogProps {
  open: boolean;
  title: string;
  label: string;
  /** Pre-filled when renaming; empty when creating. */
  initial?: string;
  confirmLabel: string;
  loading: boolean;
  error: string | null;
  onClose: () => void;
  onSubmit: (name: string) => void;
}

/** NameDialog asks for a single name: a new folder, a new file, or a rename. */
export function NameDialog({
  open,
  title,
  label,
  initial = '',
  confirmLabel,
  loading,
  error,
  onClose,
  onSubmit,
}: NameDialogProps) {
  const [name, setName] = useState(initial);

  // Reset on open so a dialog reopened after a failure does not show the last
  // attempt's value as though it had been accepted.
  useEffect(() => {
    if (open) {
      setName(initial);
    }
  }, [open, initial]);

  const trimmed = name.trim();
  const invalid = trimmed === '' || trimmed.includes('/') || trimmed === '.' || trimmed === '..';

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={title}
      busy={loading}
      footer={
        <>
          <Button onClick={onClose} disabled={loading}>
            Cancel
          </Button>
          <Button
            variant="primary"
            loading={loading}
            disabled={invalid}
            onClick={() => onSubmit(trimmed)}
          >
            {confirmLabel}
          </Button>
        </>
      }
    >
      <form
        onSubmit={(event) => {
          event.preventDefault();
          if (!invalid) {
            onSubmit(trimmed);
          }
        }}
      >
        <TextField
          id="file-name"
          label={label}
          value={name}
          onChange={(event) => setName(event.target.value)}
          autoComplete="off"
          spellCheck={false}
          error={
            trimmed !== '' && invalid ? 'A name cannot contain "/" and cannot be "." or ".."' : null
          }
          hint="A single file or folder name, not a path."
        />
      </form>

      {error && (
        <Alert tone="danger" className="mt-3">
          {error}
        </Alert>
      )}
    </Modal>
  );
}

interface PermissionsDialogProps {
  open: boolean;
  entry: FileEntry | null;
  loading: boolean;
  error: string | null;
  onClose: () => void;
  onSubmit: (mode: string, recursive: boolean) => void;
}

/** The nine permission bits, as checkboxes over an octal value. */
const PERMISSION_ROWS = [
  { label: 'Owner', shift: 6 },
  { label: 'Group', shift: 3 },
  { label: 'Everyone', shift: 0 },
] as const;

const PERMISSION_BITS = [
  { label: 'Read', bit: 4 },
  { label: 'Write', bit: 2 },
  { label: 'Execute', bit: 1 },
] as const;

/**
 * PermissionsDialog edits a mode.
 *
 * Both forms are shown at once — the checkboxes and the octal value — because
 * hosting documentation is written in octal while the checkboxes are what make
 * a wrong value obvious.
 */
export function PermissionsDialog({
  open,
  entry,
  loading,
  error,
  onClose,
  onSubmit,
}: PermissionsDialogProps) {
  const [mode, setMode] = useState('0644');
  const [recursive, setRecursive] = useState(false);

  useEffect(() => {
    if (open && entry) {
      setMode(entry.mode);
      setRecursive(false);
    }
  }, [open, entry]);

  const parsed = Number.parseInt(mode, 8);
  const valid = !Number.isNaN(parsed) && parsed >= 0 && parsed <= 0o777;

  const toggleBit = (shift: number, bit: number) => {
    if (!valid) {
      return;
    }
    const mask = bit << shift;
    const next = (parsed & mask) === mask ? parsed & ~mask : parsed | mask;
    setMode(next.toString(8).padStart(4, '0'));
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Permissions"
      description={entry ? entry.path : undefined}
      busy={loading}
      footer={
        <>
          <Button onClick={onClose} disabled={loading}>
            Cancel
          </Button>
          <Button
            variant="primary"
            loading={loading}
            disabled={!valid}
            onClick={() => onSubmit(mode, recursive)}
          >
            Apply
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <table className="w-full text-sm">
          <thead>
            <tr className="text-left text-xs uppercase tracking-wide text-ink-muted">
              <th scope="col" className="py-1 font-medium">
                Who
              </th>
              {PERMISSION_BITS.map((bit) => (
                <th key={bit.label} scope="col" className="py-1 font-medium">
                  {bit.label}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {PERMISSION_ROWS.map((row) => (
              <tr key={row.label}>
                <th scope="row" className="py-1.5 text-left font-normal text-ink">
                  {row.label}
                </th>
                {PERMISSION_BITS.map((bit) => (
                  <td key={bit.label} className="py-1.5">
                    <input
                      type="checkbox"
                      aria-label={`${row.label} can ${bit.label.toLowerCase()}`}
                      checked={valid && ((parsed >> row.shift) & bit.bit) === bit.bit}
                      onChange={() => toggleBit(row.shift, bit.bit)}
                      className="h-4 w-4 rounded border-surface-border text-brand-600 focus:ring-brand-500"
                    />
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>

        <TextField
          id="file-mode"
          label="Octal value"
          value={mode}
          onChange={(event) => setMode(event.target.value)}
          autoComplete="off"
          spellCheck={false}
          error={valid ? null : 'Use an octal mode between 0000 and 0777'}
          suffix={valid ? describeMode(mode) : undefined}
          hint="The setuid, setgid and sticky bits cannot be set from the panel."
        />

        {entry?.type === 'directory' && (
          <Toggle
            id="file-mode-recursive"
            label="Apply to everything inside"
            description="Changes every file and folder below this one. Symlinks are skipped."
            checked={recursive}
            onChange={setRecursive}
          />
        )}

        {error && <Alert tone="danger">{error}</Alert>}
      </div>
    </Modal>
  );
}
