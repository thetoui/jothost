import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { Modal } from '@/components/ui/Modal';

describe('Modal', () => {
  it('renders nothing while closed', () => {
    render(
      <Modal open={false} onClose={vi.fn()} title="Hidden">
        <p>Body</p>
      </Modal>,
    );

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('is a labelled modal dialog when open', () => {
    render(
      <Modal open onClose={vi.fn()} title="New website" description="Provisioned on the host.">
        <p>Body</p>
      </Modal>,
    );

    const dialog = screen.getByRole('dialog');
    expect(dialog).toHaveAttribute('aria-modal', 'true');
    expect(dialog).toHaveAccessibleName('New website');
    expect(dialog).toHaveAccessibleDescription('Provisioned on the host.');
  });

  // A form dialog should leave the cursor ready to type, not one Tab away from
  // dismissing the thing that was just opened.
  it('focuses the first field rather than the close button', async () => {
    render(
      <Modal open onClose={vi.fn()} title="New website">
        <label htmlFor="domain">Domain</label>
        <input id="domain" />
      </Modal>,
    );

    expect(screen.getByLabelText('Domain')).toHaveFocus();
  });

  it('focuses the dialog itself when it has no fields', async () => {
    render(
      <Modal open onClose={vi.fn()} title="Notice">
        <p>Nothing to fill in.</p>
      </Modal>,
    );

    // Falls back to the first focusable, which is the close button.
    expect(screen.getByRole('button', { name: 'Close dialog' })).toHaveFocus();
  });

  it('closes on Escape', async () => {
    const onClose = vi.fn();
    const user = userEvent.setup();
    render(
      <Modal open onClose={onClose} title="Closable">
        <p>Body</p>
      </Modal>,
    );

    await user.keyboard('{Escape}');
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  // Dismissing mid-submit leaves the user with no idea whether what they asked
  // for happened.
  it('refuses to close while busy', async () => {
    const onClose = vi.fn();
    const user = userEvent.setup();
    render(
      <Modal open onClose={onClose} title="Working" busy>
        <p>Body</p>
      </Modal>,
    );

    await user.keyboard('{Escape}');
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByRole('button', { name: 'Close dialog' })).toBeDisabled();
  });

  it('locks the page behind it and releases the lock on close', async () => {
    function Harness() {
      const [open, setOpen] = useState(true);
      return (
        <Modal open={open} onClose={() => setOpen(false)} title="Scroll lock">
          <p>Body</p>
        </Modal>
      );
    }

    const user = userEvent.setup();
    render(<Harness />);

    expect(document.body.style.overflow).toBe('hidden');

    await user.click(screen.getByRole('button', { name: 'Close dialog' }));
    expect(document.body.style.overflow).not.toBe('hidden');
  });

  // Tab must cycle within the dialog: the page behind is still rendered and
  // still focusable.
  it('traps Tab inside the dialog', async () => {
    const user = userEvent.setup();
    render(
      <>
        <button type="button">Outside</button>
        <Modal open onClose={vi.fn()} title="Trapped" footer={<button type="button">Save</button>}>
          <label htmlFor="field">Field</label>
          <input id="field" />
        </Modal>
      </>,
    );

    const outside = screen.getByRole('button', { name: 'Outside' });
    const save = screen.getByRole('button', { name: 'Save' });

    // Walking forward past the last control wraps to the first, never landing
    // on the button behind the backdrop.
    save.focus();
    await user.tab();
    expect(outside).not.toHaveFocus();
    expect(screen.getByRole('dialog')).toContainElement(document.activeElement as HTMLElement);
  });

  it('renders in a portal, outside the triggering tree', () => {
    const { container } = render(
      <Modal open onClose={vi.fn()} title="Portalled">
        <p>Body</p>
      </Modal>,
    );

    expect(container.querySelector('[role="dialog"]')).toBeNull();
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });
});

describe('ConfirmDialog', () => {
  it('names what is about to happen and runs it on confirm', async () => {
    const onConfirm = vi.fn();
    const user = userEvent.setup();

    render(
      <ConfirmDialog
        open
        onClose={vi.fn()}
        onConfirm={onConfirm}
        title="Delete this website?"
        description="This cannot be undone."
        confirmLabel="Delete website"
        destructive
      >
        <p>example.test and its files are removed.</p>
      </ConfirmDialog>,
    );

    // The point of a real dialog over window.confirm: it can say which record.
    expect(screen.getByText(/example\.test and its files are removed\./)).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: /Delete website/ }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it('stays open and reports a failure rather than vanishing', () => {
    render(
      <ConfirmDialog
        open
        onClose={vi.fn()}
        onConfirm={vi.fn()}
        title="Delete this website?"
        error="That domain is still in use"
      />,
    );

    expect(screen.getByRole('dialog')).toBeInTheDocument();
    expect(screen.getByRole('alert')).toHaveTextContent('That domain is still in use');
  });

  it('disables its actions while the work runs', () => {
    render(
      <ConfirmDialog
        open
        onClose={vi.fn()}
        onConfirm={vi.fn()}
        title="Delete this website?"
        confirmLabel="Delete website"
        loading
      />,
    );

    expect(screen.getByRole('button', { name: /Delete website/ })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeDisabled();
  });
});
