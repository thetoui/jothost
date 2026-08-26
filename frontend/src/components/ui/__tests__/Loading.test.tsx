import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';

import { Button } from '@/components/ui/Button';
import { EmptyState, ProgressBar, SkeletonRows } from '@/components/ui/Loading';

describe('ProgressBar', () => {
  it('reports a known percentage', () => {
    render(<ProgressBar value={42} label="Creating website" />);

    const bar = screen.getByRole('progressbar', { name: 'Creating website' });
    expect(bar).toHaveAttribute('aria-valuenow', '42');
    expect(bar).toHaveAttribute('aria-valuemin', '0');
    expect(bar).toHaveAttribute('aria-valuemax', '100');
  });

  // The panel dispatches work to a host that does not always report progress.
  // A bar pinned at 0% reads as "stuck" when the truth is "running".
  it('is indeterminate when no value is known', () => {
    render(<ProgressBar label="Working" />);

    const bar = screen.getByRole('progressbar', { name: 'Working' });
    expect(bar).not.toHaveAttribute('aria-valuenow');
  });

  it('clamps a value outside the range', () => {
    render(<ProgressBar value={150} label="Overshot" />);
    expect(screen.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '100');
  });
});

describe('SkeletonRows', () => {
  // A skeleton is decorative; what a screen reader needs is the fact that
  // something is loading, said once.
  it('announces loading without reading out the placeholders', () => {
    render(<SkeletonRows rows={3} />);

    const status = screen.getByRole('status');
    expect(status).toHaveTextContent('Loading');
    expect(status.querySelectorAll('[aria-hidden="true"]').length).toBeGreaterThan(0);
  });
});

describe('EmptyState', () => {
  it('explains the emptiness and offers a way out', () => {
    render(
      <EmptyState
        icon={<span />}
        title="No websites yet"
        description="Create one to serve a domain."
        action={<Button>New website</Button>}
      />,
    );

    expect(screen.getByText('No websites yet')).toBeInTheDocument();
    expect(screen.getByText('Create one to serve a domain.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'New website' })).toBeInTheDocument();
  });
});

describe('Button', () => {
  it('disables itself and reports busy while loading', () => {
    render(<Button loading>Saving</Button>);

    const button = screen.getByRole('button', { name: /Saving/ });
    expect(button).toBeDisabled();
    expect(button).toHaveAttribute('aria-busy', 'true');
  });

  it('is not busy by default', () => {
    render(<Button>Save</Button>);

    const button = screen.getByRole('button', { name: 'Save' });
    expect(button).toBeEnabled();
    expect(button).not.toHaveAttribute('aria-busy');
  });
});
